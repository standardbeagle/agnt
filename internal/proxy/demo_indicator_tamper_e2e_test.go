//go:build chromee2e

package proxy

// demo_indicator_tamper_e2e_test.go is the INV-14 adversarial gate (spec §11 →
// P10): the demo-indicator disclosure badge survives a HOSTILE publisher, not
// merely a benign SPA.
//
// Why this file exists separately from the module's own tests: S7 shipped
// demo-indicator.js with a MutationObserver whose stated threat model is a
// framework replacing the DOM ("hostile removal is P10's slice and is not
// claimed here" — demo-indicator.js header). Meanwhile INV-6 was retired, so a
// variant now legitimately carries raw CSS/HTML/JS. The publisher can therefore
// AIM at the badge. Nothing proved it loses until this file.
//
// WHAT AN ASSERTION HERE MUST BE. A closed shadow root resists ordinary page CSS
// but is not absolute, and DOM presence is not disclosure: a host that is present
// but collapsed, transparent, or parked off-screen discloses nothing. Every
// assertion below therefore reads OBSERVED RENDERING off the host — used computed
// values plus a laid-out border box inside the viewport — never `getElementById(...)
// !== null`. indicatorState() is the single reader; assertVisible is the single
// verdict.
//
// PROVENANCE OVER "IT DIDN'T WORK" (publish-security-review-lessons §8). Each
// attack ships a DECOY carrying the same weapon aimed at an ordinary element. The
// decoy's fate names which layer refused the attack, and separates "the badge won"
// from "the attack never landed" — two outcomes an indicator-only assertion cannot
// tell apart, and only one of which is evidence.
//
// Tier: real Chrome, `//go:build chromee2e` per AGENTS.md, skipIfNoBrowser →
// LOUD skip when no browser is present, never a silent pass. Run with
// `go test -tags=chromee2e -run TestE2E_PublicPlane_DemoIndicator ./internal/proxy/`.

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	cdp "github.com/chromedp/chromedp"
	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// demoIndicatorHostID mirrors HOST_ID in demo-indicator.js. Hardcoded on purpose:
// the id is the handle a hostile publisher aims at, so the test must aim at the
// same literal rather than at whatever the module currently calls itself.
const demoIndicatorHostID = "agnt-demo-indicator"

