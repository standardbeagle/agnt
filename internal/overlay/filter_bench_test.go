package overlay

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// claudeRedrawFrame approximates one Ink repaint of Claude Code's prompt
// area: erase N lines upward, then redraw truecolor text, a full-width rule,
// the prompt and a status line with wide glyphs and a ZWJ cluster.
func claudeRedrawFrame(i, width int) []byte {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	for j := 0; j < 6; j++ {
		b.WriteString("\x1b[2K\x1b[1A")
	}
	b.WriteString("\x1b[2K\x1b[G")
	fmt.Fprintf(&b, "\x1b[38;2;215;119;87m✻\x1b[39m \x1b[38;2;153;153;153mThinking… (%ds · esc to interrupt)\x1b[39m\r\n", i)
	b.WriteString("\x1b[38;2;136;136;136m" + strings.Repeat("─", width) + "\x1b[39m\r\n")
	b.WriteString("❯ \x1b[7m \x1b[27m\r\n")
	b.WriteString("\x1b[38;2;136;136;136m" + strings.Repeat("─", width) + "\x1b[39m\r\n")
	b.WriteString("  \x1b[38;2;175;135;255m⏵⏵ bypass permissions on\x1b[39m · 日本語 👩‍💻\r\n")
	b.WriteString("\x1b[?2026l")
	return []byte(b.String())
}

// BenchmarkProtectedWriterClaudeRedraw measures the filter on the hot path:
// every byte the child writes passes through ProtectedWriter.Write.
func BenchmarkProtectedWriterClaudeRedraw(b *testing.B) {
	const w, h = 160, 48
	frames := make([][]byte, 64)
	var total int64
	for i := range frames {
		frames[i] = claudeRedrawFrame(i, w)
		total += int64(len(frames[i]))
	}
	pw := NewProtectedWriter(io.Discard, w, h, FilterConfig{ProtectBottomRows: 1})
	pw.EnforceScrollRegion()
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range frames {
			if _, err := pw.Write(f); err != nil {
				b.Fatal(err)
			}
		}
	}
}
