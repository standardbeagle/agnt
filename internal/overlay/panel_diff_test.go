package overlay

import (
	"bytes"
	"strings"
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

func TestRefreshPanelContent_NoCacheFallback(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)
	panel := PanelItem{}
	panel.SetContent("hello")
	ok := r.RefreshPanelContent(panel)
	assert.False(t, ok, "should return false when no cached state")
}

func TestRefreshPanelContent_DiffUpdatesOnlyChanged(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	// Simulate initial full render by populating the cache
	panel := PanelItem{}
	panel.SetContent("line1\nline2\nline3")
	r.mu.Lock()
	r.drawScrollableContent(5, 3, 70, 10, panel)
	r.mu.Unlock()
	buf.Reset()

	// Now refresh with one changed line
	panel.SetContent("line1\nchanged\nline3")
	ok := r.RefreshPanelContent(panel)
	require.True(t, ok)

	output := buf.String()
	// Should contain "changed" but not "line1" or "line3" (unchanged)
	assert.Contains(t, output, "changed")
	assert.NotContains(t, output, "line1")
	assert.NotContains(t, output, "line3")
}

func TestRefreshPanelContent_HandlesNewLines(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	panel := PanelItem{}
	panel.SetContent("line1\nline2")
	r.mu.Lock()
	r.drawScrollableContent(5, 3, 70, 10, panel)
	r.mu.Unlock()
	buf.Reset()

	// Add a new line
	panel.SetContent("line1\nline2\nline3")
	ok := r.RefreshPanelContent(panel)
	require.True(t, ok)

	output := buf.String()
	assert.Contains(t, output, "line3")
	// line1 and line2 are unchanged
	assert.NotContains(t, output, "line1")
	assert.NotContains(t, output, "line2")
}

func TestRefreshPanelContent_HandlesShrinkingContent(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	panel := PanelItem{}
	panel.SetContent("line1\nline2\nline3")
	r.mu.Lock()
	r.drawScrollableContent(5, 3, 70, 10, panel)
	r.mu.Unlock()
	buf.Reset()

	// Remove last line
	panel.SetContent("line1\nline2")
	ok := r.RefreshPanelContent(panel)
	require.True(t, ok)

	output := buf.String()
	// Row where line3 was should have ClearToEOL to erase it
	assert.True(t, strings.Contains(output, ClearToEOL))
}

func TestRefreshPanelContent_NoOpWhenUnchanged(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	panel := PanelItem{}
	panel.SetContent("line1\nline2")
	r.mu.Lock()
	r.drawScrollableContent(5, 3, 70, 10, panel)
	r.mu.Unlock()
	buf.Reset()

	// Same content
	ok := r.RefreshPanelContent(panel)
	require.True(t, ok)

	output := buf.String()
	// Should only contain cursor hide/show and possibly scroll indicators,
	// but not the actual content lines
	assert.NotContains(t, output, "line1")
	assert.NotContains(t, output, "line2")
}

func TestRefreshPanelContent_ScrolledPanelDiff(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	panel := PanelItem{ScrollOffset: 1}
	panel.SetContent("a\nb\nc\nd\ne")
	r.mu.Lock()
	r.drawScrollableContent(5, 3, 70, 3, panel)
	r.mu.Unlock()

	// Verify initial visible lines with scroll offset
	assert.Equal(t, []string{"b", "c", "d"}, r.lastPanelLines)
	buf.Reset()

	// Content grows at bottom, but we're scrolled so visible window stays same
	panel.SetContent("a\nb\nc\nd\ne\nf")
	ok := r.RefreshPanelContent(panel)
	require.True(t, ok)

	// With ScrollOffset=1, endLine=6-1=5, fromLine=5-3=2 -> lines[2:5]=c,d,e
	// Old was b,c,d -> all three lines changed
	output := buf.String()
	assert.Contains(t, output, "e")
}
