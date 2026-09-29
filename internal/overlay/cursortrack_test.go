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
		"ZWJ emoji is one wide cell":  {"\x1b[3;1H", "👩‍💻", "x"},
		"flag is one wide cell":       {"\x1b[3;1H", "🇯🇵", "x"},
		"skin tone joins its base":    {"\x1b[3;1H", "👍🏽", "x"},
		"VS16 widens its base":        {"\x1b[3;1H", "⚠️", "x"},
		"combining mark joins":        {"\x1b[3;1H", "e\u0301", "x"},
		"cluster split across writes": {"\x1b[3;1H", "👩", "\u200d", "💻", "x"},
		"sync update frame": {
			"\x1b[?2026h\x1b[8;1H" + rule + "\r\n❯ \r\n" + rule, "\x1b[?2026l",
			"\x1b[2A\x1b[G", "❯ hi",
		},
	}
	for name, chunks := range cases {
		t.Run(name, func(t *testing.T) { trackerDivergence(t, w, h, chunks) })
	}
}

// xterm edges the tracker must get right: margin-bounded CUD/CUU, wide glyphs
// at the right edge, clusters, and CSI ... u sequences that are not SCORC.
// Each case pins the xterm coordinates AND holds the tracker to a vt10x host
// fed the same bytes (vt10x models all of these since v0.1.0-agnt.2), so the
// expectation and the terminal model check each other.
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
			host := vt10x.New(vt10x.WithSize(w, h))
			pw := NewProtectedWriter(host, w, h, FilterConfig{ProtectBottomRows: 1})
			pw.EnforceScrollRegion()
			if _, err := pw.Write([]byte(tc.in)); err != nil {
				t.Fatalf("filter write: %v", err)
			}
			if row, col, _ := pw.ChildCursor(); row != tc.row || col != tc.col {
				t.Errorf("tracker cursor (%d,%d), want (%d,%d)", row, col, tc.row, tc.col)
			}
			if cur := host.Cursor(); cur.Y+1 != tc.row || cur.X+1 != tc.col {
				t.Errorf("host terminal cursor (%d,%d), want (%d,%d)", cur.Y+1, cur.X+1, tc.row, tc.col)
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
		b.WriteString(glyphText(term.Cell(x, y)))
	}
	return strings.TrimRight(b.String(), " ")
}

// The restore CUP is read when a draw is built, but the child keeps writing
// while the overlay formats its bytes. A child chunk landing in that gap must
// not send the cursor back to where the child *was*.
func TestDrawRestoresTheChildsCursorAsOfTheWrite(t *testing.T) {
	const w, h = 40, 12
	host := vt10x.New(vt10x.WithSize(w, h))
	pw := newTrackedFilter(t, &bytes.Buffer{}, w, h)
	pw.out = host
	pw.EnforceScrollRegion()
	if _, err := pw.Write([]byte("\x1b[8;5H")); err != nil {
		t.Fatalf("filter write: %v", err)
	}

	park, parked := parkCursor()
	draw := park + "\x1b[12;1Hstatus" + parked.restore()

	// Child output in the gap between building the draw and writing it.
	if _, err := pw.Write([]byte("\x1b[3A\x1b[G")); err != nil {
		t.Fatalf("filter write: %v", err)
	}
	writeOverChild(host, []byte(draw), parked)

	if cur := host.Cursor(); cur.Y+1 != 5 || cur.X+1 != 1 {
		t.Fatalf("after the draw the terminal cursor is at (%d,%d); the child left it at (5,1)", cur.Y+1, cur.X+1)
	}
}

// A character split across two PTY reads must reach the terminal whole: an
// overlay draw landing between the halves would corrupt the glyph and shift
// every column after it.
func TestSplitUTF8IsHeldUntilComplete(t *testing.T) {
	var out bytes.Buffer
	pw := NewProtectedWriter(&out, 40, 12, FilterConfig{ProtectBottomRows: 1})
	rule := []byte("─") // e2 94 80
	if _, err := pw.Write(append([]byte("ab"), rule[:2]...)); err != nil {
		t.Fatalf("filter write: %v", err)
	}
	if got := out.String(); got != "ab" {
		t.Fatalf("partial UTF-8 leaked before completion: %q", got)
	}
	if _, err := pw.Write(append(rule[2:3:3], 'c')); err != nil {
		t.Fatalf("filter write: %v", err)
	}
	if got := out.String(); got != "ab─c" {
		t.Fatalf("output %q, want %q", got, "ab─c")
	}
	if _, col, _ := pw.ChildCursor(); col != 5 {
		t.Errorf("cursor col %d, want 5", col)
	}

	// A sequence cut short by ESC is flushed as-is and occupies one cell.
	out.Reset()
	if _, err := pw.Write(append(rule[:2:2], "\x1b[K"...)); err != nil {
		t.Fatalf("filter write: %v", err)
	}
	if got, want := out.String(), string(rule[:2])+"\x1b[K"; got != want {
		t.Errorf("truncated sequence output %q, want %q", got, want)
	}
	if _, col, _ := pw.ChildCursor(); col != 6 {
		t.Errorf("cursor col %d after truncated sequence, want 6", col)
	}
}
