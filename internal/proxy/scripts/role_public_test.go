package scripts

import (
	"sort"
	"strings"
	"testing"
)

// rolePublicGoldenManifest is the EXACT, ordered set of modules RolePublic is
// allowed to contain. It is the tripwire: adding or removing a module from the
// public allowlist without updating this golden list fails TestRolePublicGoldenManifest.
// Keep it in module-load order (the order buildCombinedScript emits).
// public-boot was added deliberately: without it the other three members are
// inert on a served share (the artifact shell carries no inline script), so a
// visitor saw a blank page. It is the only public member that runs on its own,
// double-gated on the RolePublic version marker + the /s/{token} route.
// demo-indicator was added deliberately (spec §9c / INV-14): the disclosure badge
// is MANDATORY on every public artifact response, so it belongs in the allowlist
// rather than behind any switch. It is listed FIRST because the disclosure must
// not be contingent on any other member — it self-mounts on the RolePublic
// version marker alone and is dependency-free, so the closure walk below is still
// exactly the allowlist.
var rolePublicGoldenManifest = []string{
	"demo-indicator",
	"variant-engine",
	"walkthrough-viewer",
	"feedback-client",
	"public-boot",
}

// forbiddenPublicTokens are DEV-CONTROL-SURFACE signatures that MUST NOT appear
// anywhere in the assembled public bundle. This is the executable half of INV-1:
// the build fails if a dev-control module reaches the public plane.
//
// Scope, restated after INV-6's retirement (spec §6a, 2026-07-27): this gate
// bans the DEV CONTROL SURFACE, not authored content. The public plane now
// renders publisher-authored HTML/CSS/script (variant-engine's setHTML/addStyle/
// addScript), so markup and script-element sinks are a SANCTIONED public
// capability and must not be listed here — TestRolePublicPermitsAuthoredContent
// pins that they stay permitted. Containment of authored script is CSP's job:
// it runs iff its sha256 is in the served revision's pinned script-src
// (INV-11/INV-12). What remains banned is anything that would let the public
// plane *control the developer's session* or run code CSP never pinned.
//
// Each token is derived from a REAL dev-surface entrypoint, not guessed:
//
//   - "__devtool"      the entire dev control namespace: core WS transport
//     (window.__devtool_core, new WebSocket in core.js), the
//     exec channel result surface (window.__devtool_errors,
//     send('execution')), capture (window.__devtool_capture),
//     auth breakout (window.__devtool_auth*), audits
//     (window.__devtool_audit*), inspection
//     (window.__devtool_inspection), design
//     (window.__devtool_design), and the indicator
//     (window.__devtool_indicator*). Every dev module hangs its
//     public API off this prefix, so a blanket ban proves the
//     whole control API is absent.
//   - "new WebSocket"  the WebSocket command channel opened in core.js.
//   - "WebSocket("     any WebSocket construction (defensive superset).
//   - "eval("          the exec channel's dynamic-code sink (core.js: eval(code)).
//     Still banned AFTER INV-6's retirement, for a different
//     reason than before: the served CSP carries no
//     'unsafe-eval', so a compile path could only be an attempt
//     to run a body whose hash CSP never pinned.
//   - "new Function"   dynamic code compilation; same reasoning.
//   - "html2canvas"    the capture/screenshot library bundled for the dev roles.
var forbiddenPublicTokens = []string{
	"__devtool",
	"new WebSocket",
	"WebSocket(",
	"eval(",
	"new Function",
	"html2canvas",
}

// publicMembers walks moduleOrder + includeInRole exactly as buildCombinedScript
// does and returns the ordered module names that ship in RolePublic.
func publicMembers() []string {
	var out []string
	for _, m := range moduleOrder {
		if includeInRole(m.name, RolePublic) {
			out = append(out, m.name)
		}
	}
	return out
}

