package overlay

import (
	"bytes"
	"strings"
	"testing"

	"github.com/standardbeagle/vt10x"
)

// The overlay restores the child's cursor after every status-bar, toast, or
// splash draw with an absolute CUP taken from the ProtectedWriter's cursor
// tracker (cursorpark.go). Interactive agents such as Claude Code redraw
// their input box with *relative* moves — "go up N rows, erase, repaint" —
// so any error in the tracker becomes a mis-parked cursor, and the child's
// next frame erases and repaints the wrong rows: rules drawn over earlier
// text, duplicated blocks, stale placeholder lines.
//
// These tests hold the tracker to a real terminal model (vt10x) fed the same
// bytes the host terminal receives.

// trackerDivergence feeds each chunk through a ProtectedWriter whose output
// drives a vt10x host terminal, and reports the first chunk after which the
// tracked cursor disagrees with the terminal's.
func trackerDivergence(t *testing.T, w, h int, chunks []string) {
	t.Helper()
	host := vt10x.New(vt10x.WithSize(w, h))
	pw := NewProtectedWriter(host, w, h, FilterConfig{ProtectBottomRows: 1})
	pw.EnforceScrollRegion() // as the run pipeline does right after start
	for i, c := range chunks {
		if _, err := pw.Write([]byte(c)); err != nil {
			t.Fatalf("filter write: %v", err)
		}
		cur := host.Cursor()
		row, col, _ := pw.ChildCursor()
		if row != cur.Y+1 || col != cur.X+1 {
			t.Fatalf("after chunk %d %q: tracker at (%d,%d), terminal at (%d,%d)",
				i, c, row, col, cur.Y+1, cur.X+1)
		}
	}
}

func TestCursorTrackerMatchesTerminal(t *testing.T) {
	const w, h = 40, 12
	rule := strings.Repeat("─", w)
	cases := map[string][]string{
		// A full-width line leaves the cursor on the last column with a
		// pending wrap; the following CRLF moves down exactly one row.
		"full-width rule then CRLF":   {"\x1b[3;1H", rule, "\r\n", "x"},
		"full-width ascii then CR":    {"\x1b[3;1H", strings.Repeat("a", w), "\r", "b"},
		"full-width then CUU":         {"\x1b[5;1H", rule, "\x1b[2A"},
		"ink erase lines":             {"\x1b[6;1Habc\r\n", "def", "\x1b[2K\x1b[1A\x1b[2K\x1b[1A\x1b[2K\x1b[G", "xyz"},
		"CHA":                         {"\x1b[4;10Habc", "\x1b[3G", "q"},
		"CUF CUB":                     {"\x1b[4;10H", "\x1b[5C", "\x1b[3D"},
		"CNL CPL":                     {"\x1b[4;10H", "\x1b[2E", "\x1b[F"},
		"DECSC DECRC":                 {"\x1b[4;10H\x1b7", "\x1b[8;2H", "\x1b8"},
		"tab":                         {"\x1b[2;1Hab\t", "c"},
		"CUF clamps at right edge":    {"\x1b[2;1H", "\x1b[99C"},
		"CUP clamps":                  {"\x1b[99;99H"},
		"VPA":                         {"\x1b[2;7H", "\x1b[5d"},
		"LF at region bottom":         {"\x1b[11;1H", "\n\n", "z"},
		"DECSTBM homes the cursor":    {"\x1b[7;7H", "\x1b[r", "x"},
		"restore of empty slot":       {"\x1b[7;7H", "\x1b8"},
		"pending wrap cleared by CUU": {"\x1b[5;1H", strings.Repeat("a", w), "\x1b[1A", "b"},
		"sync update frame": {
			"\x1b[?2026h\x1b[8;1H" + rule + "\r\n❯ \r\n" + rule, "\x1b[?2026l",
			"\x1b[2A\x1b[G", "❯ hi",
		},
	}
	for name, chunks := range cases {
		t.Run(name, func(t *testing.T) { trackerDivergence(t, w, h, chunks) })
	}
}

