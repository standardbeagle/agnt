package overlay

import (
	"io"
	"testing"
	"time"
)

// Windows Terminal reports a typed or pasted character in win32-input-mode as
// its UTF-16 code unit (the Uc field). The child reads UTF-8, so anything
// outside ASCII has to be encoded, and a character outside the BMP arrives as
// two key events (high then low surrogate) that make one rune.

func TestParseWin32NonASCIIIsEncodedAsUTF8(t *testing.T) {
	tests := []struct {
		name string
		uc   int
		want string
	}{
		{"latin-1", 0xE9, "é"},
		{"cjk", 0x4E2D, "中"},
		{"box drawing", 0x2500, "─"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rem := parseWin32InputModeInternal(buildWin32Seq(0, 0, tt.uc, 1, 0, 1))
			if rem != nil {
				t.Fatalf("unexpected remainder %q", rem)
			}
			if string(got) != tt.want {
				t.Fatalf("uc=%#x: got %q (% x), want %q", tt.uc, got, got, tt.want)
			}
		})
	}
}

func TestParseWin32SurrogatePairIsOneRune(t *testing.T) {
	// 😀 U+1F600 = D83D DE00. Windows Terminal sends a down and an up event for
	// each half; the key-ups must not break the pair.
	var buf []byte
	buf = append(buf, buildWin32Seq(0, 0, 0xD83D, 1, 0, 1)...)
	buf = append(buf, buildWin32Seq(0, 0, 0xD83D, 0, 0, 1)...)
	buf = append(buf, buildWin32Seq(0, 0, 0xDE00, 1, 0, 1)...)
	buf = append(buf, buildWin32Seq(0, 0, 0xDE00, 0, 0, 1)...)
	got, _ := parseWin32InputModeInternal(buf)
	if string(got) != "😀" {
		t.Fatalf("got %q (% x), want %q", got, got, "😀")
	}
}

func TestParseWin32UnpairedSurrogateEmitsNothingBroken(t *testing.T) {
	// A high half followed by an ordinary character, and a stray low half,
	// must not leak a byte the child would read as garbage.
	var buf []byte
	buf = append(buf, buildWin32Seq(0, 0, 0xD83D, 1, 0, 1)...)
	buf = append(buf, buildWin32Seq(65, 0, 'a', 1, 0, 1)...)
	buf = append(buf, buildWin32Seq(0, 0, 0xDE00, 1, 0, 1)...)
	got, _ := parseWin32InputModeInternal(buf)
	if string(got) != "a" {
		t.Fatalf("got %q (% x), want %q", got, got, "a")
	}
}

func TestScanWin32InputJoinsSurrogatesAcrossReads(t *testing.T) {
	// The two halves of a pair can arrive in separate reads; the scanner has
	// to carry the high half over.
	pr, pw := io.Pipe()
	out := make(chan []byte, 1)
	go func() {
		var got []byte
		for b := range scanWin32Input(pr, time.Hour) {
			got = append(got, b)
		}
		out <- got
	}()
	if _, err := pw.Write(buildWin32Seq(0, 0, 0xD83D, 1, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := pw.Write(buildWin32Seq(0, 0, 0xDE00, 1, 0, 1)); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	select {
	case got := <-out:
		if string(got) != "😀" {
			t.Fatalf("got %q (% x), want %q", got, got, "😀")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scanner did not finish")
	}
}
