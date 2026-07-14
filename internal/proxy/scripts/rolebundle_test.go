package scripts

import (
	"strings"
	"testing"
)

// TestRoleBundleComposition verifies the per-frame-role bundle split:
//   - the content bundle omits every chrome-only module (indicator UI,
//     walkthrough host) but carries the forwarding stubs,
//   - the chrome bundle omits every content-only module,
//   - the full (passive/legacy) bundle carries everything,
//
// checking both the emitted "// <name> module" markers and one load-bearing
// symbol per interesting module so a mis-mapped embed cannot slip through.
func TestRoleBundleComposition(t *testing.T) {
	full := GetCombinedScriptForRole(RoleFull)
	chrome := GetCombinedScriptForRole(RoleChrome)
	content := GetCombinedScriptForRole(RoleContent)

	marker := func(name string) string { return "// " + name + " module\n" }

	// Every module marker must be present in the full bundle.
	for _, m := range moduleOrder {
		if !strings.Contains(full, marker(m.name)) {
			t.Errorf("full bundle missing module %q", m.name)
		}
	}

	// Role-filtered bundles: markers included/excluded exactly per moduleRole.
	for _, m := range moduleOrder {
		wantChrome := moduleRole[m.name] != RoleContent
		if got := strings.Contains(chrome, marker(m.name)); got != wantChrome {
			t.Errorf("chrome bundle: module %q present=%v, want %v", m.name, got, wantChrome)
		}
		wantContent := moduleRole[m.name] != RoleChrome
		if got := strings.Contains(content, marker(m.name)); got != wantContent {
			t.Errorf("content bundle: module %q present=%v, want %v", m.name, got, wantContent)
		}
	}

	// Symbol-level spot checks: chrome-only symbols absent from content...
	chromeOnlySymbols := []string{
		"OverviewTabComponent",         // indicator-tabs
		"__devtool_indicator_internal", // indicator-* namespace
		"function installHost",         // walkthrough host impl (proxy only READS __devtool_walkthrough_host)
		"function createTabBar",        // indicator chrome shell
	}
	for _, sym := range chromeOnlySymbols {
		if strings.Contains(content, sym) {
			t.Errorf("content bundle must not contain chrome-only symbol %q", sym)
		}
		if !strings.Contains(chrome, sym) || !strings.Contains(full, sym) {
			t.Errorf("chrome and full bundles must contain %q", sym)
		}
	}

	// ...and content-only symbols absent from chrome.
	contentOnlySymbols := []string{
		"__devtool_spinners",                // mutation
		"window.__devtool_override_store =", // override-store export (design.js reads it lazily, so the bare name is shared)
		"window.__devtool_text_fragility =", // text-fragility export (api.js has a guarded runtime lookup)
	}
	for _, sym := range contentOnlySymbols {
		if strings.Contains(chrome, sym) {
			t.Errorf("chrome bundle must not contain content-only symbol %q", sym)
		}
		if !strings.Contains(content, sym) || !strings.Contains(full, sym) {
			t.Errorf("content and full bundles must contain %q", sym)
		}
	}

	// The forwarding stubs must be in every bundle (shared role set).
	for _, name := range []string{"indicator-bridge", "walkthrough-proxy"} {
		for flavor, bundle := range map[string]string{"full": full, "chrome": chrome, "content": content} {
			if !strings.Contains(bundle, marker(name)) {
				t.Errorf("%s bundle missing shared stub module %q", flavor, name)
			}
		}
	}

	// Distinct flavours must actually differ (the split is not a no-op) and
	// must be strictly smaller than the full bundle.
	if len(chrome) >= len(full) {
		t.Errorf("chrome bundle (%d bytes) must be smaller than full (%d bytes)", len(chrome), len(full))
	}
	if len(content) >= len(full) {
		t.Errorf("content bundle (%d bytes) must be smaller than full (%d bytes)", len(content), len(full))
	}
}

