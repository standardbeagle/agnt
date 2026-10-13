package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe bytes.Buffer for editor output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

func feedBytes(le *LineEditor, bs []byte) {
	for _, b := range bs {
		le.Feed(b)
	}
}

func nextLine(t *testing.T, le *LineEditor) string {
	t.Helper()
	select {
	case line := <-le.Lines():
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("no line submitted")
		return ""
	}
}

// TestLineEditorAcceptsUTF8 is the regression for the byte-range filter that
// dropped every non-ASCII byte. Input is fed one byte per Feed call — exactly
// how the input router delivers it — so every multi-byte rune straddles a
// call boundary.
func TestLineEditorAcceptsUTF8(t *testing.T) {
	for _, want := range []string{"café", "日本語", "héllo wörld", "ok 👍 done", "ß"} {
		out := &syncBuffer{}
		le := NewLineEditor(out, "> ")
		feedBytes(le, []byte(want))
		le.Feed('\r')
		if got := nextLine(t, le); got != want {
			t.Errorf("submitted %q, want %q", got, want)
		}
		if echoed := out.String(); !strings.Contains(echoed, want) {
			t.Errorf("echo %q does not contain %q", echoed, want)
		}
	}
}

// TestLineEditorRuneAwareEditing covers cursor motion, backspace and
// word-delete over multi-byte and double-width runes.
func TestLineEditorRuneAwareEditing(t *testing.T) {
	t.Run("backspace removes a whole rune", func(t *testing.T) {
		le := NewLineEditor(&syncBuffer{}, "> ")
		feedBytes(le, []byte("aé日"))
		le.Feed(0x7f)
		le.Feed(0x7f)
		le.Feed('\r')
		if got := nextLine(t, le); got != "a" {
			t.Fatalf("got %q, want %q", got, "a")
		}
	})

	t.Run("backspace erases both cells of a wide rune", func(t *testing.T) {
		out := &syncBuffer{}
		le := NewLineEditor(out, "> ")
		feedBytes(le, []byte("日"))
		out.Reset()
		le.Feed(0x7f)
		if got := out.String(); got != "\b\b  \b\b" {
			t.Fatalf("erase sequence %q, want two-cell erase", got)
		}
	})

	t.Run("insert in the middle lands on a rune boundary", func(t *testing.T) {
		le := NewLineEditor(&syncBuffer{}, "> ")
		feedBytes(le, []byte("日語"))
		le.FeedKey("Left")
		feedBytes(le, []byte("本"))
		le.Feed('\r')
		if got := nextLine(t, le); got != "日本語" {
			t.Fatalf("got %q, want %q", got, "日本語")
		}
	})

	t.Run("cursor left over a wide rune moves two cells", func(t *testing.T) {
		out := &syncBuffer{}
		le := NewLineEditor(out, "> ")
		feedBytes(le, []byte("a日"))
		out.Reset()
		le.FeedKey("Left")
		le.FeedKey("Left")
		if got := out.String(); got != "\033[2D\033[1D" {
			t.Fatalf("cursor sequence %q, want 2 cells then 1", got)
		}
	})

	t.Run("word delete", func(t *testing.T) {
		le := NewLineEditor(&syncBuffer{}, "> ")
		feedBytes(le, []byte("héllo wörld"))
		le.Feed(0x17) // Ctrl+W
		le.Feed('\r')
		if got := nextLine(t, le); got != "héllo " {
			t.Fatalf("got %q, want %q", got, "héllo ")
		}
	})

	t.Run("history round-trips non-ASCII", func(t *testing.T) {
		le := NewLineEditor(&syncBuffer{}, "> ")
		feedBytes(le, []byte("naïve"))
		le.Feed('\r')
		nextLine(t, le)
		le.FeedKey("Up")
		le.Feed('\r')
		if got := nextLine(t, le); got != "naïve" {
			t.Fatalf("got %q, want %q", got, "naïve")
		}
	})
}

