package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func profileTestFindings() []AuditFinding {
	return []AuditFinding{
		{ID: "f111aaaa", Type: "overflow", Severity: "critical", Selector: ".main", Message: "forces horizontal scroll +25px", Next: "proxy exec __devtool.getContainer('.main')"},
		{ID: "f222bbbb", Type: "layout", Severity: "critical", Selector: ".header", Message: "collapsed content, element has text but zero height", Next: "__devtool.inspect('.header')"},
		{ID: "f333cccc", Type: "layout", Severity: "warning", Selector: ".nav", Message: "fixed element covers 30% of viewport", Next: "__devtool.getStacking('.nav')"},
		{ID: "f444dddd", Type: "overflow", Severity: "info", Selector: ".sidebar", Message: "truncated text without title/tooltip", Next: "__devtool.getBox('.sidebar')"},
		{ID: "f555eeee", Type: "overflow", Severity: "warning", Selector: ".content", Message: "content clipped by overflow:hidden", Next: "__devtool.getBox('.content')"},
		{ID: "f666ffff", Type: "overflow", Severity: "warning", Selector: "img.hero", Message: "image squeezed or hidden by container", Next: "__devtool.inspect('img.hero')"},
		{ID: "f7770000", Type: "a11y", Severity: "info", Selector: "button.submit", Message: "touch target smaller than 44x44px minimum", Next: "__devtool.inspect('button.submit')"},
	}
}

func TestProfileProjectionBugTopFive(t *testing.T) {
	projected := ProjectFindingsByProfile(profileTestFindings(), AuditProfileBug)
	require.Len(t, projected, 5, "bug profile returns at most 5 findings")

	// Top 5 by severity, stable: 2 criticals, then the 3 warnings in input order.
	wantIDs := []string{"f111aaaa", "f222bbbb", "f333cccc", "f555eeee", "f666ffff"}
	for i, id := range wantIDs {
		assert.Equal(t, id, projected[i].ID, "position %d", i)
	}

	for _, f := range projected {
		assert.NotEqual(t, "info", f.Severity, "bug profile drops info when higher-severity findings fill the budget")
	}
}

func TestProfileProjectionBugFewerThanFive(t *testing.T) {
	projected := ProjectFindingsByProfile(profileTestFindings()[:3], AuditProfileBug)
	require.Len(t, projected, 3, "bug profile keeps everything when fewer than 5 findings exist")
}

func TestProfileProjectionFullUnchanged(t *testing.T) {
	input := profileTestFindings()
	for _, profile := range []string{"", AuditProfileFull, AuditProfileRelease} {
		projected := ProjectFindingsByProfile(input, profile)
		require.Len(t, projected, len(input), "profile %q must be an identity projection", profile)
		for i := range input {
			assert.Equal(t, input[i], projected[i], "profile %q reordered finding %d", profile, i)
		}
	}
}

func TestAuditSeverityRank(t *testing.T) {
	assert.Less(t, SeverityRank("critical"), SeverityRank("warning"))
	assert.Less(t, SeverityRank("warning"), SeverityRank("info"))
	assert.Greater(t, SeverityRank("unknown-severity"), SeverityRank("info"))
}
