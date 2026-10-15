package overlay

import (
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestStartupSplash_TruncatesOnRuneBoundary renders the arrow-key tip at every
// width that cuts it. A byte-offset cut split "←" or "→" (3 bytes each) and
// wrote invalid UTF-8 to the terminal.
func TestStartupSplash_TruncatesOnRuneBoundary(t *testing.T) {
	const tip = "Tip: Press Ctrl+←/→ to browse process, proxy, and log panels"
	for width := 14; width <= utf8.RuneCountInString(tip)+4; width++ {
		out := &splashSafeWriter{}
		NewStartupSplash(out, width, 24).render(tip)
		got := out.String()
		if !utf8.ValidString(got) {
			t.Fatalf("width %d: splash wrote invalid UTF-8: %q", width, got)
		}
		if !strings.Contains(got, "Tip: ") {
			t.Fatalf("width %d: tip missing from output: %q", width, got)
		}
	}
}

// TestCleanLine_StripsWholeEscapeSequences: the alert scanner's line cleaner
// ended every escape at its first letter, so an OSC title or hyperlink target
// leaked into the scanned text, and a CSI whose final byte is not a letter
// (ESC [ 2 ~) or a two-byte escape (ESC 7) swallowed the start of the text.
func TestCleanLine_StripsWholeEscapeSequences(t *testing.T) {
	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{IdleTimeout: time.Hour})
	defer am.Stop()
	cases := map[string]string{
		"\x1b]0;my window title\x07build failed":                     "build failed",
		"\x1b]8;;https://example.com/x\x1b\\link\x1b]8;;\x1b\\ text": "link text",
		"\x1b]2;title with ST\x1b\\error: boom":                      "error: boom",
		"\x1b[2~error here":                                          "error here",
		"\x1b[200~pasted\x1b[201~":                                   "pasted",
		"\x1b7saved\x1b8 line":                                       "saved line",
		"\x1b(Bplain \x1b[1;31mred\x1b[0m":                           "plain red",
		"\x1b[?25lhidden cursor\r\n":                                 "hidden cursor",
		"unterminated \x1b]0;title never ends":                       "unterminated",
	}
	for in, want := range cases {
		if got := am.cleanLine(in); got != want {
			t.Errorf("cleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}