// TestRolePublicGoldenManifest pins the exact module set of RolePublic. It is a
// tripwire: any module added to or removed from the public allowlist (or added to
// moduleOrder such that includeInRole lets it into RolePublic) fails here until
// the golden manifest is deliberately updated — forcing a human to acknowledge a
// change to the hard public boundary.
func TestRolePublicGoldenManifest(t *testing.T) {
	got := publicMembers()
	if len(got) != len(rolePublicGoldenManifest) {
		t.Fatalf("RolePublic membership drift: got %v, want golden %v", got, rolePublicGoldenManifest)
	}
	for i, name := range rolePublicGoldenManifest {
		if got[i] != name {
			t.Fatalf("RolePublic member %d = %q, want %q (full got=%v)", i, got[i], name, got)
		}
	}
	// The allowlist map and the golden manifest must agree, so neither can drift
	// silently relative to the other.
	if len(rolePublicModules) != len(rolePublicGoldenManifest) {
		t.Fatalf("rolePublicModules (%d) and golden manifest (%d) disagree in size", len(rolePublicModules), len(rolePublicGoldenManifest))
	}
	for _, name := range rolePublicGoldenManifest {
		if !rolePublicModules[name] {
			t.Errorf("golden manifest module %q missing from rolePublicModules allowlist", name)
		}
	}
}

// TestRolePublicForbiddenSymbolScan asserts the ASSEMBLED public bundle contains
// none of the dev-control-surface tokens. This is the proof-of-negative gate: if
// any forbidden module (or the html2canvas header, or the __devtool_version
// marker) ever entered RolePublic, one of these tokens would appear and the test
// would fail naming the intruder token.
func TestRolePublicForbiddenSymbolScan(t *testing.T) {
	bundle := GetCombinedScriptForRole(RolePublic)
	for _, tok := range forbiddenPublicTokens {
		if strings.Contains(bundle, tok) {
			t.Errorf("public bundle contains forbidden dev-surface token %q — the public plane must be free of every dev-control surface", tok)
		}
	}
	// Sanity: the tokens are DISCRIMINATING — a real dev module does contain the
	// blanket namespace token, so were it to enter the allowlist the scan above
	// would fire. This guards against a scan that trivially passes because its
	// tokens never match anything.
	if !strings.Contains(moduleScript["audit-api"], "__devtool") {
		t.Fatal("expected the audit-api dev module to contain the __devtool namespace token; the forbidden-symbol scan would be vacuous otherwise")
	}
	if !strings.Contains(moduleScript["core"], "new WebSocket") {
		t.Fatal("expected core.js to contain 'new WebSocket'; the forbidden-symbol scan would be vacuous otherwise")
	}
	if !strings.Contains(moduleScript["core"], "eval(") {
		t.Fatal("expected core.js to contain the exec 'eval(' sink; the forbidden-symbol scan would be vacuous otherwise")
	}
}

// devControlModules are modules whose presence in the public bundle would mean
// the public plane can drive the developer's session. They are the concrete
// intruders TestRolePublicScanCatchesDevControlModule feeds the scan to prove it
// still has teeth: core (WS command channel + exec sink), indicator/design
// (agent control UI), capture (screenshotting the dev page), audit-api (the
// audit family).
var devControlModules = []string{"core", "indicator", "design", "capture", "audit-api"}

// TestRolePublicScanCatchesDevControlModule is the proof-of-negative for the
// symbol scan, at the level the gate actually protects: for EVERY dev-control
// module, appending its source to the public bundle must trip at least one
// forbidden token. Without this, a scan could silently rot into a list of
// tokens that no longer matches any real dev surface and pass vacuously.
func TestRolePublicScanCatchesDevControlModule(t *testing.T) {
	clean := GetCombinedScriptForRole(RolePublic)
	for _, name := range devControlModules {
		src, ok := moduleScript[name]
		if !ok {
			t.Fatalf("dev-control module %q not found in moduleScript — update devControlModules", name)
		}
		poisoned := clean + "\n// " + name + " module\n" + src
		var tripped []string
		for _, tok := range forbiddenPublicTokens {
			if strings.Contains(poisoned, tok) {
				tripped = append(tripped, tok)
			}
		}
		if len(tripped) == 0 {
			t.Errorf("forbidden-symbol scan did not trip on dev-control module %q — the gate would not fail the build if it entered RolePublic", name)
		}
	}
}

// TestRolePublicPermitsAuthoredContent pins the OTHER half of the rewritten
// gate: since INV-6's retirement the public plane's whole job includes rendering
// publisher-authored HTML/CSS/script (§6a), so the authored-content render path
// must be PRESENT in the public bundle and must not trip the dev-control scan.
// This fails if someone "hardens" the gate by re-banning markup/script sinks,
// which would silently delete the capability the epic exists to ship.
func TestRolePublicPermitsAuthoredContent(t *testing.T) {
	pub := GetCombinedScriptForRole(RolePublic)
	for _, want := range []string{
		"el.innerHTML = op.html", // setHTML
		"styleEl.textContent",    // addStyle
		"scriptEl.textContent",   // addScript — the hash-pinnable script element
		"__variant-engine-root",  // the variant root the two root-ops append to
	} {
		if !strings.Contains(pub, want) {
			t.Errorf("public bundle missing sanctioned authored-content render path %q (§6a)", want)
		}
	}
	// And the authored path must not itself be a dynamic-code path: the served
	// CSP has no 'unsafe-eval', so an authored body only ever runs via the
	// hash-pinned script element, never a compile call.
	for _, tok := range []string{"eval(", "new Function"} {
		if strings.Contains(variantEngineJS, tok) {
			t.Errorf("the authored-content path must not use %q — script executes only via CSP hash pinning (INV-12)", tok)
		}
	}
}

