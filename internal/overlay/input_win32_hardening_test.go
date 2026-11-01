package overlay

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feedSplit runs every cut of input through one decoder in two parse calls,
// carrying the remainder the way the scanner does, and returns what each cut
// emitted. A decoder that only works on whole buffers fails here.
func feedSplit(input string) map[int]string {
	out := map[int]string{}
	for cut := 1; cut < len(input); cut++ {
		var d win32Decoder
		got, rem := d.parse([]byte(input[:cut]))
		next := append(append([]byte{}, rem...), input[cut:]...)
		tail, rem2 := d.parse(next)
		out[cut] = string(got) + string(tail) + string(rem2)
	}
	return out
}

// Any CSI the terminal sends that is not a win32 key report, such as an SGR
// mouse report, a kitty key or a cursor-position reply, reaches the child
// whole. A read boundary inside it must not release a fragment early: the
// fragment ends up in one write and the rest in the next.
func TestWin32DecoderHoldsEverySplitCSIUntilComplete(t *testing.T) {
	for _, seq := range []string{
		"\x1b[<35;10;5M",                 // SGR mouse motion: '<' private prefix
		"\x1b[?1;2c",                     // DA reply: '?' prefix
		"\x1b[>0;95;0c",                  // DA2 reply: '>' prefix
		"\x1b[12;40R",                    // cursor position reply
		"\x1b[99;5u",                     // kitty Ctrl+C
		"\x1b[200~",                      // bracketed paste start
		"\x1b[1 q",                       // intermediate byte
		"\x1b[MabC"[:6],                  // X10 mouse: ESC [ M + 3 raw bytes
		"\x1bOA",                         // SS3 Up
		"\x1b]11;rgb:1e1e/1e1e/1e1e\x07", // OSC 11 reply, BEL
		"\x1bP>|WindowsTerminal\x1b\\",   // DCS reply, ST
	} {
		for cut := 1; cut < len(seq); cut++ {
			var d win32Decoder
			got, rem := d.parse([]byte(seq[:cut]))
			assert.Empty(t, got, "%q cut at %d: a fragment was released before the sequence completed", seq, cut)
			tail, rem2 := d.parse(append(append([]byte{}, rem...), seq[cut:]...))
			assert.Equal(t, seq, string(tail), "%q cut at %d: reassembled sequence", seq, cut)
			assert.Empty(t, rem2, "%q cut at %d: nothing left over", seq, cut)
		}
	}
}

// A win32 key report split at any byte still decodes to the key.
func TestWin32DecoderReassemblesSplitKeyReports(t *testing.T) {
	report := string(buildWin32Seq(0x41, 30, 'a', 1, 0, 1))
	for cut, got := range feedSplit(report) {
		assert.Equal(t, "a", got, "cut at %d", cut)
	}
}

// A CSI that never ends is not held forever: past the cap it is dropped to
// its final byte, and the keys after it still arrive.
func TestWin32DecoderDropsARunawayCSIAndRecovers(t *testing.T) {
	var d win32Decoder
	var emitted strings.Builder
	var rem []byte
	feed := func(s string) {
		out, r := d.parse(append(rem, s...))
		rem = r
		emitted.Write(out)
	}
	feed("\x1b[")
	for range 200 {
		feed("1;")
	}
	assert.LessOrEqual(t, len(rem), win32CSIMaxLen, "a runaway CSI is not held past its cap")
	feed("9_") // the runaway's final byte: dropped with it
	feed("x")  // the next key
	assert.Equal(t, "x", emitted.String(), "the runaway is dropped whole and typing resumes")
}

// The discard after a runaway is itself bounded: a stream that never sends a
// final byte cannot swallow input forever.
func TestWin32DecoderDiscardIsBounded(t *testing.T) {
	var d win32Decoder
	_, rem := d.parse([]byte("\x1b[" + strings.Repeat("1", win32CSIMaxLen+1)))
	require.Empty(t, rem)
	_, _ = d.parse([]byte(strings.Repeat("2", stringMaxLen+1)))
	out, _ := d.parse([]byte("ok"))
	assert.Equal(t, "ok", string(out), "after stringMaxLen bytes the discard gives up")
}

// Field values are bounded. An overflowing or out-of-range number is a
// malformed report and is dropped, never turned into a garbage character.
func TestWin32DecoderRejectsOutOfRangeFields(t *testing.T) {
	for _, body := range []string{
		"65;30;99999999999999999999;1;0;1", // Uc overflows int
		"65;30;70000;1;0;1",                // Uc above one UTF-16 unit
		"65;30;97;2;0;1",                   // Kd is 0 or 1
		"65;30;97;1;0;1;7",                 // seven fields
		"99999;30;97;1;0;1",                // Vk above a WORD
	} {
		var d win32Decoder
		out, rem := d.parse([]byte("\x1b[" + body + "_z"))
		assert.Equal(t, "z", string(out), "%q: malformed report dropped, the next byte kept", body)
		assert.Empty(t, rem)
	}
}

