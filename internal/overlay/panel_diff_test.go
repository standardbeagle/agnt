package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVisibleLines_EmptyContent(t *testing.T) {
	panel := PanelItem{Content: ""}
	lines := visibleLines(panel, 10, 80)
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "no output")
}

func TestVisibleLines_FewerLinesThanAvail(t *testing.T) {
	panel := PanelItem{}
	panel.SetContent("line1\nline2\nline3")
	lines := visibleLines(panel, 10, 80)
	assert.Equal(t, []string{"line1", "line2", "line3"}, lines)
}

func TestVisibleLines_MoreLinesThanAvail(t *testing.T) {
	panel := PanelItem{}
	panel.SetContent("a\nb\nc\nd\ne")
	lines := visibleLines(panel, 3, 80)
	// Pinned to bottom (ScrollOffset=0), should show last 3 lines
	assert.Equal(t, []string{"c", "d", "e"}, lines)
}

func TestVisibleLines_ScrollOffset(t *testing.T) {
	panel := PanelItem{ScrollOffset: 2}
	panel.SetContent("a\nb\nc\nd\ne")
	lines := visibleLines(panel, 3, 80)
	// endLine = 5-2=3, fromLine = 3-3=0 -> lines[0:3] = a,b,c
	assert.Equal(t, []string{"a", "b", "c"}, lines)
}

func TestVisibleLines_Truncation(t *testing.T) {
	panel := PanelItem{}
	panel.SetContent("this is a very long line indeed")
	lines := visibleLines(panel, 10, 10)
	assert.Equal(t, []string{"this is a "}, lines)
}
