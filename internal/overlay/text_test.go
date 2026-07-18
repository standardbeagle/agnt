package overlay

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		max    int
		suffix string
		expect string
	}{
		{"under limit unchanged", "hello", 10, "...", "hello"},
		{"exact limit unchanged", "hello", 5, "...", "hello"},
		{"ascii truncated", "hello world", 5, "...", "hello..."},
		{"zero max unchanged", "hello", 0, "...", "hello"},
		{"negative max unchanged", "hello", -1, "...", "hello"},
		{"empty suffix", "hello world", 5, "", "hello"},
		// The load-bearing case: cutting multibyte content must not split a rune.
		{"multibyte cut on boundary", "héllo wörld", 5, "...", "héllo..."},
		{"all multibyte", "日本語テスト", 3, "…", "日本語…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.in, tt.max, tt.suffix)
			if got != tt.expect {
				t.Errorf("TruncateRunes(%q, %d, %q) = %q, want %q", tt.in, tt.max, tt.suffix, got, tt.expect)
			}
			if !utf8.ValidString(got) {
				t.Errorf("TruncateRunes produced invalid UTF-8: %q", got)
			}
		})
	}
}