// indicatorState is the observed rendering of the badge host, read from the page.
// Presence is one field among many and is never sufficient on its own.
type indicatorState struct {
	Present    bool    `json:"present"`
	Display    string  `json:"display"`
	Visibility string  `json:"visibility"`
	Opacity    string  `json:"opacity"`
	Position   string  `json:"position"`
	Transform  string  `json:"transform"`
	ZIndex     string  `json:"zIndex"`
	ClipPath   string  `json:"clipPath"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	Top        float64 `json:"top"`
	Left       float64 `json:"left"`
	InViewport bool    `json:"inViewport"`
	// Shadow is String(host.shadowRoot): "null" for a CLOSED root (the shipped
	// shape) and "[object ShadowRoot]" if it ever became page-reachable.
	Shadow string `json:"shadow"`
	// Count is how many elements carry the host id — >1 means something squatted
	// it, which the module's idempotent mount would then mistake for itself.
	Count int `json:"count"`
}

// indicatorProbe is the page-side reader. It is deliberately computed-style based:
// used values already fold in the whole cascade, so an attack that wins anywhere
// (page rule, inline style, injected stylesheet) shows up here without the test
// having to know which mechanism was tried.
const indicatorProbe = `(function(){
  var id = ` + "`" + demoIndicatorHostID + "`" + `;
  var all = document.querySelectorAll('#' + id);
  var h = document.getElementById(id);
  if (!h) { return {present:false, count:all.length}; }
  var cs = getComputedStyle(h);
  var r = h.getBoundingClientRect();
  return {
    present: true,
    count: all.length,
    display: cs.display,
    visibility: cs.visibility,
    opacity: cs.opacity,
    position: cs.position,
    transform: cs.transform,
    zIndex: cs.zIndex,
    clipPath: cs.clipPath,
    width: r.width, height: r.height, top: r.top, left: r.left,
    inViewport: r.width > 0 && r.height > 0 && r.bottom > 0 && r.right > 0 &&
                r.top < window.innerHeight && r.left < window.innerWidth,
    shadow: String(h.shadowRoot)
  };
})()`

// readIndicator evaluates the probe on the live page.
func readIndicator(t *testing.T, ctx context.Context) indicatorState {
	t.Helper()
	var st indicatorState
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(indicatorProbe, &st)))
	return st
}

// assertVisible is the single verdict used by every attack: the disclosure is
// rendered, not merely attached. Each clause names a distinct way to be present
// and unseen, because an attacker only needs one of them.
func assertVisible(t *testing.T, st indicatorState, attack string) {
	t.Helper()
	require.True(t, st.Present, "%s: the demo indicator host is GONE from the document", attack)
	assert.Equal(t, 1, st.Count, "%s: exactly one element may carry the host id (id squatting defeats the idempotent mount)", attack)
	assert.NotEqual(t, "none", st.Display, "%s: display collapsed", attack)
	assert.Equal(t, "visible", st.Visibility, "%s: visibility suppressed", attack)
	assert.Equal(t, "1", st.Opacity, "%s: opacity suppressed", attack)
	assert.Equal(t, "fixed", st.Position, "%s: the pinned position was overridden", attack)
	assert.Contains(t, []string{"none", ""}, st.Transform, "%s: a transform moved the badge (%s)", attack, st.Transform)
	assert.Contains(t, []string{"none", "auto", ""}, st.ClipPath, "%s: the badge was clipped away (%s)", attack, st.ClipPath)
	assert.Greater(t, st.Width, 0.0, "%s: the badge has zero laid-out width", attack)
	assert.Greater(t, st.Height, 0.0, "%s: the badge has zero laid-out height", attack)
	assert.True(t, st.InViewport,
		"%s: the badge is laid out OUTSIDE the viewport (top=%v left=%v w=%v h=%v)", attack, st.Top, st.Left, st.Width, st.Height)
	assert.Equal(t, "null", st.Shadow, "%s: the shadow root must stay CLOSED and page-unreachable", attack)
}

// publishTamperShare publishes a share whose single variant carries ops. Each
// attack gets its own revision because an addScript body is pinned per revision.
func publishTamperShare(t *testing.T, p *e2ePlane, id string, ops []publish.Op) string {
	t.Helper()
	wt := e2eWalkthrough()
	wt.ID = "wt-" + id
	wt.VariantSet.ID = "set-" + id
	wt.VariantSet.Variants[0].Ops = ops
	_, token, err := p.store.Create(wt, e2eProjectA)
	require.NoError(t, err, "the hostile variant must PUBLISH — INV-14 is a runtime guarantee, not a validator rejection")
	return token
}

// decoyHTML is the control subtree every attack also aims at. #decoy is an
// ordinary page element with no shadow root and no !important geometry: whatever
// the attack does to the badge, it should do to #decoy — unless a layer above the
// badge refused the attack outright, which is exactly what the decoy reports.
const decoyHTML = `<div id="app"><h1 id="title">Original</h1><p class="msg">hi</p>` +
	`<div id="decoy">decoy</div></div>`

// loadTamperPage navigates to the real artifact, waits for the real RolePublic
// bundle, appends the decoy subtree (insertAdjacentHTML, NOT body.innerHTML — a
// wipe would remove the badge and this helper must not do the attack's work for
// it), fetches the share's OWN variant set same-origin, and applies the variant.
func loadTamperPage(t *testing.T, ctx context.Context, p *e2ePlane, token string) {
	t.Helper()
	// The poll waits for the BUNDLE only, deliberately not for the badge: the
	// artifact auto-boots and applies its own variant set, so on a hostile share
	// the attack has already fired by the time this returns. Waiting on the badge
	// here would turn every successful attack into an opaque poll timeout instead
	// of a named assertion failure. The pre-attack state is established once per
	// test by assertBaselineRenders below.
	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(p.srv.URL+"/s/"+token),
		cdp.Poll(`!!window.__variantEngine`, &ready, cdp.WithPollingTimeout(10*time.Second)),
	))
	require.True(t, ready, "the served RolePublic bundle must load")

	awaitPromise := func(pp *runtime.EvaluateParams) *runtime.EvaluateParams { return pp.WithAwaitPromise(true) }
	var applied bool
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(async () => {
		document.body.insertAdjacentHTML('beforeend', `+jsonString(decoyHTML)+`);
		const vs = await (await fetch('/s/`+token+`/variants.json')).json();
		window.__eng = window.__variantEngine.create(vs, { when: 'any' });
		window.__eng.apply('a');
		return true;
	})()`, &applied, awaitPromise)))
	require.True(t, applied, "the variant must apply on the real page")
	// Let the module's MutationObserver drain: re-assert runs on the microtask
	// checkpoint, and a laid-out box needs a frame.
	require.NoError(t, cdp.Run(ctx, cdp.Sleep(300*time.Millisecond)))
}

