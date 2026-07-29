package scripts

import (
	"strings"
	"testing"
)

// Pure-logic (no-browser) gates for demo-indicator.js — the always-on disclosure
// badge that spec §9c/INV-14 makes a MANDATORY member of the RolePublic bundle.
// What is proven here is what can be proven without a browser: membership in the
// public allowlist, the CSSOM-only styling the public CSP forces, the closed
// shadow root, and the absence of any switch that could turn the disclosure off.
//
// The adversarial "a variant's raw CSS/HTML cannot hide it" e2e is a separate
// slice (spec §11 → P10) and is deliberately NOT attempted here.

// demoIndicatorSource returns the module source as the bundle builder sees it —
// via moduleScript, so a module that exists on disk but was never registered
// fails these tests instead of silently passing on the raw file bytes.
func demoIndicatorSource(t *testing.T) string {
	t.Helper()
	src := moduleScript["demo-indicator"]
	if strings.TrimSpace(src) == "" {
		t.Fatal("demo-indicator is not registered in moduleScript — the mandatory disclosure module is absent from the bundle builder")
	}
	return src
}

// TestDemoIndicatorIsMandatoryPublicMember pins the load-bearing registration:
// the module is in the CLOSED rolePublicModules allowlist, the bundle builder
// emits it for RolePublic, and its disclosure text reaches the assembled bytes.
// Membership is what makes it ship on BOTH public artifact paths, since both
// serve the same RolePublic bundle.
func TestDemoIndicatorIsMandatoryPublicMember(t *testing.T) {
	if !rolePublicModules["demo-indicator"] {
		t.Fatal("demo-indicator must be a member of the rolePublicModules allowlist (INV-14: mandatory, not opt-in)")
	}
	found := false
	for _, name := range publicMembers() {
		if name == "demo-indicator" {
			found = true
		}
	}
	if !found {
		t.Fatalf("demo-indicator missing from the assembled RolePublic membership %v", publicMembers())
	}

	bundle := GetCombinedScriptForRole(RolePublic)
	if !strings.Contains(bundle, "// demo-indicator module\n") {
		t.Error("RolePublic bundle does not carry the demo-indicator module marker")
	}
	// The disclosure text itself must survive into the served bytes: a member
	// whose text got stripped is a badge that renders blank.
	for _, want := range []string{"Demo walkthrough", "not the live site"} {
		if !strings.Contains(bundle, want) {
			t.Errorf("RolePublic bundle missing disclosure text %q", want)
		}
	}
	// It must NOT be in the dev-role subtractive map: the other public members
	// are shared-and-inert in dev bundles, and this one self-gates the same way.
	if r, ok := moduleRole["demo-indicator"]; ok {
		t.Errorf("demo-indicator should not be classified in moduleRole (got %q); it is allowlisted for RolePublic and inert elsewhere", r)
	}
}

// demoIndicatorText returns the single disclosure string literal the module
// renders, read out of the source rather than restated here, so the assertions
// below are about the shipped constant and not about a copy of it.
func demoIndicatorText(t *testing.T) string {
	t.Helper()
	js := demoIndicatorSource(t)
	const marker = "var TEXT = '"
	i := strings.Index(js, marker)
	if i < 0 {
		t.Fatal("demo-indicator no longer declares a single TEXT disclosure constant")
	}
	rest := js[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		t.Fatal("demo-indicator TEXT constant is unterminated")
	}
	return rest[:j]
}

// TestDemoIndicatorDisclosureTextIsPathNeutral is the honesty assertion. The badge
// ships on BOTH public artifact paths off the same bundle bytes, and on the
// self-contained path there is no upstream and nothing is proxied — so a claim of
// proxying is false on exactly one path. For a control whose only job is to be
// accurate, that is the worst possible direction to be wrong in. The text must
// therefore assert nothing about proxying while still naming a demo and
// disclaiming the live site.
func TestDemoIndicatorDisclosureTextIsPathNeutral(t *testing.T) {
	text := demoIndicatorText(t)

	for _, claim := range []string{"proxied", "proxy", "upstream", "live site itself", "this site"} {
		if strings.Contains(strings.ToLower(text), claim) {
			t.Errorf("disclosure text %q asserts %q, which is false on the self-contained path (no upstream exists there)", text, claim)
		}
	}
	// The three clauses that must survive any rewording: it is a demo, it is a
	// walkthrough, and it is not the live site. (The agnt brand ships as its own
	// element, asserted separately.)
	for _, want := range []string{"Demo", "walkthrough", "not the live site"} {
		if !strings.Contains(text, want) {
			t.Errorf("disclosure text %q dropped the required clause %q", text, want)
		}
	}
	if !strings.Contains(demoIndicatorSource(t), "var BRAND = 'agnt';") {
		t.Error("the badge must still name agnt")
	}
	// One constant, one path: a second disclosure string would mean the wording had
	// become conditional, which INV-14 forbids (it must read no input at all).
	if n := strings.Count(demoIndicatorSource(t), "var TEXT = "); n != 1 {
		t.Errorf("the disclosure must be exactly one unconditional constant, found %d", n)
	}
}

