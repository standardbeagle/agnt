package scripts

import (
	"strings"
	"testing"
)

// TestTransformScriptEmbedded verifies the transform + override-store modules
// are reachable from the combined bundle and expose the API surface the
// design-mode geometry-handle feature depends on.
func TestTransformScriptEmbedded(t *testing.T) {
	combined := buildCombinedScript(RoleFull)

	for _, marker := range []string{"// transform module", "// override-store module"} {
		if !strings.Contains(combined, marker) {
			t.Errorf("%s not found in combined script — embed.go moduleOrder entry missing", marker)
		}
	}

	// Override store public API (consumed by transform.js).
	storeAPI := []string{
		"window.__devtool_override_store",
		"ensureOID: ensureOID",
		"upsert: upsert",
		"read: read",
		"pop: pop",
		"clear: clear",
	}
	for _, want := range storeAPI {
		if !strings.Contains(combined, want) {
			t.Errorf("combined script missing override-store API export %q", want)
		}
	}

	// Transform public API, including the pure snap helpers exposed for testing.
	transformAPI := []string{
		"window.__devtool_transform",
		"select: select",
		"hide: hide",
		"snapValue: snapValue",
		"gridCandidates: gridCandidates",
	}
	for _, want := range transformAPI {
		if !strings.Contains(combined, want) {
			t.Errorf("combined script missing transform API export %q", want)
		}
	}

	// The geometry handles must emit through the design_edit channel and key
	// overrides off the data-devtool-oid attribute.
	for _, want := range []string{"'design_edit'", "data-devtool-oid", "__devtool_overrides"} {
		if !strings.Contains(combined, want) {
			t.Errorf("combined script missing transform contract token %q", want)
		}
	}
}

// TestTransformModuleOrder verifies transform loads after its declared deps
// (override-store, design, overlay, core, utils) and before api.
func TestTransformModuleOrder(t *testing.T) {
	combined := buildCombinedScript(RoleFull)

	idx := func(marker string) int { return strings.Index(combined, marker) }
	transformIdx := idx("// transform module")
	storeIdx := idx("// override-store module")
	designIdx := idx("// design module")
	overlayIdx := idx("// overlay module")
	apiIdx := idx("// api module")

	if transformIdx < 0 || storeIdx < 0 {
		t.Fatal("transform or override-store module marker not found")
	}
	if storeIdx >= transformIdx {
		t.Errorf("override-store must load before transform (store=%d, transform=%d)", storeIdx, transformIdx)
	}
	if designIdx < 0 || designIdx >= transformIdx {
		t.Errorf("design must load before transform (design=%d, transform=%d)", designIdx, transformIdx)
	}
	if overlayIdx < 0 || overlayIdx >= transformIdx {
		t.Errorf("overlay must load before transform (overlay=%d, transform=%d)", overlayIdx, transformIdx)
	}
	if apiIdx >= 0 && transformIdx >= apiIdx {
		t.Errorf("transform must load before api (transform=%d, api=%d)", transformIdx, apiIdx)
	}
}

// TestTransformInScriptNames verifies both new scripts show up in
// GetScriptNames so the debug surface lists them.
func TestTransformInScriptNames(t *testing.T) {
	names := GetScriptNames()
	want := map[string]bool{"transform.js": false, "override-store.js": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s not found in GetScriptNames()", name)
		}
	}
}