// PageUp and PageDown arrive as Vk with no character; they were dropped.
func TestWin32DecoderTranslatesPageKeys(t *testing.T) {
	cases := map[string][]byte{
		"\x1b[5~":   buildWin32Seq(0x21, 73, 0, 1, 0, 1),
		"\x1b[6~":   buildWin32Seq(0x22, 81, 0, 1, 0, 1),
		"\x1b[5;5~": buildWin32Seq(0x21, 73, 0, 1, csCtrl, 1),
		"\x1b[6;2~": buildWin32Seq(0x22, 81, 0, 1, csShift, 1),
	}
	for want, in := range cases {
		got, _ := parseWin32InputModeInternal(in)
		assert.Equal(t, want, string(got))
	}
}

// Rc is the repeat count: one report for a held key can stand for several
// presses. It is honoured, within a cap, and zero means one.
func TestWin32DecoderHonoursRepeatCount(t *testing.T) {
	got, _ := parseWin32InputModeInternal(buildWin32Seq(0x41, 30, 'a', 1, 0, 3))
	assert.Equal(t, "aaa", string(got))
	got, _ = parseWin32InputModeInternal(buildWin32Seq(0x41, 30, 'a', 1, 0, 0))
	assert.Equal(t, "a", string(got), "Rc 0 is one press")
	got, _ = parseWin32InputModeInternal(buildWin32Seq(0x25, 75, 0, 1, 0, 2))
	assert.Equal(t, "\x1b[D\x1b[D", string(got), "repeat applies to translated keys too")
	got, _ = parseWin32InputModeInternal(buildWin32Seq(0x41, 30, 'a', 1, 0, 60000))
	assert.Equal(t, strings.Repeat("a", win32RepeatMax), string(got), "repeat is capped")
}

// Alt+numpad composition delivers its character on the Alt key-up. Dropping
// every key-up lost it.
func TestWin32DecoderKeepsAltNumpadCharacter(t *testing.T) {
	got, _ := parseWin32InputModeInternal(buildWin32Seq(0x12, 56, 0xe9, 0, 0, 1))
	assert.Equal(t, "é", string(got), "the character on the Alt key-up is typed")
	got, _ = parseWin32InputModeInternal(buildWin32Seq(0x41, 30, 'a', 0, 0, 1))
	assert.Empty(t, got, "an ordinary key-up still types nothing")
}

// Ctrl+Space carries no character; it is NUL in a VT terminal.
func TestWin32DecoderCtrlSpaceIsNUL(t *testing.T) {
	got, _ := parseWin32InputModeInternal(buildWin32Seq(0x20, 57, 0, 1, csCtrl, 1))
	assert.Equal(t, "\x00", string(got))
}

// A sequence fragment that goes stale is the head of a unit whose tail may
// still come. Releasing it typed "\x1b[65;30" into the agent and the late tail
// "97;1;0;1_" after it. The fragment is dropped and so is its tail.
func TestScannerDropsAStaleFragmentAndItsTail(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	chunks := make(chan string, 8)
	go func() {
		for c := range scanWin32Chunks(pr, 5*time.Millisecond) {
			chunks <- string(c)
		}
		close(chunks)
	}()

	_, _ = pw.Write([]byte("\x1b[65;30"))
	time.Sleep(3 * staleSequenceFactor * 5 * time.Millisecond) // 3× the 100ms budget
	_, _ = pw.Write([]byte(";97;1;0;1_b"))
	_ = pw.Close()

	var got strings.Builder
	for c := range chunks {
		got.WriteString(c)
	}
	assert.Equal(t, "b", got.String(), "the stale head and its tail are dropped; the next key survives")
}

// A read error other than EOF still delivers what was held.
func TestScannerFlushesPendingOnReadError(t *testing.T) {
	r := &errAfter{data: []byte{0x1b}, err: errors.New("tty gone")}
	var got []byte
	for c := range scanWin32Chunks(r, time.Hour) {
		got = append(got, c...)
	}
	assert.Equal(t, []byte{0x1b}, got)
}

type errAfter struct {
	data []byte
	err  error
	done bool
}

func (e *errAfter) Read(p []byte) (int, error) {
	if e.done {
		return 0, e.err
	}
	e.done = true
	return copy(p, e.data), nil
}