// TestDemoIndicatorStylesViaCSSOMOnly is the CSP assertion at module level. The
// proxied public path passes an EMPTY nonce, so it authorises no inline style at
// all; a <style> element or a style= attribute here would be refused by the
// browser, and "fixing" that by widening style-src is forbidden (INV-11/INV-12).
// Constructed-stylesheet rules are not subject to style-src, so this is the only
// shape that works without touching a single directive.
func TestDemoIndicatorStylesViaCSSOMOnly(t *testing.T) {
	js := demoIndicatorSource(t)

	for _, forbidden := range []string{
		"<style",              // inline style element
		"style=",              // style attribute in markup
		"setAttribute('style", // style attribute via DOM
		`setAttribute("style`,
		".style.",              // inline-style property writes (CSSStyleDeclaration on the element)
		".cssText",             // ditto, wholesale
		"createElement('style", // a style element built at runtime
		`createElement("style`,
	} {
		if strings.Contains(js, forbidden) {
			t.Errorf("demo-indicator must not use %q — the proxied public response authorises no inline style (empty nonce)", forbidden)
		}
	}
	for _, want := range []string{"new CSSStyleSheet()", "adoptedStyleSheets"} {
		if !strings.Contains(js, want) {
			t.Errorf("demo-indicator must style through the CSSOM; missing %q", want)
		}
	}
	// No external asset either: img-src/font-src are 'self' and there is no
	// subresource proxy route, so anything fetched by URL would simply not load.
	for _, sink := range []string{"http://", "https://", "url(", "@import", "fetch(", "XMLHttpRequest"} {
		if strings.Contains(js, sink) {
			t.Errorf("demo-indicator must load no external asset and make no request; found %q", sink)
		}
	}
}

// TestDemoIndicatorSurvivesBenignDOMReplacement pins the decision taken for the
// SPA case: the badge RE-ATTACHES rather than being a documented gap. A one-shot
// mount is removed for good by an ordinary `document.body.innerHTML = …` on route
// change, and INV-14 is not satisfied by rendering the disclosure until the first
// client-side navigation.
//
// This tier can only assert the SHAPE of the mechanism — there is no DOM in a Go
// test, and this repo's answer to "assert JS behaviour" is the chromee2e tier,
// whose file is outside this task's scope (the functional end-state assertion is
// handed to the P10 adversarial slice, which already drives a real browser against
// this badge). The pins below are therefore chosen to fail on every way the fix
// could be reverted or mis-shaped, not merely on the word "MutationObserver"
// existing: wrong observation target, wrong options, an unguarded re-mount that
// would loop, or a poll.
func TestDemoIndicatorSurvivesBenignDOMReplacement(t *testing.T) {
	js := demoIndicatorSource(t)

	if !strings.Contains(js, "new MutationObserver(reassert)") {
		t.Fatal("the badge must be re-asserted by a MutationObserver; a one-shot mount is removed for good by an SPA replacing body's contents")
	}
	// Target and options decide whether a body-contents replacement is even seen.
	// Observing body would miss a replacement OF body; without subtree a nested
	// container swap is missed.
	if !strings.Contains(js, "observer.observe(target, { childList: true, subtree: true })") {
		t.Error("the observer must watch childList with subtree, or a container swap goes unseen")
	}
	if !strings.Contains(js, "var target = document.documentElement;") {
		t.Error("the observer must be attached to documentElement, which survives a replacement of body itself")
	}
	// The re-mount must be guarded by the host's absence. Without this the
	// observer's own insertion re-triggers the callback and re-mounts forever.
	reassert := js[strings.Index(js, "function reassert()"):]
	reassert = reassert[:strings.Index(reassert, "function watch()")]
	if !strings.Contains(reassert, "if (document.getElementById(HOST_ID)) { return; }") {
		t.Error("reassert must no-op while the host is present; an unguarded re-mount loops on its own insertion")
	}
	if !strings.Contains(reassert, "mount();") {
		t.Error("reassert must re-mount when the host is absent")
	}
	// Bounded: a page removing the badge in a loop must not be able to hang the
	// tab through the microtask checkpoint.
	if !strings.Contains(js, "MAX_REASSERTS") || !strings.Contains(reassert, "observer.disconnect()") {
		t.Error("re-asserting must carry a finite budget that disconnects the observer, so a mutual removal loop cannot hang the page")
	}
	// No polling, no timers: the platform's own signal or nothing.
	for _, poll := range []string{"setInterval", "setTimeout", "requestAnimationFrame", "requestIdleCallback"} {
		if strings.Contains(js, poll) {
			t.Errorf("the badge must not re-assert by polling; found %q", poll)
		}
	}
	// The observer is installed once, and only alongside a mount.
	if n := strings.Count(js, "new MutationObserver"); n != 1 {
		t.Errorf("expected exactly one observer, found %d", n)
	}
	if !strings.Contains(js, "if (observer || typeof MutationObserver !== 'function') { return; }") {
		t.Error("watch must be idempotent and must degrade on a platform without MutationObserver")
	}
	// Re-attaching may not introduce a public handle that could stop it, and must
	// stay inside the closed shadow root (mount is the only code that builds the
	// tree, so the re-attach path inherits the closed root by construction).
	// The exported object literal only — LastIndex because the header comment
	// documents the same name, and scanning from the comment would sweep the whole
	// module body.
	surface := js[strings.LastIndex(js, "window.__agntDemoIndicator = {"):]
	for _, banned := range []string{"observer", "disconnect", "reassert", "watch:"} {
		if strings.Contains(surface, banned) {
			t.Errorf("the exported surface must not expose %q — that would be an off switch for the re-assert", banned)
		}
	}
}

