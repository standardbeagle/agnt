package tools

// Coverage for the front-end framework diagnostic classifier: recognizing
// signature React/Next/Vue/Svelte/Solid runtime messages and attaching the
// correct remediation direction plus the common wrong-fix to avoid. These are
// the runtime-only signals an agent is blind to in source (stale closures,
// hydration divergence, key identity) — the classifier makes them legible.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func errMsg(msg string) map[string]interface{} {
	return map[string]interface{}{"message": msg, "timestamp": ts("01")}
}

func TestClassifyDiagnostics_ReactInfiniteLoop(t *testing.T) {
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Console Error: Warning: Maximum update depth exceeded. This can happen when a component calls setState inside useEffect, but useEffect either doesn't have a dependency array, or one of the dependencies changes on every render."),
	})
	require.Len(t, d, 1)
	assert.Equal(t, "infinite-render-loop", d[0].Category)
	assert.Equal(t, "react", d[0].Framework)
	assert.NotEmpty(t, d[0].Fix)
	// The report's documented LLM failure mode for this bug.
	assert.Contains(t, d[0].Avoid, "dependency array")
}

func TestClassifyDiagnostics_HydrationMismatch(t *testing.T) {
	for _, msg := range []string{
		"Console Error: Warning: Text content did not match. Server: \"5\" Client: \"7\"",
		"Console Error: Hydration failed because the initial UI does not match what was rendered on the server.",
		"Console Error: Warning: An error occurred during hydration. The server HTML was replaced with client content.",
	} {
		d := classifyDiagnostics([]map[string]interface{}{errMsg(msg)})
		require.Len(t, d, 1, msg)
		assert.Equal(t, "hydration-mismatch", d[0].Category, msg)
		assert.Contains(t, d[0].Avoid, "suppressHydrationWarning")
	}
}

func TestClassifyDiagnostics_MissingKey(t *testing.T) {
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Console Error: Warning: Each child in a list should have a unique \"key\" prop."),
	})
	require.Len(t, d, 1)
	assert.Equal(t, "missing-key", d[0].Category)
	assert.Contains(t, d[0].Avoid, "index")
}

func TestClassifyDiagnostics_ControlledUncontrolled(t *testing.T) {
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Console Error: Warning: A component is changing an uncontrolled input to be controlled. This is likely caused by the value changing from undefined to a defined value."),
	})
	require.Len(t, d, 1)
	assert.Equal(t, "controlled-uncontrolled", d[0].Category)
}

func TestClassifyDiagnostics_HooksOrder(t *testing.T) {
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Console Error: Warning: React has detected a change in the order of Hooks called by Form. This will lead to bugs and errors if not fixed."),
	})
	require.Len(t, d, 1)
	assert.Equal(t, "hooks-order", d[0].Category)
}

func TestClassifyDiagnostics_VueReactivityWarning(t *testing.T) {
	// Vue emits via console.warn — only reachable once the browser forwards
	// signature-matched warnings to the server (Part A).
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Console Warning: [Vue warn]: Set operation on key \"count\" failed: target is readonly."),
	})
	require.Len(t, d, 1)
	assert.Equal(t, "vue-reactivity", d[0].Category)
	assert.Equal(t, "vue", d[0].Framework)
}

func TestClassifyDiagnostics_VuePropMutation(t *testing.T) {
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Console Warning: [Vue warn]: Avoid mutating a prop directly since the value will be overwritten whenever the parent component re-renders."),
	})
	require.Len(t, d, 1)
	assert.Equal(t, "vue-prop-mutation", d[0].Category)
}

func TestClassifyDiagnostics_GroupsAndCounts(t *testing.T) {
	// Same category from repeated occurrences collapses to one entry with a count.
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Console Error: Warning: Each child in a list should have a unique \"key\" prop."),
		errMsg("Console Error: Warning: Each child in a list should have a unique \"key\" prop."),
		errMsg("Console Error: Warning: Maximum update depth exceeded."),
	})
	require.Len(t, d, 2, "two distinct categories")
	// Highest-count category first.
	assert.Equal(t, "missing-key", d[0].Category)
	assert.Equal(t, 2, d[0].Count)
	assert.Equal(t, "infinite-render-loop", d[1].Category)
	assert.Equal(t, 1, d[1].Count)
}

func TestClassifyDiagnostics_IgnoresUnknownAndPlainErrors(t *testing.T) {
	d := classifyDiagnostics([]map[string]interface{}{
		errMsg("Uncaught TypeError: foo is not a function"),
		errMsg("Console Error: some app-specific message with no framework signature"),
	})
	assert.Empty(t, d, "only signature framework diagnostics are classified")
}

func TestClassifyDiagnostics_MatchesErrorField(t *testing.T) {
	// React's raw text sometimes lands in the `error` field, not `message`.
	d := classifyDiagnostics([]map[string]interface{}{
		{"error": "Warning: Each child in a list should have a unique \"key\" prop.", "timestamp": ts("01")},
	})
	require.Len(t, d, 1)
	assert.Equal(t, "missing-key", d[0].Category)
}

func TestConvertToPageTriage_SurfacesDiagnosticsAndKeepsOverview(t *testing.T) {
	// Diagnostics are ADDITIVE: the full page overview must remain intact.
	m := triageWireSession()
	m["errors"] = append(m["errors"].([]interface{}),
		map[string]interface{}{"message": "Console Error: Warning: Each child in a list should have a unique \"key\" prop.", "type": "Warning", "timestamp": ts("04")},
	)
	tr := convertToPageTriage(m)

	// New diagnostics section present.
	require.Len(t, tr.FrameworkDiagnostics, 1)
	assert.Equal(t, "missing-key", tr.FrameworkDiagnostics[0].Category)

	// Overview still fully populated — nothing replaced.
	assert.Equal(t, "/checkout", tr.URL)
	assert.Equal(t, "Checkout", tr.PageTitle)
	assert.Equal(t, int64(2200), tr.LoadTimeMs)
	require.NotNil(t, tr.Performance)
	require.NotEmpty(t, tr.LastActions)
	require.NotNil(t, tr.AfterLastAction)
	require.NotEmpty(t, tr.FailedResources)
}
