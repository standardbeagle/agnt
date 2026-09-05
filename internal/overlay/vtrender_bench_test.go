package overlay

import (
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