// TestDemoIndicatorUnstyledFallbackStaysVisibleAndCSPFree pins the decision taken
// for the degraded path: when the platform has no constructable stylesheets the
// badge renders UNSTYLED-BUT-PRESENT, made as visible as CSS-free HTML allows, and
// still buys no CSP source. The reviewer's finding was that the degraded badge is
// "present but plausibly unseen"; these are the two zero-CSP mitigations, so a
// regression that reverts either one fails here.
func TestDemoIndicatorUnstyledFallbackStaysVisibleAndCSPFree(t *testing.T) {
	js := demoIndicatorSource(t)

	// Mitigation 1: the host lands at the TOP of the document flow. With the sheet
	// adopted the host is position:fixed and order is irrelevant; without it, this
	// is the difference between above the page's content and below all of it.
	if !strings.Contains(js, "insertBefore(host, parent.firstChild)") {
		t.Error("the host must be inserted as the first child so the unstyled fallback is not buried under page content")
	}
	if strings.Contains(js, "parent.appendChild(host)") {
		t.Error("appending the host puts the unstyled disclosure below the whole page")
	}
	// Mitigation 2: emphasis that survives with no stylesheet at all.
	if !strings.Contains(js, "createElement('strong')") {
		t.Error("the brand must be a <strong> so it renders bold with no CSS applied")
	}
	// Neither mitigation may buy a CSP source: no inline style (covered exactly by
	// TestDemoIndicatorStylesViaCSSOMOnly) and no fetched asset. What is asserted
	// here is the third temptation — falling back to a style element on the
	// degraded path specifically.
	if strings.Contains(js, "createElement('style") || strings.Contains(js, `createElement("style`) {
		t.Error("the fallback must not build a style element; the proxied path's empty nonce refuses it")
	}
	// The disclosure survives the degradation: text is set unconditionally, not
	// inside the styled branch.
	if !strings.Contains(js, "text.textContent = TEXT;") {
		t.Error("the disclosure text must be set unconditionally, independent of whether styling succeeded")
	}
	// The failure is reported rather than silent.
	if !strings.Contains(js, "console.warn(") {
		t.Error("an unstyled render must warn; a silently degraded disclosure is indistinguishable from a working one")
	}
	// The already-dead half of the branch is gone: the early-Chrome
	// insertRule-without-replaceSync constructable-stylesheet shape never shipped
	// in a reachable browser, and an untested code path is worse than none.
	if strings.Contains(js, "insertRule") {
		t.Error("the insertRule fallback is unreachable in every browser that has adoptedStyleSheets; do not carry it")
	}
	// One sheet, built once and reused, so re-mounting cannot allocate per mount.
	if n := strings.Count(js, "new CSSStyleSheet()"); n != 1 {
		t.Errorf("expected exactly one constructed stylesheet, found %d", n)
	}
}

