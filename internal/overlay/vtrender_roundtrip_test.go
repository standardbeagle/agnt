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
