package replaytest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	cdp "github.com/standardbeagle/agnt/internal/chromedp"
)

// errCollectorJS installs a tiny window.__rt_errors collector. It runs as a
// second addScriptToEvaluateOnNewDocument so uncaught errors and rejected
// promises are captured before any app code runs.
const errCollectorJS = `window.__rt_errors=[];` +
	`window.addEventListener('error',function(e){window.__rt_errors.push(String(e.message));});` +
	`window.addEventListener('unhandledrejection',function(e){window.__rt_errors.push(String(e.reason));});`

// Driver runs the chromedp seed lane: it injects the worker bundle (which mocks
// the network entirely in-page) before navigation, drives a Scenario's Steps,
// evaluates Assertions, captures JS errors, and returns a *Report. It is THIN —
// all request matching and response mutation lives in the worker bundle.
type Driver struct {
	proxyID string
}

// NewDriver returns a seed-lane driver. proxyID may be "" when the bundle fully
// mocks the network in-page (the default); when set, the chromedp session is
// routed through that proxy.
func NewDriver(proxyID string) *Driver {
	return &Driver{proxyID: proxyID}
}

// RunSeed injects the worker bundle for the given preset, drives the scenario's
// steps once, and records a single SeedResult labeled by preset ("baseline"
// when preset==""). It fails fast on bundle generation or browser errors.
func (d *Driver) RunSeed(ctx context.Context, sc *Scenario, preset string) (*Report, error) {
	js, err := GenerateBundle(sc, preset)
	if err != nil {
		return nil, fmt.Errorf("generate bundle: %w", err)
	}

	rep := NewReport(sc.Name)

	label := preset
	if label == "" {
		label = "baseline"
	}

	sess := cdp.NewSession(cdp.SessionConfig{
		ID:       "replay-seed-" + label,
		Headless: true,
		ProxyURL: proxyURLFor(d.proxyID),
		Timeout:  30 * time.Second,
	})
	if err := sess.Start(ctx); err != nil {
		return nil, fmt.Errorf("start chromedp session: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sess.Stop(stopCtx)
	}()

	// Install the bundle + error collector BEFORE navigation so the fetch/XHR
	// shim is in place before the SPA's own scripts run.
	if err := sess.Run(chromedp.ActionFunc(func(ctx context.Context) error {
		if _, err := page.AddScriptToEvaluateOnNewDocument(js).Do(ctx); err != nil {
			return err
		}
		if _, err := page.AddScriptToEvaluateOnNewDocument(errCollectorJS).Do(ctx); err != nil {
			return err
		}
		return nil
	})); err != nil {
		return nil, fmt.Errorf("inject bundle: %w", err)
	}

	var failures []string
	currentRoute := sc.BaseURL

	for _, step := range sc.Steps {
		route, stepFailures, err := d.runStep(sess, sc, step)
		if err != nil {
			return nil, err
		}
		if route != "" {
			currentRoute = route
		}
		failures = append(failures, stepFailures...)
	}

	// Collect JS errors accumulated across the run.
	var jsErrors []string
	if err := sess.Run(chromedp.Evaluate(`window.__rt_errors || []`, &jsErrors)); err != nil {
		return nil, fmt.Errorf("read js errors: %w", err)
	}
	for _, e := range jsErrors {
		rep.AddCrash(currentRoute, "", e)
	}

	rep.AddSeedResult(label, len(failures) == 0 && len(jsErrors) == 0, failures)
	return rep, nil
}

// runStep performs a single step's action, settles the DOM, recomputes the
// DOMSignature, and evaluates the step's assertions. It returns the route after
// the action (non-empty only for navigate), and any assertion failure strings.
func (d *Driver) runStep(sess *cdp.AutomationSession, sc *Scenario, step Step) (route string, failures []string, err error) {
	switch step.Kind {
	case StepNavigate:
		route = joinURL(sc.BaseURL, step.Selector)
		if err = sess.Run(chromedp.Navigate(route)); err != nil {
			return "", nil, fmt.Errorf("navigate %s: %w", route, err)
		}
	case StepClick:
		if err = sess.Run(chromedp.Click(step.Selector, chromedp.ByQuery)); err != nil {
			return "", nil, fmt.Errorf("click %s: %w", step.Selector, err)
		}
	case StepInput:
		if err = sess.Run(chromedp.SendKeys(step.Selector, step.Value, chromedp.ByQuery)); err != nil {
			return "", nil, fmt.Errorf("input %s: %w", step.Selector, err)
		}
	case StepSubmit:
		if err = sess.Run(chromedp.Submit(step.Selector, chromedp.ByQuery)); err != nil {
			return "", nil, fmt.Errorf("submit %s: %w", step.Selector, err)
		}
	default:
		return "", nil, fmt.Errorf("unknown step kind %q", step.Kind)
	}

	// Wait for the DOM to settle: body ready, then a short async-network drain.
	if err = sess.Run(chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		return "", nil, fmt.Errorf("wait body: %w", err)
	}
	if err = sess.Run(chromedp.Sleep(300 * time.Millisecond)); err != nil {
		return "", nil, fmt.Errorf("settle: %w", err)
	}

	failures = d.evalAssertions(sess, step)

	// DOM signature regression check (volatile attrs stripped so cosmetic noise
	// does not register). Only fires when the scenario carries a recorded
	// baseline for this step.
	if step.DOMSignature != "" {
		var html string
		if e := sess.Run(chromedp.OuterHTML("body", &html, chromedp.ByQuery)); e == nil {
			if got := DOMSignature(html, defaultVolatileAttrs); got != step.DOMSignature {
				failures = append(failures, fmt.Sprintf("dom signature changed: want %s got %s", step.DOMSignature, got))
			}
		}
	}
	return route, failures, nil
}

// defaultVolatileAttrs are attributes whose values commonly churn between runs
// (nonces, generated ids) and are stripped before signing the DOM.
var defaultVolatileAttrs = []string{"nonce", "data-reactid", "data-react-checksum"}

// evalAssertions evaluates a step's assertions against the live DOM and returns
// a failure string per failed assertion. Masked assertions are skipped.
func (d *Driver) evalAssertions(sess *cdp.AutomationSession, step Step) []string {
	var failures []string
	for _, a := range step.Assertions {
		switch a.Type {
		case AssertText:
			if a.Mask {
				continue
			}
			var got string
			expr := fmt.Sprintf(
				`(function(){var el=document.querySelector(%s);return el?(el.textContent||''):null;})()`,
				jsString(a.Selector))
			if err := sess.Run(chromedp.Evaluate(expr, &got)); err != nil {
				failures = append(failures, fmt.Sprintf("assert text %s: eval error: %v", a.Selector, err))
				continue
			}
			if got != a.Expect {
				failures = append(failures, fmt.Sprintf("assert text %s: want %q got %q", a.Selector, a.Expect, got))
			}
		case AssertPresent:
			var present bool
			expr := fmt.Sprintf(`(document.querySelector(%s) !== null)`, jsString(a.Selector))
			if err := sess.Run(chromedp.Evaluate(expr, &present)); err != nil {
				failures = append(failures, fmt.Sprintf("assert present %s: eval error: %v", a.Selector, err))
				continue
			}
			if !present {
				failures = append(failures, fmt.Sprintf("assert present %s: element missing", a.Selector))
			}
		default:
			failures = append(failures, fmt.Sprintf("unknown assertion type %q on %s", a.Type, a.Selector))
		}
	}
	return failures
}

// proxyURLFor maps a proxyID to a ProxyServer URL. An empty proxyID means the
// bundle mocks the network in-page and no proxy routing is required.
func proxyURLFor(proxyID string) string {
	if proxyID == "" {
		return ""
	}
	return proxyID
}

// joinURL joins a base URL with a step selector treated as a path/href. A blank
// or "/" selector yields the base URL unchanged.
func joinURL(base, sel string) string {
	if sel == "" || sel == "/" {
		return base
	}
	if strings.HasPrefix(sel, "http://") || strings.HasPrefix(sel, "https://") {
		return sel
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(sel, "/")
}

// jsString returns a JSON-quoted JS string literal safe for embedding in an
// evaluated expression.
func jsString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