// closureIntruders walks the transitive HARD-dependency closure of the given
// allowlist over moduleOrder and returns any module a member requires that is NOT
// itself in the allowlist. A non-empty result means a member would drag a
// non-allowlisted (potentially forbidden) module into the public bundle.
func closureIntruders(allow map[string]bool) []string {
	deps := map[string][]string{}
	for _, m := range moduleOrder {
		deps[m.name] = m.deps
	}
	seen := map[string]bool{}
	intruders := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		for _, d := range deps[name] {
			if !allow[d] {
				intruders[d] = true
			}
			visit(d)
		}
	}
	for name := range allow {
		visit(name)
	}
	out := make([]string, 0, len(intruders))
	for name := range intruders {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestRolePublicDependencyClosure walks the public allowlist's transitive
// dependency closure and fails if any member pulls in a module the allowlist does
// not sanction. Because every public member is dependency-free, the closure is
// exactly the allowlist and there are no intruders.
func TestRolePublicDependencyClosure(t *testing.T) {
	if intruders := closureIntruders(rolePublicModules); len(intruders) != 0 {
		t.Errorf("RolePublic dependency closure escapes the allowlist: members transitively require non-allowlisted modules %v", intruders)
	}
}

// TestRolePublicClosureRejectsForbiddenMember PROVES THE NEGATIVE: were a
// forbidden dev module (audit-api) ever added to the public allowlist, the
// closure walk would catch it — its declared dependencies (utils, api-tracker)
// are not allowlisted, so they surface as intruders naming the escape. This is
// the test that would fail if RolePublic were widened to a forbidden module.
func TestRolePublicClosureRejectsForbiddenMember(t *testing.T) {
	poisoned := map[string]bool{}
	for k, v := range rolePublicModules {
		poisoned[k] = v
	}
	poisoned["audit-api"] = true // synthetic forbidden member

	intruders := closureIntruders(poisoned)
	if len(intruders) == 0 {
		t.Fatal("closure walk failed to reject a synthetic forbidden member (audit-api) — the negative gate is not working")
	}
	// audit-api's declared deps are utils + api-tracker; both must surface.
	joined := strings.Join(intruders, ",")
	for _, want := range []string{"api-tracker", "utils"} {
		if !strings.Contains(joined, want) {
			t.Errorf("closure walk on poisoned allowlist should name %q as an intruder; got %v", want, intruders)
		}
	}
}

// TestRolePublicShapeAndSize verifies the assembled public bundle carries exactly
// the allowlisted public member markers, omits the html2canvas capture library and the
// __devtool_version dev marker, and is strictly smaller than the full bundle.
func TestRolePublicShapeAndSize(t *testing.T) {
	pub := GetCombinedScriptForRole(RolePublic)
	full := GetCombinedScriptForRole(RoleFull)

	marker := func(name string) string { return "// " + name + " module\n" }
	for _, name := range rolePublicGoldenManifest {
		if !strings.Contains(pub, marker(name)) {
			t.Errorf("public bundle missing allowlisted member marker %q", name)
		}
	}
	// No dev module marker other than the three public members may appear.
	for _, m := range moduleOrder {
		if rolePublicModules[m.name] {
			continue
		}
		if strings.Contains(pub, marker(m.name)) {
			t.Errorf("public bundle unexpectedly contains non-allowlisted module marker %q", m.name)
		}
	}
	if strings.Contains(pub, "__devtool_version") {
		t.Error("public bundle must not carry the __devtool_version dev marker")
	}
	if !strings.Contains(pub, "__agnt_public_version") {
		t.Error("public bundle should carry the public-namespaced version marker")
	}
	if len(pub) >= len(full) {
		t.Errorf("public bundle (%d bytes) must be far smaller than full (%d bytes)", len(pub), len(full))
	}
}