// TestDemoIndicatorClosedShadowRoot pins the §9c containment shape: a CLOSED
// shadow root at the top z-layer, so page script holds no handle on the badge's
// internals and ordinary page CSS does not reach them.
func TestDemoIndicatorClosedShadowRoot(t *testing.T) {
	js := demoIndicatorSource(t)
	if !strings.Contains(js, "attachShadow({ mode: 'closed' })") {
		t.Error("the badge must be mounted in a CLOSED shadow root (§9c)")
	}
	if strings.Contains(js, "mode: 'open'") {
		t.Error("the shadow root must not be open — page script would get a handle on the disclosure")
	}
	if !strings.Contains(js, "z-index:") || !strings.Contains(js, "2147483647") {
		t.Error("the badge must sit at the top z-layer")
	}
	// The host geometry must be !important so an important page declaration
	// cannot win the cascade against it.
	if !strings.Contains(js, "position:fixed!important") {
		t.Error("the :host geometry must be !important; an important page rule would otherwise beat it")
	}
}

// TestDemoIndicatorHasNoDisableSurface is the INV-14 "always on" half: the module
// reads NO input that could switch it off, carries no close affordance, and
// exposes no removal function. The only conditional in it is the public-plane
// marker that decides whether it is on the public plane at all.
func TestDemoIndicatorHasNoDisableSurface(t *testing.T) {
	js := demoIndicatorSource(t)

	// No publisher/viewer-reachable input of any kind.
	for _, input := range []string{
		"location.search", "URLSearchParams", "location.hash",
		"localStorage", "sessionStorage", "document.cookie",
		"dataset", "getAttribute(",
	} {
		if strings.Contains(js, input) {
			t.Errorf("demo-indicator must read no input that could disable it; found %q", input)
		}
	}
	// No removal / dismissal surface.
	for _, off := range []string{
		"unmount", "dismiss", "removeChild", "remove()", ".hidden", "display:none",
		"addEventListener('click'", "addEventListener('keydown'",
	} {
		if strings.Contains(js, off) {
			t.Errorf("demo-indicator must have no dismissal surface; found %q", off)
		}
	}
	// The exported surface is mount + inert metadata. Anything callable that
	// could take the badge away must not exist.
	surface := js[strings.Index(js, "window.__agntDemoIndicator"):]
	for _, banned := range []string{"hide:", "remove:", "destroy:", "disable:", "setText:"} {
		if strings.Contains(surface, banned) {
			t.Errorf("exported surface must not include %q", banned)
		}
	}
	// The one gate is the RolePublic marker — not the share route, not a config.
	if !strings.Contains(js, "typeof window.__agnt_public_version === 'string'") {
		t.Error("the only gate must be the RolePublic version marker")
	}
}

// TestDemoIndicatorMountsIndependentlyOfBoot pins that the disclosure does not
// ride on any other public member: it must render even when the walkthrough
// fetch 404s, the viewer is missing, or the path is not a /s/{token} share.
func TestDemoIndicatorMountsIndependentlyOfBoot(t *testing.T) {
	js := demoIndicatorSource(t)
	for _, dep := range []string{"__walkthroughViewer", "__variantEngine", "__agntPublicBoot", "__feedbackClient", "/s/"} {
		if strings.Contains(js, dep) {
			t.Errorf("demo-indicator must not depend on %q — the disclosure cannot be contingent on another member booting", dep)
		}
	}
	// And its moduleOrder entry must declare no dependencies, so the closure walk
	// in role_public_test.go stays exactly the allowlist.
	for _, m := range moduleOrder {
		if m.name == "demo-indicator" && len(m.deps) != 0 {
			t.Errorf("demo-indicator declares dependencies %v; it must be dependency-free", m.deps)
		}
	}
}

// TestDemoIndicatorCarriesNoDevSurface runs the public forbidden-token scan
// against this module alone, so a regression names this file rather than only
// failing the whole-bundle scan.
func TestDemoIndicatorCarriesNoDevSurface(t *testing.T) {
	js := demoIndicatorSource(t)
	for _, tok := range forbiddenPublicTokens {
		if strings.Contains(js, tok) {
			t.Errorf("demo-indicator contains forbidden dev-surface token %q", tok)
		}
	}
}

// TestDemoIndicatorIsolatedIIFE asserts the module keeps the leading space after
// `function` so wrapModule leaves its IIFE intact rather than merging it into the
// shared bundle scope (same convention as the other public members).
func TestDemoIndicatorIsolatedIIFE(t *testing.T) {
	body := headerless(demoIndicatorSource(t))
	if !strings.HasPrefix(body, "(function () {") {
		t.Errorf("demo-indicator must open with an isolated IIFE `(function () {`, got %.24q", body)
	}
	if wrapped := wrapModule(demoIndicatorSource(t)); !strings.Contains(wrapped, "(function () {") {
		t.Error("wrapModule must leave the demo-indicator IIFE intact")
	}
}
