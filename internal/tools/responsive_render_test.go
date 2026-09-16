package tools

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/responsive_audit_fixture.json
var responsiveAuditFixture []byte

// responsiveCompactFullGolden is the exact compact text the full profile must
// produce for the fixture: every pre-existing line format preserved, with one
// "id:" and one "next:" line per finding appended under the finding line.
const responsiveCompactFullGolden = `=== Responsive Audit: 3 viewports ===

MOBILE (375px) - 4 issues
  ! [overflow] .main - forces horizontal scroll +25px #f111aaaa
    id: f111aaaa
    next: proxy exec __devtool.getContainer('.main')
  ! [layout] .header - collapsed content, element has text but zero height #f222bbbb
    id: f222bbbb
    next: __devtool.inspect('.header')
  ! [layout] .nav - fixed element covers 30% of viewport #f333cccc
    id: f333cccc
    next: __devtool.getStacking('.nav')
  o [overflow] .sidebar - truncated text without title/tooltip #f444dddd
    id: f444dddd
    next: __devtool.getBox('.sidebar')

TABLET (768px) - 2 issues
  ! [overflow] .content - content clipped by overflow:hidden #f555eeee
    id: f555eeee
    next: __devtool.getBox('.content')
  ! [overflow] img.hero - image squeezed or hidden by container #f666ffff
    id: f666ffff
    next: __devtool.inspect('img.hero')

DESKTOP (1440px) - 1 issues
  o [a11y] button.submit - touch target smaller than 44x44px minimum #f7770000
    id: f7770000
    next: __devtool.inspect('button.submit')

SUMMARY: 7 issues (2 critical, 5 minor)
PATTERNS: 4 mobile-only, 2 tablet-only, 0 cross-viewport`

func findingLineIndices(t *testing.T, text string) []int {
	t.Helper()
	lines := strings.Split(text, "\n")
	var idx []int
	for i, line := range lines {
		if strings.HasPrefix(line, "  ! [") || strings.HasPrefix(line, "  o [") {
			idx = append(idx, i)
		}
	}
	return idx
}

func TestResponsiveCompactEveryFindingHasIDAndNext(t *testing.T) {
	for _, profile := range []string{"", AuditProfileFull, AuditProfileRelease, AuditProfileBug} {
		t.Run("profile="+profile, func(t *testing.T) {
			text, err := renderResponsiveCompact(responsiveAuditFixture, profile)
			require.NoError(t, err)
			lines := strings.Split(text, "\n")
			idx := findingLineIndices(t, text)
			require.NotEmpty(t, idx)
			for _, i := range idx {
				require.Greater(t, i+2, 1)
				idLine := lines[i+1]
				nextLine := lines[i+2]
				assert.True(t, strings.HasPrefix(idLine, "    id: "), "line after finding must be id: got %q", idLine)
				idVal := strings.TrimSpace(strings.TrimPrefix(idLine, "    id: "))
				assert.Regexp(t, "^[0-9a-f]{8}$", idVal)
				assert.True(t, strings.HasPrefix(nextLine, "    next: "), "line after id must be next: got %q", nextLine)
				nextVal := strings.TrimSpace(strings.TrimPrefix(nextLine, "    next: "))
				assert.NotContains(t, nextVal, "<selector>", "next must carry a concrete selector, not a placeholder")
				assert.Contains(t, nextVal, "'", "next must carry a concrete quoted selector")
			}
		})
	}
}

func TestResponsiveNextActionUsesHelperNotRawJS(t *testing.T) {
	text, err := renderResponsiveCompact(responsiveAuditFixture, AuditProfileFull)
	require.NoError(t, err)
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "    next: ") {
			continue
		}
		count++
		next := strings.TrimSpace(strings.TrimPrefix(line, "    next: "))
		code := strings.TrimPrefix(next, "proxy exec ")
		assert.True(t, strings.HasPrefix(code, "__devtool."), "code-bearing next must begin with __devtool.: %q", next)
		assert.NotContains(t, next, "document.")
		assert.NotContains(t, next, "window.")
		assert.NotContains(t, next, "querySelector")
	}
	assert.Equal(t, 7, count)
}

func TestResponsiveCompactFullUnchanged(t *testing.T) {
	text, err := renderResponsiveCompact(responsiveAuditFixture, AuditProfileFull)
	require.NoError(t, err)
	assert.Equal(t, responsiveCompactFullGolden, text)
}

func TestResponsiveCompactBugProfileTopFive(t *testing.T) {
	text, err := renderResponsiveCompact(responsiveAuditFixture, AuditProfileBug)
	require.NoError(t, err)
	idx := findingLineIndices(t, text)
	assert.Len(t, idx, 5, "bug profile renders at most 5 findings")
	assert.Contains(t, text, "SUMMARY: 5 issues (2 critical, 3 minor)")
	// info-severity findings are the first dropped
	assert.NotContains(t, text, "f444dddd")
	assert.NotContains(t, text, "f7770000")
}

func TestRenderResponsiveCompactRejectsInvalidJSON(t *testing.T) {
	_, err := renderResponsiveCompact([]byte("{not json"), AuditProfileFull)
	require.Error(t, err)
}

// TestResponsiveCompactSingleViewportPatternsZero pins the calculatePatterns()
// early return in responsive.js: with fewer than two viewports no pattern is
// computable, so the PATTERNS line renders all zeros.
func TestResponsiveCompactSingleViewportPatternsZero(t *testing.T) {
	single := []byte(`{"viewports":{"mobile":{"width":375,"issues":[
		{"type":"overflow","severity":"critical","selector":".main","message":"forces horizontal scroll +25px","id":"f111aaaa","next":"proxy exec __devtool.getContainer('.main')"},
		{"type":"layout","severity":"warning","selector":".nav","message":"fixed element covers 30% of viewport","id":"f333cccc","next":"__devtool.getStacking('.nav')"}
	]}},"summary":{"total":2,"critical":1,"minor":1,"complete":true}}`)
	text, err := renderResponsiveCompact(single, AuditProfileFull)
	require.NoError(t, err)
	assert.Contains(t, text, "=== Responsive Audit: 1 viewports ===")
	assert.Contains(t, text, "PATTERNS: 0 mobile-only, 0 tablet-only, 0 cross-viewport")
}