// TestLineEditorDiscardsBrokenUTF8 pins the exhaustion branch: a truncated or
// invalid sequence is dropped whole and the bytes after it are still read
// correctly, rather than the tail being reinterpreted as fresh input.
func TestLineEditorDiscardsBrokenUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"truncated lead then ascii", []byte{'a', 0xe6, 0x97, 'b'}, "ab"},
		{"stray continuation", []byte{'a', 0x80, 'b'}, "ab"},
		{"lead replaced by new lead", append([]byte{0xe6, 0x97}, []byte("é!")...), "é!"},
		{"invalid lead", []byte{0xff, 'x'}, "x"},
		{"C1 control is not inserted", []byte{0xc2, 0x9b, 'x'}, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			le := NewLineEditor(&syncBuffer{}, "> ")
			feedBytes(le, tc.in)
			le.Feed('\r')
			if got := nextLine(t, le); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLineEditorArrowKeysStillParseAfterUTF8 guards the interaction between
// the UTF-8 assembler and the escape reader: an escape sequence arriving right
// after (or in place of the tail of) a multi-byte rune is still a key.
func TestLineEditorArrowKeysStillParseAfterUTF8(t *testing.T) {
	le := NewLineEditor(&syncBuffer{}, "> ")
	feedBytes(le, []byte("é"))
	feedBytes(le, []byte{0xe6, 0x97})     // rune cut short…
	feedBytes(le, []byte{0x1b, '[', 'D'}) // …by a Left arrow
	feedBytes(le, []byte("x"))
	le.Feed('\r')
	if got := nextLine(t, le); got != "xé" {
		t.Fatalf("got %q, want %q", got, "xé")
	}
}

// TestLineEditorInterruptWhileInactive is the regression for "a running turn
// cannot be interrupted": in raw mode Ctrl+C is byte 0x03, and the inactive
// editor used to drop it along with everything else.
func TestLineEditorInterruptWhileInactive(t *testing.T) {
	le := NewLineEditor(&syncBuffer{}, "> ")
	fired := 0
	le.SetInterruptHandler(func() { fired++ })

	// Active: Ctrl+C clears the line, it does not interrupt.
	le.Feed(0x03)
	if fired != 0 {
		t.Fatalf("interrupt fired while active")
	}

	le.SetActive(false)
	feedBytes(le, []byte("ignored"))
	if fired != 0 {
		t.Fatalf("non-interrupt bytes fired the handler")
	}
	le.Feed(0x03)
	if fired != 1 {
		t.Fatalf("interrupt fired %d times, want 1", fired)
	}

	// Typed-while-inactive bytes must not leak into the next line.
	le.SetActive(true)
	feedBytes(le, []byte("next"))
	le.Feed('\r')
	if got := nextLine(t, le); got != "next" {
		t.Fatalf("got %q, want %q", got, "next")
	}
}

// TestLineEditorAsk covers the mid-turn question path used for ACP permission
// prompts: the editor is inactive (turn running), Ask takes it over, the
// answer goes to Ask and not to Lines(), and the prior state is restored.
func TestLineEditorAsk(t *testing.T) {
	out := &syncBuffer{}
	le := NewLineEditor(out, "> ")
	feedBytes(le, []byte("draft"))
	le.SetActive(false)

	type result struct {
		line string
		ok   bool
	}
	got := make(chan result, 1)
	go func() {
		line, ok := le.Ask(context.Background(), "Choose: ")
		got <- result{line, ok}
	}()

	// Wait until Ask owns the editor (it draws its prompt when it does).
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Choose: ") {
		if time.Now().After(deadline) {
			t.Fatal("Ask never drew its prompt")
		}
		time.Sleep(time.Millisecond)
	}

	feedBytes(le, []byte("2\r"))
	select {
	case r := <-got:
		if !r.ok || r.line != "2" {
			t.Fatalf("Ask = (%q, %v), want (\"2\", true)", r.line, r.ok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return")
	}

	select {
	case line := <-le.Lines():
		t.Fatalf("Ask answer leaked to Lines(): %q", line)
	default:
	}
	if le.IsActive() {
		t.Fatal("editor left active after Ask during a turn")
	}

	// The interrupted draft survives.
	le.SetActive(true)
	le.Feed('\r')
	if line := nextLine(t, le); line != "draft" {
		t.Fatalf("draft = %q, want %q", line, "draft")
	}
}

// TestLineEditorAskCancel: Ctrl+C and a cancelled context both end Ask with
// ok=false instead of leaving the caller blocked.
func TestLineEditorAskCancel(t *testing.T) {
	t.Run("ctrl-c", func(t *testing.T) {
		out := &syncBuffer{}
		le := NewLineEditor(out, "> ")
		done := make(chan bool, 1)
		go func() {
			_, ok := le.Ask(context.Background(), "Choose: ")
			done <- ok
		}()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(out.String(), "Choose: ") {
			if time.Now().After(deadline) {
				t.Fatal("Ask never drew its prompt")
			}
			time.Sleep(time.Millisecond)
		}
		le.Feed(0x03)
		select {
		case ok := <-done:
			if ok {
				t.Fatal("Ask reported ok after Ctrl+C")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Ask did not return after Ctrl+C")
		}
	})

	t.Run("context", func(t *testing.T) {
		le := NewLineEditor(&syncBuffer{}, "> ")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, ok := le.Ask(ctx, "Choose: "); ok {
			t.Fatal("Ask reported ok with a cancelled context")
		}
	})
}
