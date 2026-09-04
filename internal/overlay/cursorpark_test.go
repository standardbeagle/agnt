package overlay

import (
	"bytes"
	"strings"
	"testing"
)

// The child TUI (opencode/opentui, Claude Code, Bubble Tea) brackets its own
// repaints with CSI s / CSI u. That is a single terminal-global save slot, so
// an overlay draw that also uses it makes the child restore to the status
// bar's position and paint its next frame from the wrong origin — the "TUI
// stops redrawing when switching panels" report.
//
// Deleting the parkCursor wiring makes every assertion below fail, so these
// are behavioural, not tautological: they name the exact bytes that must and
// must not reach the terminal.

func newTrackedFilter(t *testing.T, out *bytes.Buffer, w, h int) *ProtectedWriter {
	t.Helper()
	pw := NewProtectedWriter(out, w, h, FilterConfig{ProtectBottomRows: 1})
	t.Cleanup(func() { SetChildCursorSource(nil) })
	SetChildCursorSource(pw.ChildCursor)
	return pw
}

func TestDrawIndicatorNeverTouchesTheSharedCursorSaveSlot(t *testing.T) {
	var child bytes.Buffer
	pw := newTrackedFilter(t, &child, 100, 30)

	// Child paints and parks its cursor at row 7, col 12.
	if _, err := pw.Write([]byte("\x1b[7;12H")); err != nil {
		t.Fatalf("filter write: %v", err)
	}

	var term bytes.Buffer
	r := NewRenderer(&term, 100, 30)
	r.DrawIndicator(Status{})

	got := term.String()
	if strings.Contains(got, CursorSave) {
		t.Errorf("indicator emitted CSI s; it must not consume the save slot the child TUI uses.\ngot: %q", got)
	}
	if strings.Contains(got, CursorRestore) {
		t.Errorf("indicator emitted CSI u; it must restore by absolute CUP.\ngot: %q", got)
	}
	if want := "\x1b[7;12H"; !strings.Contains(got, want) {
		t.Errorf("indicator did not restore the child's cursor to %q.\ngot: %q", want, got)
	}
}

func TestDrawIndicatorKeepsTheChildsCursorHidden(t *testing.T) {
	var child bytes.Buffer
	pw := newTrackedFilter(t, &child, 100, 30)

	// A full-screen TUI hides the cursor for the duration of a frame.
	if _, err := pw.Write([]byte("\x1b[?25l\x1b[7;12H")); err != nil {
		t.Fatalf("filter write: %v", err)
	}
	if _, _, visible := pw.ChildCursor(); visible {
		t.Fatal("premise broken: filter did not track DECTCEM reset, the hidden-cursor path is untested")
	}

	var term bytes.Buffer
	r := NewRenderer(&term, 100, 30)
	r.DrawIndicator(Status{})

	if got := term.String(); strings.Contains(got, CursorShow) {
		t.Errorf("indicator forced the cursor visible over a child that hid it.\ngot: %q", got)
	}
}

func TestDrawIndicatorShowsCursorWhenChildHasItVisible(t *testing.T) {
	var child bytes.Buffer
	pw := newTrackedFilter(t, &child, 100, 30)

	if _, err := pw.Write([]byte("\x1b[?25l\x1b[?25h\x1b[3;4H")); err != nil {
		t.Fatalf("filter write: %v", err)
	}

	var term bytes.Buffer
	r := NewRenderer(&term, 100, 30)
	r.DrawIndicator(Status{})

	got := term.String()
	if !strings.Contains(got, "\x1b[3;4H"+CursorShow) {
		t.Errorf("indicator did not restore position+visibility for a child with a visible cursor.\ngot: %q", got)
	}
}

// Without a registered source there is no child TUI to collide with, so the
// historical SCP/RCP pair stays correct — and stays in use.
func TestDrawIndicatorFallsBackToSaveRestoreWithoutACursorSource(t *testing.T) {
	SetChildCursorSource(nil)

	var term bytes.Buffer
	r := NewRenderer(&term, 100, 30)
	r.DrawIndicator(Status{})

	got := term.String()
	if !strings.Contains(got, CursorSave) || !strings.Contains(got, CursorRestore) {
		t.Errorf("expected SCP/RCP fallback with no cursor source.\ngot: %q", got)
	}
}

func TestEnforceScrollRegionRepositionsWithoutDECSC(t *testing.T) {
	var out bytes.Buffer
	pw := NewProtectedWriter(&out, 100, 30, FilterConfig{ProtectBottomRows: 1})
	if _, err := pw.Write([]byte("\x1b[9;5H")); err != nil {
		t.Fatalf("filter write: %v", err)
	}
	out.Reset()

	pw.EnforceScrollRegion()

	got := out.String()
	if strings.Contains(got, "\x1b7") || strings.Contains(got, "\x1b8") {
		t.Errorf("scroll-region enforcement used DECSC/DECRC, which shares the child's save slot.\ngot: %q", got)
	}
	if want := "\x1b[1;29r\x1b[9;5H"; got != want {
		t.Errorf("scroll region + reposition = %q, want %q", got, want)
	}
}
