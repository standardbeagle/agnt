package overlay

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/aichannel"
)

// refLineTap is the byte-at-a-time line assembler and cleaner the run-based
// one replaced, kept as the reference: the per-line tap must deliver exactly
// the same lines for any output split at any chunk boundaries.
type refLineTap struct {
	line                             bytes.Buffer
	pendingCR, animating, overflowed bool
	out                              []string
}

func refCleanLine(line string) string {
	line = strings.TrimRight(line, "\r\n")
	result := make([]byte, 0, len(line))
	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			i = aichannel.SkipEscape(line, i)
			continue
		}
		if line[i] >= 32 || line[i] == '\t' {
			result = append(result, line[i])
		}
		i++
	}
	cleaned := strings.TrimSpace(string(result))
	cleaned = strings.TrimSpace(strings.Trim(cleaned, "│┃║"))
	return TruncateRunes(cleaned, 117, "...")
}

func (r *refLineTap) write(p []byte) {
	for _, b := range p {
		switch b {
		case '\r':
			r.pendingCR = true
		case '\n':
			r.pendingCR = false
			r.overflowed = false
			if r.line.Len() > 0 || r.animating {
				if c := refCleanLine(r.line.String()); c != "" {
					r.out = append(r.out, c)
				}
			}
			r.line.Reset()
			r.animating = false
		default:
			if r.pendingCR {
				r.pendingCR = false
				r.line.Reset()
				r.animating = true
				r.overflowed = false
			}
			if r.overflowed {
				continue
			}
			r.line.WriteByte(b)
			if r.line.Len() > maxTappedLineBytes {
				r.overflowed = true
			}
		}
	}
}

// randomOutput mixes text, CR/LF in every arrangement, escapes, box glyphs,
// wide and multibyte runes, and lines long enough to hit both the 117-rune
// display cut and the 4096-byte tap cap.
func randomOutput(rng *rand.Rand) []byte {
	pieces := []string{
		"hello", " ", "\r", "\n", "\r\n", "\x1b[31m", "\x1b[0m", "\x1b]0;title\a", "\x1b]8;;http://x\x1b\\",
		"│", "┃ boxed ┃", "日本語", "👩‍💻", "\t", "\x07", "\x1b[2K", "\x1b", "error: boom", "   ",
	}
	var b bytes.Buffer
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
	return b.Bytes()
}

func TestLineTapMatchesByteAtATimeReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 300; iter++ {
		data := randomOutput(rng)
		var got []string
		am := NewActivityMonitor(&bytes.Buffer{}, ActivityMonitorConfig{
			InitialCols: 80, InitialRows: 24,
			OnOutputLine: func(s string) { got = append(got, s) },
		})
		ref := &refLineTap{}
		for rest := data; len(rest) > 0; {
			n := 1 + rng.Intn(min(len(rest), 700))
			am.captureForPreview(rest[:n])
			ref.write(rest[:n])
			rest = rest[n:]
		}
		am.Stop()
		if len(got) != len(ref.out) {
			t.Fatalf("iter %d: %d lines, reference %d", iter, len(got), len(ref.out))
		}
		for i := range got {
			if got[i] != ref.out[i] {
				t.Fatalf("iter %d line %d:\n got %q\n ref %q", iter, i, got[i], ref.out[i])
			}
		}
		if am.currentLine.String() != ref.line.String() || am.pendingCR != ref.pendingCR ||
			am.isAnimating != ref.animating || am.lineOverflowed != ref.overflowed {
			t.Fatalf("iter %d: carried state differs from reference", iter)
		}
	}
}

func TestCleanTextMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var scratch []byte
	for iter := 0; iter < 2000; iter++ {
		data := randomOutput(rng)
		line := data[:rng.Intn(min(len(data), 400))]
		want := refCleanLine(string(line))
		var got string
		got, scratch = cleanText(scratch, line)
		if got != want {
			t.Fatalf("iter %d: cleanText(%q)\n got %q\nwant %q", iter, line, got, want)
		}
		if s, _ := cleanText(nil, string(line)); s != want {
			t.Fatalf("iter %d: string form differs", iter)
		}
	}
}
