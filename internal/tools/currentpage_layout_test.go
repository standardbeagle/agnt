package tools

// Coverage for the currentpage action:"layout" parser — projecting the browser
// __devtool.diagnoseLayoutIssues() JSON onto the structured tool output.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLayoutDiagnostics_FullShape(t *testing.T) {
	raw := `{
	  "findings": [
	    {"check":"containing-block-trap","severity":"high","selector":"#modal","cause":"#xform","cause_property":"transform","detail":"d","fix":"f","avoid":"a"},
	    {"check":"ineffective-zindex","severity":"high","selector":"#z","cause":"","cause_property":"position:static","detail":"ignored","fix":"position:relative","avoid":"don't inflate"}
	  ],
	  "count": 2,
	  "scanned": 1200,
	  "capped": false,
	  "by_check": {"containing-block-trap":1,"ineffective-zindex":1,"click-interception":0,"clipped-descendant":0}
	}`

	out, err := parseLayoutDiagnostics(raw)
	require.NoError(t, err)
	assert.Equal(t, 2, out.Count)
	assert.Equal(t, 1200, out.Scanned)
	require.Len(t, out.Findings, 2)
	assert.Equal(t, "containing-block-trap", out.Findings[0].Check)
	assert.Equal(t, "#xform", out.Findings[0].Cause)
	assert.Equal(t, "transform", out.Findings[0].CauseProperty)
	assert.Equal(t, "position:relative", out.Findings[1].Fix)
	assert.Equal(t, 1, out.ByCheck["ineffective-zindex"])
	assert.Empty(t, out.Hint, "findings present → no empty hint")
}

func TestParseLayoutDiagnostics_EmptyGetsHint(t *testing.T) {
	out, err := parseLayoutDiagnostics(`{"findings":[],"count":0,"scanned":800,"capped":false}`)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Count)
	assert.NotEmpty(t, out.Hint, "clean page gets a reassuring hint")
}

func TestParseLayoutDiagnostics_CappedSurfaced(t *testing.T) {
	out, err := parseLayoutDiagnostics(`{"findings":[],"count":0,"scanned":4000,"capped":true}`)
	require.NoError(t, err)
	assert.True(t, out.Capped)
	assert.Contains(t, out.Hint, "4000", "capped scan is disclosed, not silent")
}

func TestParseLayoutDiagnostics_ModuleError(t *testing.T) {
	_, err := parseLayoutDiagnostics(`{"error":"layout module not loaded"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "layout module not loaded")
}

func TestParseLayoutDiagnostics_Garbage(t *testing.T) {
	_, err := parseLayoutDiagnostics(`not json`)
	require.Error(t, err)
}
