package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestFinishConsoleSetup_RestoresInputOnOutputModeFailure(t *testing.T) {
	want := errors.New("VT mode rejected")
	restored := 0
	err := finishConsoleSetup(func() error { return want }, func() error {
		restored++
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if restored != 1 {
		t.Fatalf("restore calls = %d, want 1", restored)
	}
}

func TestParseDetachChord(t *testing.T) {
	cases := []struct {
		spec    string
		want    []byte
		wantErr bool
	}{
		{spec: `ctrl-\ ctrl-\`, want: []byte{0x1c, 0x1c}},
		{spec: `CTRL-\ ctrl-\`, want: []byte{0x1c, 0x1c}}, // case-insensitive prefix
		{spec: `ctrl-a`, want: []byte{0x01}},
		{spec: ``, wantErr: true},
		{spec: `   `, wantErr: true},
		{spec: `x`, wantErr: true},
		{spec: `ctrl-`, wantErr: true},
		{spec: `ctrl-ab`, wantErr: true}, // token must be exactly one char after prefix
	}
	for _, c := range cases {
		got, err := parseDetachChord(c.spec)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseDetachChord(%q): expected error, got %v", c.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDetachChord(%q): unexpected error: %v", c.spec, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseDetachChord(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestPollAttachResize_ChangesOnlyAndJoinsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sizes := make(chan [2]int, 4)
	current := [2]int{80, 24}
	var currentMu sync.RWMutex
	joined := pollAttachResize(ctx, time.Millisecond,
		func() (int, int, error) {
			currentMu.RLock()
			defer currentMu.RUnlock()
			return current[0], current[1], nil
		},
		func(cols, rows int) { sizes <- [2]int{cols, rows} })
	select {
	case got := <-sizes:
		if got != current {
			t.Fatalf("initial size = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("initial resize not delivered")
	}
	select {
	case got := <-sizes:
		t.Fatalf("duplicate resize %v", got)
	case <-time.After(5 * time.Millisecond):
	}
	currentMu.Lock()
	current = [2]int{120, 40}
	currentMu.Unlock()
	select {
	case got := <-sizes:
		if got != current {
			t.Fatalf("changed size = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("changed resize not delivered")
	}
	cancel()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("resize worker did not join after cancellation")
	}
}

func TestDefaultDetachChord_DoesNotCollideWithOverlayControls(t *testing.T) {
	// The overlay command palette is triggered by printable ':' and '/'
	// (0x3a, 0x2f); the forwarding-pause hotkey is Ctrl+Up/Down, which are
	// ESC-prefixed CSI sequences ("\x1b[1;5A" / "\x1b[1;5B"). The default
	// detach chord (0x1c 0x1c) must overlap with neither family.
	for _, b := range defaultDetachChord {
		if b == ':' || b == '/' || b == 0x1b {
			t.Fatalf("default detach chord byte %#x collides with an overlay control", b)
		}
	}
}

func TestChordCarryScanner_WithinSingleChunk(t *testing.T) {
	s := newChordCarryScanner([]byte{0x1c, 0x1c})
	forward, detached := s.Feed([]byte("hello\x1c\x1cworld"))
	if !detached {
		t.Fatalf("expected chord to be detected")
	}
	if string(forward) != "hello" {
		t.Fatalf("forward = %q, want %q", forward, "hello")
	}
}

func TestChordCarryScanner_SplitAcrossReads(t *testing.T) {
	s := newChordCarryScanner([]byte{0x1c, 0x1c})

	forward1, detached1 := s.Feed([]byte("abc\x1c"))
	if detached1 {
		t.Fatalf("chord should not be complete yet")
	}
	if string(forward1) != "abc" {
		t.Fatalf("forward1 = %q, want %q", forward1, "abc")
	}

	forward2, detached2 := s.Feed([]byte{0x1c})
	if !detached2 {
		t.Fatalf("expected chord to complete on second read")
	}
	if len(forward2) != 0 {
		t.Fatalf("forward2 = %q, want empty (chord bytes must never be forwarded)", forward2)
	}
}

func TestChordCarryScanner_FalseStartIsForwarded(t *testing.T) {
	// A single Ctrl-\ not followed by a second one is ordinary input and
	// must reach the remote, not be silently swallowed.
	s := newChordCarryScanner([]byte{0x1c, 0x1c})
	forward, detached := s.Feed([]byte("a\x1cb"))
	if detached {
		t.Fatalf("single Ctrl-\\ must not trigger detach")
	}
	// The trailing byte is legitimately withheld as carry (it might start a
	// new chord on the next read) until Flush; nothing is silently dropped.
	got := append(append([]byte(nil), forward...), s.Flush()...)
	if string(got) != "a\x1cb" {
		t.Fatalf("forward+flush = %q, want %q (no bytes dropped on a false start)", got, "a\x1cb")
	}
}

func TestChordCarryScanner_FlushReturnsWithheldCarry(t *testing.T) {
	s := newChordCarryScanner([]byte{0x1c, 0x1c})
	_, detached := s.Feed([]byte("xyz\x1c"))
	if detached {
		t.Fatalf("chord should not be complete")
	}
	if got := s.Flush(); string(got) != "\x1c" {
		t.Fatalf("Flush() = %q, want the withheld Ctrl-\\ byte", got)
	}
	if got := s.Flush(); len(got) != 0 {
		t.Fatalf("second Flush() should be empty, got %q", got)
	}
}

func TestMatchSessionHostID(t *testing.T) {
	result := map[string]interface{}{
		"sessions": []interface{}{
			map[string]interface{}{"session_id": "claude-1", "name": "claude"},
			map[string]interface{}{"session_id": "claude-2", "name": "claude"},
			map[string]interface{}{"session_id": "unique-1", "name": "unique"},
		},
	}

	if id, ok := matchSessionHostID(result, "claude-2"); !ok || id != "claude-2" {
		t.Fatalf("exact id match failed: got (%q, %v)", id, ok)
	}
	if id, ok := matchSessionHostID(result, "unique"); !ok || id != "unique-1" {
		t.Fatalf("unique name match failed: got (%q, %v)", id, ok)
	}
	if _, ok := matchSessionHostID(result, "claude"); ok {
		t.Fatalf("ambiguous name match should fail, not silently pick one")
	}
	if _, ok := matchSessionHostID(result, "nope"); ok {
		t.Fatalf("unknown target should not match")
	}
}
