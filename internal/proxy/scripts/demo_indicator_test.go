package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
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

// demoIndicatorSpan returns the module source between two markers, so an
// assertion can be scoped to one function body instead of matching anywhere in
// the file. Both markers are required, so a rename is a loud failure.
func demoIndicatorSpan(t *testing.T, js, from, to string) string {
	t.Helper()
	i := strings.Index(js, from)
	if i < 0 {
		t.Fatalf("demo-indicator no longer contains %q", from)
	}
	rest := js[i:]
	j := strings.Index(rest, to)
	if j < 0 {
		t.Fatalf("demo-indicator no longer contains %q after %q", to, from)
	}
	return rest[:j]
}

// TestDemoIndicatorExhaustedBudgetStillCarriesTheBadge is the composition fix.
// The re-assert budget exists to bound a LOOP (a page whose own observer removes
// the host on every insertion would otherwise ping-pong with ours inside the
// microtask checkpoint and hang the tab), and it must keep doing that. What it
// must NOT do is end with the mandatory disclosure absent: an exhaustion path
// that disconnects without mounting makes the terminal state — the last state
// this module controls — carry ZERO disclosure, which for INV-14 ("every public
// artifact response renders the demo indicator") is the worst possible end
// state, and is reachable by an ordinary SPA rather than only by an attacker.
//
// So the budget branch mounts once and THEN stops watching. Asserted by order,
// not by presence: `mount()` already appeared later in reassert() before the fix,
// so only its position relative to disconnect() distinguishes fixed from broken.
func TestDemoIndicatorExhaustedBudgetStillCarriesTheBadge(t *testing.T) {
	js := demoIndicatorSource(t)
	reassert := demoIndicatorSpan(t, js, "function reassert()", "function watch()")

	// The bound itself must survive: this fix may not be implemented by deleting
	// the budget, which would restore the hang-the-tab defect it exists to stop.
	if !strings.Contains(js, "var MAX_REASSERTS = 100;") {
		t.Error("the finite re-assert budget must remain; an unbounded re-assert lets a hostile page hang the tab")
	}
	if !strings.Contains(reassert, "observer.disconnect()") {
		t.Fatal("the exhaustion path must still disconnect the observer")
	}

	// The exhaustion branch, scoped from the budget test to the disconnect that
	// closes it, must carry a mount. Scoping is what gives this teeth: the
	// trailing `mount()` of the normal re-assert path is outside this span.
	branch := demoIndicatorSpan(t, reassert, "if (reasserts >= MAX_REASSERTS) {", "observer.disconnect()")
	if !strings.Contains(branch, "mount();") {
		t.Error("on budget exhaustion the module must mount() once before disconnecting, so the terminal state still carries the mandatory disclosure (INV-14)")
	}
	// Belt and braces on the ordering, independent of the span above: the FIRST
	// mount in reassert must precede the disconnect.
	if iMount, iDisc := strings.Index(reassert, "mount();"), strings.Index(reassert, "observer.disconnect()"); iMount > iDisc {
		t.Error("reassert disconnects before it mounts; the exhausted state would carry no disclosure at all")
	}
	// Exactly two: the exhaustion mount and the ordinary re-assert mount. A third
	// would mean a path was duplicated rather than gated.
	if n := strings.Count(reassert, "mount();"); n != 2 {
		t.Errorf("expected reassert to mount on exactly two paths (exhaustion, ordinary), found %d", n)
	}
}

// TestDemoIndicatorFirstChildPlacementIsGatedOnStyling is the other half of the
// composition. Inserting the host as body's FIRST child buys visibility on the
// unstyled fallback path (with the sheet adopted the host is position:fixed, so
// document order is irrelevant there) — but applied unconditionally it changes
// the DOM for ~100% of traffic to benefit the rare no-adoptedStyleSheets case,
// and a framework is likelier to manipulate a first child than a last one. That
// widened the insert/remove surface that burns the re-assert budget, on the
// proxied SPA path which is the headline use case.
//
// So the placement is now gated on adoptStyles() having SUCCEEDED, which mount()
// already computes for its warning.
func TestDemoIndicatorFirstChildPlacementIsGatedOnStyling(t *testing.T) {
	js := demoIndicatorSource(t)
	mountBody := demoIndicatorSpan(t, js, "function mount()", "// RE-ASSERT AFTER BENIGN DOM REPLACEMENT")

	// adoptStyles' result is captured once and reused, not called twice.
	if !strings.Contains(mountBody, "var styled = adoptStyles(root);") {
		t.Fatal("mount must capture whether adoptStyles succeeded, so the placement can be gated on it")
	}
	if n := strings.Count(mountBody, "adoptStyles("); n != 1 {
		t.Errorf("adoptStyles must be called exactly once per mount, found %d calls", n)
	}

	// Whitespace-normalized so indentation is not load-bearing, but both branches
	// and their direction are: swapping them, or dropping either, fails here.
	flat := strings.Join(strings.Fields(mountBody), " ")
	if !strings.Contains(flat, "if (styled) { parent.appendChild(host); } else { parent.insertBefore(host, parent.firstChild); }") {
		t.Error("placement must be conditional: appendChild on the styled path (host is position:fixed, order irrelevant), insertBefore-firstChild only on the unstyled fallback path")
	}
	// Neither placement may be unconditional. An ungated call of either kind
	// outside that branch is the shape being fixed.
	if n := strings.Count(mountBody, "parent.insertBefore(host, parent.firstChild)"); n != 1 {
		t.Errorf("expected exactly one first-child insert, on the unstyled path only, found %d", n)
	}
	if n := strings.Count(mountBody, "parent.appendChild(host)"); n != 1 {
		t.Errorf("expected exactly one append, on the styled path only, found %d", n)
	}
}

