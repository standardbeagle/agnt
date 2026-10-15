package overlay

import (
	"fmt"
	"io"
	"sync"
)

// Drawing the status bar, a notification, or a panel over a live child TUI
// has to put the cursor back where the child left it. The obvious way — CSI s
// / CSI u (SCP/RCP) — is wrong here: that is a *single, terminal-global* save
// slot, and the child TUI uses it too. opencode (opentui), Claude Code, and
// anything on Bubble Tea bracket their own repaints with CSI s ... CSI u, so
// an overlay draw landing between the child's save and its restore overwrites
// the slot. The child then restores to the status bar's position instead of
// its own and paints its next frame from the wrong origin, which reads to the
// user as "the TUI stopped redrawing when I switched panels".
//
// The indicator redraw runs on a 200ms ticker with no knowledge of the child's
// frame boundaries, so this is a race the child always eventually loses.
//
// So we never touch the save slot. The PTY output filter already tracks the
// child's cursor row/column and its DECTCEM visibility from the child's own
// output stream; drawOverChild reads that state and restores it with an absolute
// CUP, leaving the terminal's save slot to the child.
//
// The position has to be read and the draw emitted with no child output in
// between. The filter advances its tracked cursor byte by byte and hands the
// chunk to the terminal only at the end of Write, so a position read during a
// Write (or before it, with the draw emitted after it) is not where the
// terminal's cursor is when the restore arrives; the child's next relative
// output then lands at the wrong place. drawOverChild therefore reads the
// cursor and emits the whole draw while the filter holds its lock.
//
// When no cursor source is registered (unit tests, non-PTY callers) this falls
// back to the historical SCP/RCP pair — correct on its own, since in that case
// there is no child TUI to collide with.

// childCursorSource calls fn with the child PTY's cursor row/column
// (1-indexed) and whether the child currently has the cursor visible, and
// holds the child's output back until fn returns. fn must not draw through
// anything that takes the source's own lock.
type childCursorSource func(fn func(row, col int, visible bool))

// There is exactly one child PTY and one host terminal per process, so the
// cursor authority is process-wide rather than per-renderer: ScreenManager,
// Renderer, and StartupSplash all draw over the same child.
var (
	childCursorMu sync.RWMutex
	childCursorFn childCursorSource
)

// SetChildCursorSource registers the PTY output filter as the authority on
// where the child's cursor is (ProtectedWriter.HoldChildCursor). Wired by the
// run pipeline. Passing nil restores the SCP/RCP fallback.
func SetChildCursorSource(src childCursorSource) {
	childCursorMu.Lock()
	defer childCursorMu.Unlock()
	childCursorFn = src
}

// parkedCursor is the child cursor state captured when a draw starts,
// re-emitted by restore once the overlay has finished drawing.
type parkedCursor struct {
	row, col int
	visible  bool
	tracked  bool // false => fall back to RCP
}

// drawOverChild calls emit with the sequence to write *before* drawing over
// the child and the token that restores the child's cursor afterwards. With a
// cursor source registered, emit runs while the child's output is held back,
// so everything it writes reaches the terminal as one unit relative to the
// child, and the restore targets exactly where the terminal's cursor is.
func drawOverChild(emit func(park string, parked parkedCursor)) {
	childCursorMu.RLock()
	src := childCursorFn
	childCursorMu.RUnlock()

	if src == nil {
		emit(CursorSave+CursorHide, parkedCursor{})
		return
	}
	src(func(row, col int, visible bool) {
		if row < 1 || col < 1 {
			// The filter has not seen enough output to know where the child is.
			emit(CursorSave+CursorHide, parkedCursor{})
			return
		}
		emit(CursorHide, parkedCursor{row: row, col: col, visible: visible, tracked: true})
	})
}

// writeOverChild writes body to w in one Write, bracketed by the park and
// restore sequences (see drawOverChild). body must position the cursor
// absolutely. An empty body writes nothing.
func writeOverChild(w io.Writer, body []byte) {
	if len(body) == 0 {
		return
	}
	drawOverChild(func(park string, parked parkedCursor) {
		restore := parked.restore()
		frame := make([]byte, 0, len(park)+len(body)+len(restore))
		frame = append(frame, park...)
		frame = append(frame, body...)
		frame = append(frame, restore...)
		_, _ = w.Write(frame)
	})
}

// restore returns the sequence that puts the child's cursor back.
func (p parkedCursor) restore() string {
	if !p.tracked {
		return CursorRestore + CursorShow
	}
	seq := fmt.Sprintf(CursorToFormat, p.row, p.col)
	if p.visible {
		return seq + CursorShow
	}
	// The child had the cursor hidden. Forcing it visible leaves a stray block
	// cursor parked in the middle of the child's frame.
	return seq
}
