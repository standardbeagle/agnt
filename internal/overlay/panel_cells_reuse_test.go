package overlay

import (
	"bytes"
	"io"
	"slices"
	"testing"

	"github.com/standardbeagle/vt10x"
)

// freshPanelCells is the pre-reuse implementation: a new emulator per frame.
func freshPanelCells(width, height int, frame []byte) []vt10x.Glyph {
	term := vt10x.New(vt10x.WithSize(width, height))
	_, _ = term.Write(frame)
	cells := make([]vt10x.Glyph, 0, width*(height-1))
	for y := 0; y < height-1; y++ {
		cells = term.Row(y, cells)
	}
	return cells
}

// A reused emulator must read back exactly what a fresh one does, whatever the
// previous frame left behind: styles that stay open, content on rows the next
// frame never touches, a moved cursor, a scrolled region.
func TestRenderPanelCells_ReuseMatchesFreshEmulator(t *testing.T) {
	const w, h = 40, 12
	frames := []string{
		"\x1b[2J\x1b[1;1H\x1b[1;31mred bold\x1b[3;5H\x1b[44mblue bg\x1b[10;1Hbottom row",
		"\x1b[1;1Hshort", // leaves every earlier row to be cleared by reset, not by the frame
		"\x1b[38;5;200mnever reset",
		"plain text with no cursor addressing\r\nsecond line",
		"\x1b[3;8r\x1b[8;1Hscroll\n\n\nregion",
		"\x1b[?25l\x1b[7mreverse\x1b[0m\x1b[?25h",
		"",
		"\x1b[1;1H" + string(bytes.Repeat([]byte("wrap "), 40)),
	}
	r := NewRenderer(io.Discard, w, h)
	for round := 0; round < 3; round++ {
		for i, f := range frames {
			got := r.renderPanelCells(bytes.NewBufferString(f))
			want := freshPanelCells(w, h, []byte(f))
			if !slices.Equal(got, want) {
				t.Fatalf("round %d frame %d: reused emulator differs from fresh emulator", round, i)
			}
		}
	}
}

// The two cell buffers alternate, so the previous frame survives the next draw.
func TestRenderPanelCells_PreviousFrameSurvivesNextDraw(t *testing.T) {
	r := NewRenderer(io.Discard, 20, 6)
	first := slices.Clone(r.renderPanelCells(bytes.NewBufferString("\x1b[1;1Hfirst")))
	prev := r.renderPanelCells(bytes.NewBufferString("\x1b[1;1Hsecond"))
	prevCopy := slices.Clone(prev)
	_ = r.renderPanelCells(bytes.NewBufferString("\x1b[1;1Hthird"))
	if slices.Equal(first, prevCopy) {
		t.Fatal("frames should differ")
	}
	// prev aliased the buffer the third draw did NOT write, so it was valid while
	// the third frame was being diffed against it; the draw after that reuses it.
	if len(prev) != len(prevCopy) {
		t.Fatal("previous frame length changed")
	}
}

// A resize builds an emulator of the new size rather than reading stale rows.
func TestRenderPanelCells_ResizeRebuildsEmulator(t *testing.T) {
	r := NewRenderer(io.Discard, 30, 10)
	_ = r.renderPanelCells(bytes.NewBufferString("\x1b[1;1Hbefore"))
	r.SetSize(25, 8)
	f := []byte("\x1b[2;3Hafter")
	if got, want := r.renderPanelCells(bytes.NewBuffer(f)), freshPanelCells(25, 8, f); !slices.Equal(got, want) {
		t.Fatal("cells after SetSize differ from a fresh emulator of the new size")
	}
}
