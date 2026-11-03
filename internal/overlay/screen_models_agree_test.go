package overlay

import (
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"

	"github.com/standardbeagle/vt10x"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With the indicator on, the monitor reads the child's screen from the
// filter's host model (SharedScreen) instead of parsing the output into a
// child-sized model of its own. That is sound only if the host model's child
// region is what a child-sized terminal shows: these tests feed the same
// output to both and compare every cell and the cursor after every chunk.
//
// Known, accepted gap: ESC followed directly by a multi-byte character
// ("\x1b日") reaches the host as different bytes than vt10x reads raw. The
// host model follows the bytes the terminal gets, which is the screen the
// user sees; agents do not emit it.
func TestHostModelChildRegionMatchesChildSizedScreen(t *testing.T) {
	var shell strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&shell, "line %d \x1b[32mok\x1b[0m %s\r\n", i, strings.Repeat("w", i%170))
	}
	rng := rand.New(rand.NewSource(3))
	var random [][]byte
	for i := 0; i < 40; i++ {
		random = append(random, wellFormedOutput(rng))
	}
	streams := map[string][][]byte{
		"claude":       claudeFrames(),
		"opencode":     chunked(loadCapture(t)),
		"shell-scroll": chunked([]byte(shell.String())),
		"random":       random,
		"alt-screen":   {[]byte("main\r\n\x1b[?1049h\x1b[H\x1b[2Jalt top\x1b[47;1Hbottom"), []byte("\x1b[?1049l after")},
	}
	for name, chunks := range streams {
		t.Run(name, func(t *testing.T) {
			pw, ref := newModelPair(t, 160, 48)
			for i, c := range chunks {
				_, _ = pw.Write(c)
				_, _ = ref.Write(c)
				require.Empty(t, diffChildRegion(ref, pw.hostModel, 47), "chunk %d", i)
			}
		})
	}
}

// After a resize the two models may differ in what scrolled off the top (each
// shrank from its own height; the host model's is what the terminal shows),
// but the cursor agrees at once and every cell agrees once the child prints.
func TestHostModelChildRegionMatchesChildSizedScreenAcrossResize(t *testing.T) {
	var shell strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&shell, "line %d %s\r\n", i, strings.Repeat("w", i%150))
	}
	for _, alt := range []bool{false, true} {
		for _, rows := range []int{30, 60} {
			for _, cols := range []int{100, 160, 200} {
				pw, ref := newModelPair(t, 160, 48)
				if alt {
					_, _ = pw.Write([]byte("\x1b[?1049h"))
					_, _ = ref.Write([]byte("\x1b[?1049h"))
				}
				_, _ = pw.Write([]byte(shell.String()))
				_, _ = ref.Write([]byte(shell.String()))
				pw.SetSize(cols, rows)
				ref.Resize(cols, rows-1)
				pc, rc := pw.hostModel.Cursor(), ref.Cursor()
				assert.Equal(t, [2]int{rc.X, rc.Y}, [2]int{pc.X, pc.Y}, "alt=%v %dx%d: cursor after resize", alt, cols, rows)
				_, _ = pw.Write([]byte(shell.String()))
				_, _ = ref.Write([]byte(shell.String()))
				assert.Empty(t, diffChildRegion(ref, pw.hostModel, rows-1), "alt=%v %dx%d: after output", alt, cols, rows)
			}
		}
	}
}

// The monitor's three screen readers — preview tail, input-box check, panel
// repaint — read the shared host model, never its protected status row, and
// the monitor parses nothing itself.
func TestMonitorReadsSharedScreen(t *testing.T) {
	pw := NewProtectedWriter(NewOutputGate(io.Discard), 40, 10, FilterConfig{ProtectBottomRows: 1})
	pw.EnforceScrollRegion()
	am := NewActivityMonitor(pw, ActivityMonitorConfig{SharedScreen: pw, PreviewMaxLines: 20,
		OnOutputPreview: func([]string, string) {}})
	defer am.Stop()
	require.Nil(t, am.ownScreen, "a shared screen leaves the monitor no model to feed")

	_, _ = am.Write([]byte("first line\r\nsecond input box text"))
	// Something only the host model holds: text on the protected row, put
	// there behind the filter's back.
	_, _ = pw.hostModel.Write([]byte("\x1b[10;1HSTATUSROW\x1b[2;22H"))

	tail := am.screenTail()
	assert.Equal(t, []string{"first line", "second input box text"}, tail)
	found, ok := am.CursorRowsContain("second input box text")
	assert.True(t, ok, "a shared screen is always fed, with or without callbacks")
	assert.True(t, found)
	found, _ = am.CursorRowsContain("STATUSROW")
	assert.False(t, found, "the protected row is not the child's")

	frame := string(am.RenderScreen())
	assert.Contains(t, frame, "second input box text")
	assert.NotContains(t, frame, "STATUSROW")
	assert.NotContains(t, frame, "\x1b[10;1H", "repaint stops above the status row")

	am.Resize(20, 5) // the filter owns a shared screen's size
	cols, rows := pw.hostModel.Size()
	assert.Equal(t, [2]int{40, 10}, [2]int{cols, rows})
}

func newModelPair(t *testing.T, cols, rows int) (*ProtectedWriter, vt10x.Terminal) {
	t.Helper()
	pw := NewProtectedWriter(NewOutputGate(io.Discard), cols, rows, FilterConfig{ProtectBottomRows: 1})
	t.Cleanup(pw.Stop)
	pw.EnforceScrollRegion()
	return pw, vt10x.New(vt10x.WithSize(cols, rows-1))
}

func diffChildRegion(ref, host vt10x.View, rows int) string {
	ref.Lock()
	defer ref.Unlock()
	host.Lock()
	defer host.Unlock()
	if a, b := ref.Cursor(), host.Cursor(); a.X != b.X || a.Y != b.Y {
		return fmt.Sprintf("cursor %d,%d vs host %d,%d", a.X, a.Y, b.X, b.Y)
	}
	var ra, rb []vt10x.Glyph
	for y := 0; y < rows; y++ {
		ra, rb = ref.Row(y, ra[:0]), host.Row(y, rb[:0])
		for x := range ra {
			if ra[x].Char != rb[x].Char || ra[x].FG != rb[x].FG || ra[x].BG != rb[x].BG || ra[x].Mode != rb[x].Mode {
				return fmt.Sprintf("row %d col %d: %q vs host %q", y, x, ra[x].Char, rb[x].Char)
			}
		}
	}
	return ""
}

// wellFormedOutput is randomOutput's piece mix without a bare ESC, plus the
// cursor, scroll-region and save/restore sequences TUIs repaint with.
func wellFormedOutput(rng *rand.Rand) []byte {
	pieces := []string{
		"hello", " ", "\r", "\n", "\r\n", "\x1b[31m", "\x1b[0m", "\x1b]0;title\a", "\x1b]8;;http://x\x1b\\",
		"│", "┃ boxed ┃", "日本語", "👩‍💻", "\t", "\x07", "\x1b[2K", "error: boom", "   ",
		"\x1b[5;10H", "\x1b[A", "\x1b[3B", "\x1b[J", "\x1b[1;20r", "\x1b[r", "\x1b7", "\x1b8", "\x1b[?25l", "\x1b[?25h",
	}
	var b strings.Builder
	for b.Len() < 6000 {
		switch rng.Intn(20) {
		case 0:
			b.WriteString(strings.Repeat("x", 100+rng.Intn(5000)))
		case 1:
			b.WriteString(strings.Repeat("é", 50+rng.Intn(200)))
		default:
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
	}
	return []byte(b.String())
}