// TestDemoIndicatorExhaustionAndPlacementUnderNode is the BEHAVIOURAL tier for
// the two fixes above: it runs the shipped module source against a minimal DOM
// stub under node, drives the re-assert budget past exhaustion, and reads back
// where the host actually landed on each styling path. The source assertions
// above pin the shape; this one proves the outcome — that the terminal state
// after exhaustion still contains the badge, and that the budget still stops the
// loop afterwards.
//
// It reuses this package's existing js-runtime gate (walkthrough_player_test.go)
// rather than introducing a second, differently-gated tier: OFF by default, so
// the default suite's greenness never depends on a node install.
//
//	AGNT_JS_RUNTIME_TESTS=1 go test ./internal/proxy/scripts/ -run TestDemoIndicatorExhaustionAndPlacement
//
// The stub is deliberately tiny and models only what the module touches. Its one
// non-obvious fidelity rule: a mutation notifies observers only when the mutated
// parent is CONNECTED to documentElement, because that is the subtree the module
// observes — without it, building the badge's detached element tree would fire
// the callback and the module would appear to recurse.
func TestDemoIndicatorExhaustionAndPlacementUnderNode(t *testing.T) {
	if os.Getenv("AGNT_JS_RUNTIME_TESTS") == "" {
		t.Skip("SKIPPING js-runtime tier: set AGNT_JS_RUNTIME_TESTS=1 to drive demo-indicator.js against a DOM stub under node (the always-on source guards in TestDemoIndicatorExhaustedBudgetStillCarriesTheBadge and TestDemoIndicatorFirstChildPlacementIsGatedOnStyling still ran)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("AGNT_JS_RUNTIME_TESTS is set but node is not on PATH: %v", err)
	}

	// One removal past the budget, plus one more to observe that the observer
	// really did stop: 100 ordinary re-asserts, the 101st is the exhaustion
	// mount, the 102nd is nobody's business but the page's.
	const removals = 102

	cmd := exec.Command(node, "-e", demoIndicatorDriver(t))
	cmd.Stdin = strings.NewReader(`{"removals":` + strconv.Itoa(removals) + `}`)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed: %v\n%s", err, raw)
	}
	var got struct {
		MaxReasserts int `json:"maxReasserts"`
		Styled       struct {
			HostIndex     int      `json:"hostIndex"`
			ChildCount    int      `json:"childCount"`
			PresentAfter  []bool   `json:"presentAfter"`
			Warnings      []string `json:"warnings"`
			AdoptedSheets int      `json:"adoptedSheets"`
		} `json:"styled"`
		Unstyled struct {
			HostIndex    int      `json:"hostIndex"`
			ChildCount   int      `json:"childCount"`
			PresentAfter []bool   `json:"presentAfter"`
			Warnings     []string `json:"warnings"`
		} `json:"unstyled"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode node output %q: %v", raw, err)
	}

	// The budget the driver observed must be the shipped one, else the presence
	// indices below would be asserting against the wrong boundary.
	if got.MaxReasserts != 100 {
		t.Fatalf("driver observed MAX_REASSERTS=%d, want 100", got.MaxReasserts)
	}

	// PLACEMENT, both directions. The stub pre-fills body with page content, so
	// first-child and last-child are distinguishable positions.
	if got.Styled.HostIndex != got.Styled.ChildCount-1 {
		t.Errorf("styled path: host is at index %d of %d children, want last — the first-child insert must not apply when the sheet was adopted (the host is position:fixed there, so it buys nothing and widens the mutation surface)", got.Styled.HostIndex, got.Styled.ChildCount)
	}
	if got.Unstyled.HostIndex != 0 {
		t.Errorf("unstyled path: host is at index %d, want 0 — with no stylesheet the first-child insert is the only thing keeping the disclosure above the page's content", got.Unstyled.HostIndex)
	}
	if got.Styled.AdoptedSheets != 1 {
		t.Errorf("styled path adopted %d stylesheets, want 1 — the scenario did not exercise the styled branch", got.Styled.AdoptedSheets)
	}
	if len(got.Unstyled.Warnings) == 0 {
		t.Error("the unstyled path must warn; a silently degraded disclosure is indistinguishable from a working one")
	}

	// EXHAUSTION, on both paths (the fix is in reassert, which is styling-blind,
	// but the whole point of this task is that the two interact).
	for _, sc := range []struct {
		name         string
		presentAfter []bool
	}{
		{"styled", got.Styled.PresentAfter},
		{"unstyled", got.Unstyled.PresentAfter},
	} {
		if len(sc.presentAfter) != removals {
			t.Fatalf("%s: driver reported %d removals, want %d", sc.name, len(sc.presentAfter), removals)
		}
		for i := 0; i < 100; i++ {
			if !sc.presentAfter[i] {
				t.Fatalf("%s: badge absent after removal %d, inside the re-assert budget", sc.name, i+1)
			}
		}
		// The assertion this task exists for: the removal that EXHAUSTS the budget
		// must still leave the badge mounted.
		if !sc.presentAfter[100] {
			t.Errorf("%s: the badge is ABSENT after the removal that exhausted the re-assert budget — the terminal state carries zero disclosure, which INV-14 forbids", sc.name)
		}
		// And the bound must still hold: past exhaustion the observer is gone, so
		// the page's own removal stands. A test that demanded presence here would
		// be demanding the unbounded loop back.
		if sc.presentAfter[101] {
			t.Errorf("%s: the observer is still re-mounting past the budget; the bound that stops a hostile ping-pong from hanging the tab is gone", sc.name)
		}
	}
	found := false
	for _, w := range got.Styled.Warnings {
		if strings.Contains(w, "budget exhausted") {
			found = true
		}
	}
	if !found {
		t.Errorf("exhaustion must be reported, not silent; warnings were %q", got.Styled.Warnings)
	}
}

// demoIndicatorDriver builds the node program: the SHIPPED module source (via
// moduleScript, so this cannot drift from what the bundle serves) plus the
// smallest DOM the module touches. Each scenario runs in its own function scope
// so the two styling paths cannot share module state.
func demoIndicatorDriver(t *testing.T) string {
	t.Helper()
	src, err := json.Marshal(demoIndicatorSource(t))
	if err != nil {
		t.Fatalf("marshal module source: %v", err)
	}
	return "var MODULE_SRC = " + string(src) + ";\n" + demoIndicatorDriverBody
}

// The driver body. Kept as one literal so the stub reads as the small DOM model
// it is. `eval` of MODULE_SRC is deliberate: the module is an IIFE reading
// document/window/MutationObserver/CSSStyleSheet as free variables, and a direct
// eval inside the scenario function is what binds those to the stub.
const demoIndicatorDriverBody = `
var HOST_ID = 'agnt-demo-indicator';

function Elem(tag) {
  this.tagName = tag;
  this.id = '';
  this.className = '';
  this.textContent = '';
  this.childNodes = [];
  this.parentNode = null;
  this.attrs = {};
  this.shadow = null;
}
// The module inserts relative to parent.firstChild, so the stub has to expose it:
// without this it reads undefined, insertBefore degrades to an append, and BOTH
// placement paths would look identical and pass.
Object.defineProperty(Elem.prototype, 'firstChild', {
  get: function () { return this.childNodes.length ? this.childNodes[0] : null; }
});

function runScenario(styled, removals) {
  var warnings = [];
  var observers = [];

  // A mutation is only observable when it happens inside the observed subtree,
  // which is documentElement's. Building the badge's detached tree is therefore
  // silent, exactly as in a browser.
  function connected(node) {
    while (node) {
      if (node === documentElement) { return true; }
      node = node.parentNode;
    }
    return false;
  }
  function notify(parent) {
    if (!connected(parent)) { return; }
    for (var i = 0; i < observers.length; i++) {
      if (observers[i].active) { observers[i].cb([]); }
    }
  }

  Elem.prototype.insertAt = function (i, n) {
    if (n.parentNode) { n.parentNode.detach(n); }
    this.childNodes.splice(i, 0, n);
    n.parentNode = this;
    notify(this);
  };
  Elem.prototype.appendChild = function (n) {
    this.insertAt(this.childNodes.length, n);
    return n;
  };
  Elem.prototype.insertBefore = function (n, ref) {
    var i = ref ? this.childNodes.indexOf(ref) : -1;
    this.insertAt(i < 0 ? this.childNodes.length : i, n);
    return n;
  };
  Elem.prototype.detach = function (n) {
    var i = this.childNodes.indexOf(n);
    if (i < 0) { return; }
    this.childNodes.splice(i, 1);
    n.parentNode = null;
    notify(this);
  };
  Elem.prototype.setAttribute = function (k, v) { this.attrs[k] = v; };
  Elem.prototype.attachShadow = function (opts) {
    var root = { mode: opts.mode, childNodes: [] };
    root.appendChild = function (n) { root.childNodes.push(n); return n; };
    // Present only where constructable stylesheets are, which is what the
    // module's capability probe reads.
    if (styled) { root.adoptedStyleSheets = []; }
    this.shadow = root;
    return root;
  };

  var documentElement = new Elem('html');
  var body = new Elem('body');
  documentElement.appendChild(body);
  // Page content, so first-child and last-child are distinct positions.
  body.appendChild(new Elem('main'));
  body.appendChild(new Elem('footer'));

  function find(id, node) {
    node = node || documentElement;
    if (node.id === id) { return node; }
    for (var i = 0; i < node.childNodes.length; i++) {
      var hit = find(id, node.childNodes[i]);
      if (hit) { return hit; }
    }
    return null;
  }

  var document = {
    body: body,
    documentElement: documentElement,
    readyState: 'complete',
    createElement: function (tag) { return new Elem(tag); },
    getElementById: function (id) { return find(id); },
    addEventListener: function () {}
  };
  var window = { __agnt_public_version: 'test-marker' };
  var console = { warn: function (m) { warnings.push(String(m)); }, error: function (m) { warnings.push(String(m)); } };
  function MutationObserver(cb) { this.cb = cb; this.active = false; }
  MutationObserver.prototype.observe = function () { this.active = true; observers.push(this); };
  MutationObserver.prototype.disconnect = function () { this.active = false; };
  var CSSStyleSheet = styled ? function () { this.cssText = ''; } : undefined;
  if (styled) {
    CSSStyleSheet.prototype.replaceSync = function (t) { this.cssText = t; };
  }

  eval(MODULE_SRC);

  var host = find(HOST_ID);
  if (!host) { throw new Error('module did not mount at all (styled=' + styled + ')'); }
  var out = {
    hostIndex: body.childNodes.indexOf(host),
    childCount: body.childNodes.length,
    adoptedSheets: host.shadow && host.shadow.adoptedStyleSheets ? host.shadow.adoptedStyleSheets.length : 0,
    presentAfter: []
  };
  // Each iteration is one benign removal of the host; the module's observer sees
  // it and decides whether to re-assert.
  for (var i = 0; i < removals; i++) {
    var h = find(HOST_ID);
    if (h && h.parentNode) { h.parentNode.detach(h); }
    out.presentAfter.push(!!find(HOST_ID));
  }
  out.warnings = warnings;
  return out;
}

// The shipped budget, read out of the source rather than restated, so a change to
// it fails the Go assertion instead of silently shifting the indices.
var m = /var MAX_REASSERTS = (\d+);/.exec(MODULE_SRC);
if (!m) { throw new Error('MAX_REASSERTS declaration not found in module source'); }

var cfg = JSON.parse(require('fs').readFileSync(0, 'utf8'));
process.stdout.write(JSON.stringify({
  maxReasserts: parseInt(m[1], 10),
  styled: runScenario(true, cfg.removals),
  unstyled: runScenario(false, cfg.removals)
}));
`

// TestDemoIndicatorUnstyledFallbackStaysVisibleAndCSPFree pins the decision taken
// for the degraded path: when the platform has no constructable stylesheets the
// badge renders UNSTYLED-BUT-PRESENT, made as visible as CSS-free HTML allows, and
// still buys no CSP source. The reviewer's finding was that the degraded badge is
// "present but plausibly unseen"; these are the two zero-CSP mitigations, so a
// regression that reverts either one fails here.
func TestDemoIndicatorUnstyledFallbackStaysVisibleAndCSPFree(t *testing.T) {
	js := demoIndicatorSource(t)

	// Mitigation 1: on THIS path the host lands at the TOP of the document flow,
	// which is the difference between above the page's content and below all of it.
	// It is gated on the styled path having failed, because with the sheet adopted
	// the host is position:fixed (order irrelevant) and the repositioning only
	// widens the mutation surface that burns the re-assert budget — see
	// TestDemoIndicatorFirstChildPlacementIsGatedOnStyling for both directions.
	if !strings.Contains(js, "insertBefore(host, parent.firstChild)") {
		t.Error("the host must be inserted as the first child so the unstyled fallback is not buried under page content")
	}
	if !strings.Contains(strings.Join(strings.Fields(js), " "), "} else { parent.insertBefore(host, parent.firstChild); }") {
		t.Error("the first-child insert must be the unstyled branch specifically; unconditional, it applies to ~100% of traffic to benefit the rare degraded path")
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
