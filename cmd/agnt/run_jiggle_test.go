//go:build unix

package main

import (
	"testing"

	"github.com/creack/pty"
)

// TestJiggleRepaintRestoresSize verifies the winsize jiggle leaves the PTY at
// its original size (the transient shrink is the repaint trigger; a permanent
// resize would be a bug). The fallback must NOT fire when the size is readable.
func TestJiggleRepaintRestoresSize(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	want := &pty.Winsize{Rows: 30, Cols: 100}
	if err := pty.Setsize(ptmx, want); err != nil {
		t.Fatalf("Setsize: %v", err)
	}

	fallbackFired := false
	jiggleRepaint(ptmx, func() { fallbackFired = true })

	if fallbackFired {
		t.Fatalf("fallback fired despite a readable winsize")
	}
	got, err := pty.GetsizeFull(ptmx)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if got.Rows != want.Rows || got.Cols != want.Cols {
		t.Fatalf("size not restored: got %dx%d want %dx%d", got.Rows, got.Cols, want.Rows, want.Cols)
	}
}

// TestJiggleRepaintDegenerateFallback verifies that a 1-row PTY (can't shrink
// below 1) takes the fallback path instead of jiggling.
func TestJiggleRepaintDegenerateFallback(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 1, Cols: 80}); err != nil {
		t.Fatalf("Setsize: %v", err)
	}

	fallbackFired := false
	jiggleRepaint(ptmx, func() { fallbackFired = true })
	if !fallbackFired {
		t.Fatalf("expected fallback for a 1-row PTY")
	}
}
