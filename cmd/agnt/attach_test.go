package main

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

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
	// The trailing 'b' cannot possibly extend into the chord (chord[0] is
	// 0x1c, not 'b'), so it must be forwarded immediately in this same Feed
	// call rather than withheld as carry until the next read/Flush.
	if string(forward) != "a\x1cb" {
		t.Fatalf("forward = %q, want %q (non-prefix trailing byte must not be withheld)", forward, "a\x1cb")
	}
	if got := s.Flush(); len(got) != 0 {
		t.Fatalf("Flush() = %q, want empty (nothing left carried)", got)
	}
}

// TestChordCarryScanner_NonPrefixByteEmittedImmediately is the direct
// regression test for the one-keystroke echo lag: with the default 2-byte
// chord and single-byte raw-mode reads, a byte that cannot possibly start
// the chord (chord[0] is 0x1c) must be forwarded in the very Feed call it
// arrives in, not withheld until the next Feed.
func TestChordCarryScanner_NonPrefixByteEmittedImmediately(t *testing.T) {
	s := newChordCarryScanner([]byte{0x1c, 0x1c})
	forward, detached := s.Feed([]byte("x"))
	if detached {
		t.Fatalf("single ordinary byte must not trigger detach")
	}
	if string(forward) != "x" {
		t.Fatalf("forward = %q, want %q (byte must be emitted in the same Feed call, not withheld)", forward, "x")
	}
	if got := s.Flush(); len(got) != 0 {
		t.Fatalf("Flush() = %q, want empty (nothing should be carried for a non-prefix byte)", got)
	}
}

// TestChordCarryScanner_ChordStillDetectedByteByByte pins that fixing the
// lag does not regress chord detection itself when fed one byte at a time
// (the real raw-mode read shape on the unix attach path).
func TestChordCarryScanner_ChordStillDetectedByteByByte(t *testing.T) {
	s := newChordCarryScanner([]byte{0x1c, 0x1c})

	forward1, detached1 := s.Feed([]byte{0x1c})
	if detached1 {
		t.Fatalf("first chord byte alone must not trigger detach")
	}
	if len(forward1) != 0 {
		t.Fatalf("forward1 = %q, want empty (first chord byte must be held, it may still start the chord)", forward1)
	}

	forward2, detached2 := s.Feed([]byte{0x1c})
	if !detached2 {
		t.Fatalf("expected chord to complete on second byte")
	}
	if len(forward2) != 0 {
		t.Fatalf("forward2 = %q, want empty (chord bytes must never be forwarded)", forward2)
	}
}

// TestChordCarryScanner_PrefixByteThenNonCompletingByteForwardsBoth pins the
// other half of the fix: chord[0] is legitimately held back (it might still
// complete the chord), but once the next byte proves it won't, both bytes
// must reach the remote, in order, without waiting for a third Feed.
func TestChordCarryScanner_PrefixByteThenNonCompletingByteForwardsBoth(t *testing.T) {
	s := newChordCarryScanner([]byte{0x1c, 0x1c})

	forward1, detached1 := s.Feed([]byte{0x1c})
	if detached1 || len(forward1) != 0 {
		t.Fatalf("first chord byte must be held with nothing forwarded, got forward=%q detached=%v", forward1, detached1)
	}

	forward2, detached2 := s.Feed([]byte("y"))
	if detached2 {
		t.Fatalf("Ctrl-\\ followed by 'y' must not trigger detach")
	}
	if string(forward2) != "\x1cy" {
		t.Fatalf("forward2 = %q, want %q (both bytes forwarded, in order, once the chord can no longer complete)", forward2, "\x1cy")
	}
}

// TestChordCarryScanner_LongerChordPartialPrefixCarriesAcrossFeeds exercises
// a 3-byte chord to confirm the longest-proper-prefix carry logic (not just
// the 2-byte special case) holds across more than one partial-match Feed.
func TestChordCarryScanner_LongerChordPartialPrefixCarriesAcrossFeeds(t *testing.T) {
	chord := []byte{0x01, 0x02, 0x03}
	s := newChordCarryScanner(chord)

	// "z" then the first two chord bytes: "z" is not a prefix of the chord
	// and must be forwarded immediately; 0x01 0x02 is a proper prefix and
	// must be held.
	forward1, detached1 := s.Feed([]byte{'z', 0x01, 0x02})
	if detached1 {
		t.Fatalf("partial chord prefix must not trigger detach")
	}
	if string(forward1) != "z" {
		t.Fatalf("forward1 = %q, want %q ('z' forwarded, 0x01 0x02 held as a proper chord prefix)", forward1, "z")
	}

	// A byte that doesn't complete the chord: the held prefix cannot extend,
	// so the held bytes plus the new non-completing byte must all forward.
	forward2, detached2 := s.Feed([]byte{0x09})
	if detached2 {
		t.Fatalf("chord must not be considered complete")
	}
	if string(forward2) != "\x01\x02\x09" {
		t.Fatalf("forward2 = %q, want %q (held prefix + non-completing byte forwarded together, in order)", forward2, "\x01\x02\x09")
	}

	// Now drive an actual split completion: feed the first two bytes again
	// (held), then the third completes the chord and nothing is forwarded.
	forward3, detached3 := s.Feed([]byte{0x01, 0x02})
	if detached3 || len(forward3) != 0 {
		t.Fatalf("forward3 = %q detached=%v, want empty/false (0x01 0x02 is a proper prefix, held)", forward3, detached3)
	}
	forward4, detached4 := s.Feed([]byte{0x03})
	if !detached4 {
		t.Fatalf("expected chord to complete across the split feed")
	}
	if len(forward4) != 0 {
		t.Fatalf("forward4 = %q, want empty (chord bytes must never be forwarded)", forward4)
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