// decoyState reads the same properties off the control element.
func decoyState(t *testing.T, ctx context.Context) indicatorState {
	t.Helper()
	var st indicatorState
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(function(){
	  var h = document.getElementById('decoy');
	  if (!h) { return {present:false, count:0}; }
	  var cs = getComputedStyle(h); var r = h.getBoundingClientRect();
	  return {present:true, count:1, display:cs.display, visibility:cs.visibility, opacity:cs.opacity,
	          width:r.width, height:r.height, shadow:'null'};
	})()`, &st)))
	return st
}

// assertBaselineRenders is the CONTROL run: an unattacked share on the same
// plane and the same browser. It fixes what "rendered" looks like here before any
// attack, so a later failure is attributable to the attack rather than to the
// harness, the headless viewport, or a bundle that never loaded.
func assertBaselineRenders(t *testing.T, ctx context.Context, p *e2ePlane) {
	t.Helper()
	token := publishTamperShare(t, p, "baseline", e2eWalkthrough().VariantSet.Variants[0].Ops)
	loadTamperPage(t, ctx, p, token)
	assertVisible(t, readIndicator(t, ctx), "control: benign variant")
}

// TestE2E_PublicPlane_DemoIndicatorSurvivesVariantCSS covers INV-14's CSS half:
// a published variant whose styling aims at the badge. Two mechanisms exist and
// they are refused at DIFFERENT layers, which is why they are separate subtests
// rather than one "CSS cannot hide it" claim:
//
//	applyStyle — CSSOM inline properties on the host. It LANDS (CSSOM writes are
//	  not subject to style-src) and LOSES to the shadow tree's `:host{…!important}`,
//	  because an important declaration in the shadow tree beats one in the outer
//	  tree, and an inline declaration set through setProperty cannot be important.
//	addStyle  — a raw <style> element. It never lands at all: the public CSP's
//	  style-src carries no 'unsafe-inline' and the element bears no nonce.
func TestE2E_PublicPlane_DemoIndicatorSurvivesVariantCSS(t *testing.T) {
	skipIfNoBrowser(t)
	p := newE2EPlane(t)
	ctx := newPublicBrowser(t)
	assertBaselineRenders(t, ctx, p)

	t.Run("applyStyle_aimed_at_the_host_loses_to_the_shadow_important", func(t *testing.T) {
		// Every documented hiding vector at once: collapse, blank, fade, park
		// off-screen, bury under the stack, clip away.
		hide := map[string]string{
			"display":    "none",
			"visibility": "hidden",
			"opacity":    "0",
			"position":   "absolute",
			"left":       "-99999px",
			"top":        "-99999px",
			"transform":  "translate(-99999px,-99999px)",
			"z-index":    "-2147483647",
			"clip-path":  "inset(100%)",
			"width":      "0px",
			"height":     "0px",
		}
		token := publishTamperShare(t, p, "applystyle", []publish.Op{
			{Op: publish.OpApplyStyle, Selector: "#" + demoIndicatorHostID, Props: hide},
			{Op: publish.OpApplyStyle, Selector: "#decoy", Props: hide},
		})
		loadTamperPage(t, ctx, p, token)

		// PREMISE, asserted rather than assumed: the identical weapon really did
		// fire. Without this the badge's survival could just mean the op never ran.
		d := decoyState(t, ctx)
		require.True(t, d.Present, "premise broken: the decoy element is missing")
		require.Equal(t, "none", d.Display,
			"premise broken: applyStyle did not land on an ORDINARY element, so the badge's survival proves nothing")

		assertVisible(t, readIndicator(t, ctx), "applyStyle display:none/opacity:0/offscreen/clip")
	})

	t.Run("addStyle_raw_css_is_refused_by_csp_before_it_can_bury_the_badge", func(t *testing.T) {
		css := "#" + demoIndicatorHostID + "{display:none!important;visibility:hidden!important;opacity:0!important}\n" +
			"#decoy{display:none!important}\n" +
			// An overlay rule too: raw CSS is the only way to reach ::before/::after
			// and full-viewport cover geometry in one op.
			"#app::after{content:'';position:fixed;inset:0;z-index:2147483647;background:#fff}"
		token := publishTamperShare(t, p, "addstyle", []publish.Op{
			{Op: publish.OpAddStyle, CSS: css},
		})
		loadTamperPage(t, ctx, p, token)

		// PROVENANCE: name the layer that refused it. styleEl.sheet === null on an
		// attached <style> is the browser reporting a CSP-blocked stylesheet — that
		// is the first refusal, and it is why the decoy below is still visible.
		var sheetNull, attached bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`!!document.querySelector('#__variant-engine-root style')`, &attached),
			cdp.Evaluate(`(function(){var s=document.querySelector('#__variant-engine-root style');return !!s && s.sheet === null;})()`, &sheetNull),
		))
		assert.True(t, attached, "premise broken: the engine did not emit the authored <style> at all")
		assert.True(t, sheetNull,
			"the raw authored stylesheet must be REFUSED by the public CSP (style-src carries no 'unsafe-inline' and the element bears no nonce)")

		d := decoyState(t, ctx)
		assert.NotEqual(t, "none", d.Display,
			"corroboration: with the stylesheet refused, even an ORDINARY element is untouched")

		assertVisible(t, readIndicator(t, ctx), "addStyle raw CSS display:none!important + full-viewport ::after overlay")
	})
}

// TestE2E_PublicPlane_DemoIndicatorSurvivesVariantHTML covers INV-14's markup
// half: setHTML wiping the subtree the badge lives in. This is the vector the
// module's MutationObserver is built for, driven here from a PUBLISHED variant on
// the REAL artifact rather than from a hand-built fixture page.
func TestE2E_PublicPlane_DemoIndicatorSurvivesVariantHTML(t *testing.T) {
	skipIfNoBrowser(t)
	p := newE2EPlane(t)
	ctx := newPublicBrowser(t)
	assertBaselineRenders(t, ctx, p)

	t.Run("setHTML_wiping_the_body_subtree_does_not_erase_the_disclosure", func(t *testing.T) {
		// The selector grammar admits a bare type selector, so `body` is reachable:
		// this replaces EVERY child of body, the badge host included.
		token := publishTamperShare(t, p, "sethtml", []publish.Op{
			{Op: publish.OpSetHTML, Selector: "body", HTML: `<div id="app"><h1 id="title">Wiped</h1><div id="decoy">decoy</div></div>`},
		})
		loadTamperPage(t, ctx, p, token)

		// PREMISE: the wipe really happened — the pre-attack subtree is gone.
		var wiped string
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`document.getElementById('title').textContent`, &wiped)))
		require.Equal(t, "Wiped", wiped, "premise broken: setHTML did not replace the body subtree")

		assertVisible(t, readIndicator(t, ctx), "setHTML wipe of body")
	})

	// addScript is the sharpest publisher-reachable weapon on this plane: since
	// S3/S4 the authored body's sha256 is PINNED into script-src, so unlike every
	// other raw payload it genuinely EXECUTES. What it can reach is therefore the
	// real boundary of INV-14, and the two properties below are the ones the
	// shipped module actually holds.
	//
	// SCOPE, stated rather than implied: an authored script runs in the same realm
	// as the module, so this subtest covers the two vectors a hostile script has
	// no answer to — a one-shot removal (the observer wins) and reaching into the
	// badge's internals (the root is CLOSED). It does NOT claim the module beats
	// arbitrary same-realm script; the vectors that defeat it are recorded on the
	// task rather than asserted here, because a test that asserted them would be
	// pinning a gap as intended behaviour.
	t.Run("authored_script_one_shot_removal_and_closed_shadow_root", func(t *testing.T) {
		code := `window.__attackRan = 'yes';` +
			`var h = document.getElementById('` + demoIndicatorHostID + `');` +
			// The shadow root is the interesting read: a page handle on it would
			// let the attacker edit the badge's own stylesheet from the inside.
			`window.__attackShadow = String(h && h.shadowRoot);` +
			`if (h) { h.remove(); }`
		token := publishTamperShare(t, p, "addscript", []publish.Op{
			{Op: publish.OpAddScript, Code: code},
		})
		loadTamperPage(t, ctx, p, token)

		// PREMISE: the authored body really executed. Without this the survival
		// below could just mean CSP refused the script — a different (and
		// already-covered) story, not evidence about removal.
		var ran, shadowSeen string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`String(window.__attackRan)`, &ran),
			cdp.Evaluate(`String(window.__attackShadow)`, &shadowSeen),
		))
		require.Equal(t, "yes", ran,
			"premise broken: the authored script did not run, so this asserts nothing about hostile removal")
		assert.Equal(t, "null", shadowSeen,
			"an authored script must NOT reach the badge's shadow root (mode: 'closed')")

		assertVisible(t, readIndicator(t, ctx), "authored script node removal")
	})
}