// softDeps lists dependency edges that are declared in moduleOrder for load
// ORDER but are not eval-time-hard: the depending module either captures the
// dep as a plain value, guards the lookup, or reaches it cross-frame at
// runtime. These edges are allowed to dangle in a role-filtered bundle.
var softDeps = map[string]map[string]bool{
	// api.js: plain-value captures (`mutations: mutations`), guarded lookups
	// (`wireframe ? … : fallback`, `window.__devtool_snapshot || {…}`,
	// per-call `window.__devtool_walkthrough` lookups), the guarded indicator
	// block (indicatorNotLoaded fallback), and core.js's pre-installed
	// __devtool_diagnostics fallback.
	"api": {
		"interaction": true, "mutation": true, "audit-api": true,
		"audit-loading": true, "diagnostics": true, "store": true,
		"content": true, "wireframe": true, "text-fragility": true,
		"responsive-risk": true, "snapshot-helper": true,
		"indicator": true, "walkthrough": true,
	},
	// indicator invokes sketch/design/style-editor in the CONTENT frame via
	// targetWindow(), never in its own window; the moduleOrder edges exist
	// only to keep full-bundle load order stable. (They are present in the
	// chrome bundle anyway because api.js eval-derefs them, but the edge
	// itself is soft.)
	// walkthrough's dep on walkthrough-proxy is ordering-only: the host never
	// calls the proxy.
	"walkthrough": {"walkthrough-proxy": true},
}

// TestRoleBundleDependencyClosure verifies that within each role-filtered
// bundle every included module's declared HARD dependencies are also
// included. Soft (order-only / guarded) edges are pinned in softDeps — adding
// a new dangling edge without classifying it there fails this test.
func TestRoleBundleDependencyClosure(t *testing.T) {
	for _, role := range []Role{RoleFull, RoleChrome, RoleContent, RolePublic} {
		included := map[string]bool{}
		for _, m := range moduleOrder {
			if includeInRole(m.name, role) {
				included[m.name] = true
			}
		}
		for _, m := range moduleOrder {
			if !included[m.name] {
				continue
			}
			for _, dep := range m.deps {
				if included[dep] || softDeps[m.name][dep] {
					continue
				}
				t.Errorf("role %q: module %q depends on %q which is excluded from the bundle and not classified as a soft dep", string(role), m.name, dep)
			}
		}
	}
}

// TestAPIIndicatorGuard pins the api.js not-loaded guard for the indicator
// block: in a bundle/frame without the indicator module (content bundle
// before indicator-bridge, or any future frame flavour), __devtool.indicator.*
// must return the sync {error} convention instead of the whole __devtool
// literal dying with a TypeError at eval.
func TestAPIIndicatorGuard(t *testing.T) {
	if !strings.Contains(apiJS, "indicator: indicator ? {") {
		t.Error("api.js indicator block must be guarded (`indicator: indicator ? {…} : {…fallback}`) so a bundle without the indicator module does not TypeError at eval")
	}
	if !strings.Contains(apiJS, "function indicatorNotLoaded()") {
		t.Error("api.js must define the indicatorNotLoaded fallback helper")
	}
	if !strings.Contains(apiJS, "indicator not loaded in this frame") {
		t.Error("api.js indicatorNotLoaded must return the documented error string 'indicator not loaded in this frame'")
	}
}

// TestIndicatorBridgeContract pins the split-brain contract between
// indicator-bridge.js (shared) and indicator.js (chrome-only):
//   - the bridge marks its presence and owns the hotkey registration,
//   - the full indicator skips its own registration when the bridge is
//     present (otherwise Ctrl/Cmd+Y would double-toggle),
//   - the bridge surface covers every method api.js's indicator block and
//     style-editor's content-side callers reach.
func TestIndicatorBridgeContract(t *testing.T) {
	if !strings.Contains(indicatorBridgeJS, "window.__devtool_indicator_bridge = true") {
		t.Error("indicator-bridge.js must set window.__devtool_indicator_bridge")
	}
	if !strings.Contains(indicatorJS, "if (!window.__devtool_indicator_bridge)") {
		t.Error("indicator.js must skip its own hotkey registration when indicator-bridge is present")
	}
	for _, method := range []string{"show", "hide", "toggle", "togglePanel", "addAttachment", "destroy"} {
		if !strings.Contains(indicatorBridgeJS, method+":") {
			t.Errorf("indicator-bridge.js surface missing %q — api.js/style-editor callers reach it in content frames", method)
		}
	}
	// The walkthrough proxy and host must gate on the same predicate so
	// exactly one of them installs per frame.
	const predicate = `(role === 'chrome') || (role !== 'passive' && isTop && role !== 'chrome' && role === 'content') || (!role && isTop)`
	if !strings.Contains(walkthroughProxyJS, predicate) {
		t.Error("walkthrough-proxy.js lost the shared isHost predicate")
	}
	if !strings.Contains(walkthroughJS, predicate) {
		t.Error("walkthrough.js lost the shared isHost predicate")
	}
	if strings.Contains(walkthroughJS, "installForwardingProxy") {
		t.Error("walkthrough.js must not carry the forwarding proxy — it moved to walkthrough-proxy.js")
	}
}
