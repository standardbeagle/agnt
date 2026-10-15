package overlay

import (
	"bytes"
	"testing"
)

func TestParseWin32AltCharIsEscPrefixed(t *testing.T) {
	// Alt+b (readline word-back): xterm and conhost send ESC b. Dropping the
	// modifier typed a plain "b" into the child.
	got, _ := parseWin32InputModeInternal(buildWin32Seq(0x42, 48, 'b', 1, 0x02, 1))
	if !bytes.Equal(got, []byte{0x1b, 'b'}) {
		t.Fatalf("Alt+b: got %q, want %q", got, "\x1bb")
	}
	// Alt+Enter: ESC CR (the newline key in several agent TUIs).
	got, _ = parseWin32InputModeInternal(buildWin32Seq(0x0D, 28, 0x0D, 1, 0x01, 1))
	if !bytes.Equal(got, []byte{0x1b, '\r'}) {
		t.Fatalf("Alt+Enter: got %q, want %q", got, "\x1b\r")
	}
}

func TestParseWin32AltGrCharIsNotEscPrefixed(t *testing.T) {
	// AltGr is reported as RIGHT_ALT|LEFT_CTRL. AltGr+Q on a German layout is
	// '@' and must arrive as '@', not ESC @.
	got, _ := parseWin32InputModeInternal(buildWin32Seq(0x51, 16, '@', 1, 0x01|0x08, 1))
	if !bytes.Equal(got, []byte{'@'}) {
		t.Fatalf("AltGr+Q: got %q, want %q", got, "@")
	}
}
