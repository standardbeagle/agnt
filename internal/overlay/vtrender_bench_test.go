package overlay

import (
	"io"
	"os"
	"testing"

	"github.com/standardbeagle/vt10x"
)

func loadCapture(tb testing.TB) []byte {
	tb.Helper()
	b, err := os.ReadFile("testdata_opencode.bin")
	if err != nil {
		tb.Skipf("capture missing: %v", err)
	}
	return b
}

func newScreen(cols, rows int) vt10x.Terminal {
	t := vt10x.New()
	t.Resize(cols, rows)
	return t
}

// W1 — ingest. Every child byte is parsed into the virtual screen. This is
// already on the output path today; the question is its throughput headroom.
func BenchmarkVTIngest(b *testing.B) {
	data := loadCapture(b)
	t := newScreen(100, 30) // reuse: the cost under test is parsing, not setup
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t.Write(data)
	}
}

// W2 — full-screen repaint, the overlay-close path. Budget is one frame.
func benchRender(b *testing.B, cols, rows int) {
	data := loadCapture(b)
	t := newScreen(cols, rows)
	t.Write(data)
	b.ReportMetric(float64(cols*rows), "cells")
	b.ResetTimer()
	var n int
	for i := 0; i < b.N; i++ {
		n = len(RenderScreenANSI(t, 0))
	}
	b.StopTimer()
	b.ReportMetric(float64(n), "outbytes")
}

func BenchmarkRender_100x30(b *testing.B)  { benchRender(b, 100, 30) }
func BenchmarkRender_200x50(b *testing.B)  { benchRender(b, 200, 50) }
func BenchmarkRender_400x100(b *testing.B) { benchRender(b, 400, 100) }

// BenchmarkCommandPaletteTyping measures the actual command-palette
// repaint path at a common terminal size. Keep this below one 60 Hz frame
// (16.7 ms) so palette typing remains responsive as the command list grows.
func BenchmarkCommandPaletteTyping(b *testing.B) {
	r := NewRenderer(io.Discard, 120, 40)
	panels := []PanelItem{{Type: "overview", Label: "overview"}}
	status := Status{DaemonConnected: ConnectionConnected}
	queries := []string{"", "s", "st", "sta", "star", "start", "start ", "start d"}
	r.DrawPanelView(panels, 0, status, 0, true, "", 0, false, OverviewActions{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.DrawPanelView(panels, 0, status, 0, true, queries[i%len(queries)], 0, false, OverviewActions{})
	}
}
