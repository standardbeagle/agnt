package overlay

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/standardbeagle/vt10x"
)

// The strongest correctness property for a renderer that mirrors a screen:
// feeding its output into a fresh emulator must reproduce the same screen.
func TestRenderRoundTrip(t *testing.T) {
	data, err := os.ReadFile("testdata_opencode.bin")
	if err != nil {
		t.Skip("no capture")
	}
	for _, sz := range [][2]int{{100, 30}, {200, 50}} {
		cols, rows := sz[0], sz[1]
		src := vt10x.New(vt10x.WithSize(cols, rows))
		src.Write(data)

		out := RenderScreenANSI(src, 0)

		dst := vt10x.New(vt10x.WithSize(cols, rows))
		dst.Write(out)

		diff := 0
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				a, b := src.Cell(x, y), dst.Cell(x, y)
				ac, bc := a.Char, b.Char
				if ac == 0 {
					ac = ' '
				}
				if bc == 0 {
					bc = ' '
				}
				if ac != bc || a.FG != b.FG || a.BG != b.BG {
					if diff < 5 {
						t.Errorf("%dx%d cell(%d,%d): src %q fg=%d bg=%d | rendered %q fg=%d bg=%d",
							cols, rows, x, y, string(ac), a.FG, a.BG, string(bc), b.FG, b.BG)
					}
					diff++
				}
			}
		}
		if diff > 0 {
			t.Errorf("%dx%d: %d/%d cells differ after round trip", cols, rows, diff, cols*rows)
		}
	}
}

// The repaint is only as good as the model behind it: assert the monitor's
// screen actually receives the child's bytes, and that it renders the same
// frame a bare emulator would. A monitor that silently stops being fed would
// otherwise repaint a blank screen and look like the bug it exists to fix.
func TestActivityMonitorScreenMatchesABareEmulator(t *testing.T) {
	data, err := os.ReadFile("testdata_opencode.bin")
	if err != nil {
		t.Skip("no capture")
	}
	cfg := DefaultActivityMonitorConfig()
	cfg.InitialCols, cfg.InitialRows = 100, 29
	cfg.OnOutputLine = func(string) {}
	am := NewActivityMonitor(io.Discard, cfg)
	defer am.Stop()

	if _, err := am.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	frame := am.RenderScreen(0)

	ref := vt10x.New(vt10x.WithSize(100, 29))
	ref.Write(data)
	want := RenderScreenANSI(ref, 0)

	if string(frame) != string(want) {
		t.Errorf("monitor frame (%d bytes) differs from a bare emulator fed the same bytes (%d)",
			len(frame), len(want))
	}
	// Direct colour is emitted inside the reset, as ESC[0;38;2;R;G;Bm.
	if !strings.Contains(string(frame), ";38;2;") {
		t.Errorf("frame carries no direct colour; the screen was not fed")
	}
}

// wideStream exercises what the emulator models since vt10x v0.1.0-agnt.2:
// two-column cells, multi-rune grapheme clusters, and the kitty keyboard
// sequences Claude Code sends at startup (which older vt10x misread as
// "restore cursor" and used to wipe the row).
const wideStream = "\x1b[>1u\x1b[?u" +
	"a世b 日本語\r\n" +
	"\x1b[38;2;215;119;87m👩‍💻 dev\x1b[39m 🇯🇵 ok\r\n" +
	"❯ /model\x1b[<u\r\n" +
	"é naïve é\r\n"

// TestRenderRoundTrip_WideAndClusters asserts the synthetic stream survives a
// render → re-parse round trip cell for cell, including which cells are the
// second half of a wide pair. A renderer that prints only Glyph.Char, or a
// space for a continuation cell, shifts the rest of the row right.
func TestRenderRoundTrip_WideAndClusters(t *testing.T) {
	const cols, rows = 40, 6
	src := vt10x.New(vt10x.WithSize(cols, rows))
	src.Write([]byte(wideStream))

	// Premise: the emulator really modelled the wide cells, or the round trip
	// below proves nothing about them.
	if g := src.Cell(1, 0); !g.IsWide() || g.Text() != "世" {
		t.Fatalf("premise: cell(1,0) = %q wide=%v, want wide 世", g.Text(), g.IsWide())
	}
	if g := src.Cell(0, 1); g.Text() != "👩‍💻" {
		t.Fatalf("premise: cell(0,1) = %q, want the whole ZWJ cluster", g.Text())
	}

	dst := vt10x.New(vt10x.WithSize(cols, rows))
	dst.Write(RenderScreenANSI(src, 0))

	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			a, b := src.Cell(x, y), dst.Cell(x, y)
			if glyphText(a) != glyphText(b) || a.IsWideContinuation() != b.IsWideContinuation() || a.FG != b.FG {
				t.Errorf("cell(%d,%d): src %q cont=%v fg=%d | rendered %q cont=%v fg=%d",
					x, y, a.Text(), a.IsWideContinuation(), a.FG, b.Text(), b.IsWideContinuation(), b.FG)
			}
		}
	}
}

// TestGlyphText pins the one rule every consumer shares: a continuation cell
// contributes nothing (its head already covered both columns), an empty cell
// is a space, anything else is its whole cluster.
func TestGlyphText(t *testing.T) {
	src := vt10x.New(vt10x.WithSize(10, 1))
	src.Write([]byte("世👩‍💻"))
	got := []string{}
	for x := 0; x < 6; x++ {
		got = append(got, glyphText(src.Cell(x, 0)))
	}
	want := []string{"世", "", "👩‍💻", "", " ", " "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("glyphText row = %q, want %q", got, want)
	}
}

// TestScreenTail_WideRowReadsAsWritten asserts the activity preview reads a
// CJK/emoji row as the text the child printed — no gap after each wide
// character and no dropped cluster runes.
func TestScreenTail_WideRowReadsAsWritten(t *testing.T) {
	cfg := DefaultActivityMonitorConfig()
	cfg.InitialCols, cfg.InitialRows = 60, 6
	cfg.OnOutputLine = func(string) {}
	am := NewActivityMonitor(io.Discard, cfg)
	defer am.Stop()
	if _, err := am.Write([]byte("日本語のテスト 👩‍💻 done\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	tail := strings.Join(am.screenTail(), "\n")
	if !strings.Contains(tail, "日本語のテスト 👩‍💻 done") {
		t.Fatalf("preview = %q, want the row as written", tail)
	}
}
