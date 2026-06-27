package proxy

// Real-browser coverage for window.__devtool_layout.diagnose() — the
// cause→symptom fast layout diagnostic. Drives a headless Chrome against a page
// built with one deliberate trap per check and asserts the diagnostic names the
// symptom AND the offending ancestor. Verified against a real layout engine
// because the value is entirely in computed-style / hit-testing behavior that a
// string test cannot exercise. Env-gated like the other e2e tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each block isolates exactly one bug class so a finding is unambiguous.
const layoutTrapPage = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Layout Traps</title>
<style>
  body { margin: 0; }
  /* 1. containing-block trap: transform on the ancestor captures the fixed child */
  #xform { transform: translateZ(0); width: 300px; height: 200px; position: relative; }
  #fixed-trapped { position: fixed; top: 10px; left: 10px; width: 50px; height: 50px; background: red; }
  /* 2. ineffective z-index: static element, non-flex parent */
  #plainparent { display: block; }
  #zbad { position: static; z-index: 50; width: 40px; height: 40px; background: green; }
  /* 3. clipped descendant: child taller than a hidden-overflow box */
  #clipbox { overflow: hidden; height: 30px; width: 100px; background: #eee; }
  #clipped { height: 200px; background: blue; }
  /* 4. click interception: transparent full-viewport overlay above the button */
  #realbtn { position: relative; z-index: 1; margin: 12px; }
  #overlay { position: fixed; inset: 0; z-index: 9999; background: rgba(0,0,0,0); }
</style></head>
<body>
  <div id="xform"><div id="fixed-trapped"></div></div>
  <div id="plainparent"><div id="zbad">z</div></div>
  <div id="clipbox"><div id="clipped">tall</div></div>
  <button id="realbtn" type="button">Click me</button>
  <div id="overlay"></div>
</body></html>`

type diagFinding struct {
	Check         string `json:"check"`
	Severity      string `json:"severity"`
	Selector      string `json:"selector"`
	Cause         string `json:"cause"`
	CauseProperty string `json:"cause_property"`
	Detail        string `json:"detail"`
	Fix           string `json:"fix"`
	Avoid         string `json:"avoid"`
}

type diagResult struct {
	Findings []diagFinding `json:"findings"`
	Count    int           `json:"count"`
	Scanned  int           `json:"scanned"`
	Capped   bool          `json:"capped"`
}

// firstOf returns the first finding for the given check, or false.
func firstOf(r diagResult, check string) (diagFinding, bool) {
	for _, f := range r.Findings {
		if f.Check == check {
			return f, true
		}
	}
	return diagFinding{}, false
}

func TestE2E_LayoutDiagnose_RealBrowserTraps(t *testing.T) {
	skipIfNoBrowser(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, layoutTrapPage)
	})
	backend := httptest.NewServer(mux)
	t.Cleanup(backend.Close)

	ps := startWrapProxy(t, backend)
	contentURL := "http://" + ps.ListenAddr + "/?" + frameMarkerParam + "=f1"

	allocCtx, allocCancel := cdp.NewExecAllocator(context.Background(),
		append(cdp.DefaultExecAllocatorOptions[:],
			cdp.Flag("headless", true),
			cdp.Flag("no-sandbox", true),
			cdp.Flag("disable-gpu", true),
		)...)
	t.Cleanup(allocCancel)
	ctx, cancel := cdp.NewContext(allocCtx)
	t.Cleanup(cancel)
	ctx, tcancel := context.WithTimeout(ctx, 60*time.Second)
	t.Cleanup(tcancel)

	var raw string
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(contentURL),
		cdp.WaitVisible(`#realbtn`, cdp.ByID),
		cdp.Sleep(300*time.Millisecond), // let layout + instrumentation settle
		cdp.Evaluate(`JSON.stringify(window.__devtool_layout.diagnose())`, &raw),
	))

	var res diagResult
	require.NoError(t, json.Unmarshal([]byte(raw), &res), "diagnose() returned: %s", raw)
	require.Positive(t, res.Scanned, "scanned some elements")

	// 1. containing-block trap: the fixed child is captured by #xform's transform.
	if f, ok := firstOf(res, "containing-block-trap"); assert.True(t, ok, "fixed-in-transform trap found") {
		assert.Contains(t, f.Selector, "fixed-trapped")
		assert.Contains(t, f.Cause, "xform", "names the transformed ancestor as the cause")
		assert.Equal(t, "transform", f.CauseProperty)
		assert.Equal(t, "high", f.Severity)
		assert.NotEmpty(t, f.Avoid)
	}

	// 2. ineffective z-index: #zbad is static with a block parent.
	if f, ok := firstOf(res, "ineffective-zindex"); assert.True(t, ok, "static z-index found") {
		assert.Contains(t, f.Selector, "zbad")
		assert.Contains(t, f.Detail, "ignored")
		assert.Contains(t, f.Fix, "position:relative")
	}

	// 3. clipped descendant: #clipped overflows #clipbox vertically.
	if f, ok := firstOf(res, "clipped-descendant"); assert.True(t, ok, "clipped child found") {
		assert.Contains(t, f.Selector, "clipped")
		assert.Contains(t, f.Cause, "clipbox", "names the clipping ancestor")
	}

	// 4. click interception: #overlay covers #realbtn's center.
	if f, ok := firstOf(res, "click-interception"); assert.True(t, ok, "intercepted button found") {
		assert.Contains(t, f.Selector, "realbtn")
		assert.Contains(t, f.Cause, "overlay", "names the intercepting overlay")
		assert.Equal(t, "high", f.Severity)
	}

	if testing.Verbose() {
		for _, f := range res.Findings {
			t.Logf("[%s/%s] %s  cause=%s(%s)", f.Severity, f.Check, f.Selector, f.Cause, f.CauseProperty)
		}
	}
}