// vt10x has no notion of wide glyphs, treats every CSI ... u as a cursor
// restore, and lets CUD run past the bottom margin (xterm's CursorDown stops
// at it when starting inside the region, per DEC), so these xterm behaviours
// are asserted directly.
func TestCursorTrackerXtermEdges(t *testing.T) {
	const w, h = 40, 12
	cases := []struct {
		name     string
		in       string
		row, col int
	}{
		{"CUD stops at the bottom margin", "\x1b[2;1H\x1b[50B", 11, 1},
		{"CUU stops at the top margin", "\x1b[3;11r\x1b[5;1H\x1b[50A", 3, 1},
		{"wide glyph in the last column wraps first", "\x1b[2;40H界", 3, 3},
		{"wide glyph filling the last two columns leaves a pending wrap", "\x1b[2;39H界", 2, 40},
		{"pending wrap resolves on the next glyph", "\x1b[2;39H界b", 3, 2},
		{"variation selector takes no cell", "\x1b[2;1H⚠️x", 2, 3},
		{"kitty keyboard push/pop/query never restore", "\x1b[4;10H\x1b[s\x1b[8;2H\x1b[>1u\x1b[<u\x1b[?u", 8, 2},
		{"XTSAVE is not SCOSC", "\x1b[4;10H\x1b[?1s\x1b[8;2H\x1b[u", 1, 1},
		{"1049 keeps its own save slot", "\x1b[4;10H\x1b7\x1b[5;5H\x1b[?1049h\x1b[2;2H\x1b[?1049l", 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pw := NewProtectedWriter(&bytes.Buffer{}, w, h, FilterConfig{ProtectBottomRows: 1})
			if _, err := pw.Write([]byte(tc.in)); err != nil {
				t.Fatalf("filter write: %v", err)
			}
			if row, col, _ := pw.ChildCursor(); row != tc.row || col != tc.col {
				t.Errorf("cursor (%d,%d), want (%d,%d)", row, col, tc.row, tc.col)
			}
		})
	}
}

// End-to-end shape of the reported corruption: an Ink-style prompt box
// (rule, prompt, rule, footer) that the child redraws in place with relative
// moves, while the overlay repaints its status bar between frames. The host
// screen must end up exactly as it would without the overlay.
func TestInkPromptRedrawSurvivesIndicatorDraws(t *testing.T) {
	const w, h = 60, 16
	rule := strings.Repeat("─", w)
	frame := func(prompt string) string {
		return rule + "\r\n❯ " + prompt + "\x1b[K\r\n" + rule + "\r\n  footer"
	}
	// Redraw = move to the top of the 4-row box, erase and repaint it.
	redraw := func(prompt string) string {
		return "\x1b[?2026h\x1b[3A\x1b[G" + frame(prompt) + "\x1b[?2026l"
	}
	chunks := []string{
		"welcome banner\r\n\r\n",
		frame(`Try "fix lint errors"`),
		redraw("/"),
		redraw("/model"),
		"\x1b[3A\x1b[G\x1b[J❯ /model\r\n  ⎿  Set model\r\n\r\n" + frame(""),
		redraw("/clear"),
	}

	// Reference: child talking straight to a terminal of its own size.
	ref := vt10x.New(vt10x.WithSize(w, h-1))
	for _, c := range chunks {
		_, _ = ref.Write([]byte(c))
	}

	host := vt10x.New(vt10x.WithSize(w, h))
	pw := newTrackedFilter(t, &bytes.Buffer{}, w, h)
	pw.out = host
	pw.EnforceScrollRegion()
	r := NewRenderer(host, w, h)
	for _, c := range chunks {
		if _, err := pw.Write([]byte(c)); err != nil {
			t.Fatalf("filter write: %v", err)
		}
		r.DrawIndicator(Status{})
	}

	for y := 0; y < h-1; y++ {
		if got, want := vtRow(host, y, w), vtRow(ref, y, w); got != want {
			t.Errorf("row %d:\n got  %q\n want %q", y+1, got, want)
		}
	}
}

func vtRow(term vt10x.Terminal, y, w int) string {
	var b strings.Builder
	for x := 0; x < w; x++ {
		c := term.Cell(x, y).Char
		if c == 0 {
			c = ' '
		}
		b.WriteRune(c)
	}
	return strings.TrimRight(b.String(), " ")
}
