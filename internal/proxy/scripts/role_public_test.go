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
var rolePublicGoldenManifest = []string{
	"variant-engine",
	"walkthrough-viewer",
	"feedback-client",
}

// forbiddenPublicTokens are dev-control-surface signatures that MUST NOT appear
// anywhere in the assembled public bundle. Each is derived from a REAL dev-surface
// entrypoint, not guessed:
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
//   - "new Function"   dynamic code compilation.
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
// the three public member markers, omits the html2canvas capture library and the
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
