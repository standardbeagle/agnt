package overlay

import (
	"io"
	"testing"
)

// The agent-output path in `agnt run`, as pty_common wires it: each PTY read
// is recorded, then passes ActivityMonitor (line tap, preview) ->
// ProtectedWriter (filter + the one screen model, shared with the monitor) ->
// OutputGate -> the terminal.
// The floor is one parse of the bytes into a screen model, which the overlay
// needs: BenchmarkOutputFloor_* parses the same chunks into one vt10x screen.

const chainCols, chainRows = 160, 48

func claudeFrames() [][]byte {
	frames := make([][]byte, 64)
	for i := range frames {
		frames[i] = claudeRedrawFrame(i, chainCols)
	}
	return frames
}

// chunked splits a capture into 4 KB reads, the PTY's usual delivery size.
func chunked(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := min(4096, len(data))
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

func benchOutputChain(b *testing.B, chunks [][]byte) {
	rec := NewTTYRecorder(ttyRecordBytes)
	gate := NewOutputGate(io.Discard)
	pw := NewProtectedWriter(gate, chainCols, chainRows, FilterConfig{ProtectBottomRows: 1})
	pw.EnforceScrollRegion()
	am := NewActivityMonitor(pw, ActivityMonitorConfig{
		SharedScreen:    pw,
		OnOutputLine:    func(string) {},
		OnOutputPreview: func([]string, string) {},
	})
	b.Cleanup(am.Stop)
	var total int64
	for _, c := range chunks {
		total += int64(len(c))
	}
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range chunks {
			rec.Record(TTYOut, "agent", c)
			if _, err := am.Write(c); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func benchOutputFloor(b *testing.B, chunks [][]byte) {
	t := newScreen(chainCols, chainRows-1)
	var total int64
	for _, c := range chunks {
		total += int64(len(c))
	}
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range chunks {
			t.Write(c)
		}
	}
}

func BenchmarkOutputChain_ClaudeRedraw(b *testing.B) { benchOutputChain(b, claudeFrames()) }
func BenchmarkOutputFloor_ClaudeRedraw(b *testing.B) { benchOutputFloor(b, claudeFrames()) }
func BenchmarkOutputChain_Opencode(b *testing.B)     { benchOutputChain(b, chunked(loadCapture(b))) }
func BenchmarkOutputFloor_Opencode(b *testing.B)     { benchOutputFloor(b, chunked(loadCapture(b))) }
