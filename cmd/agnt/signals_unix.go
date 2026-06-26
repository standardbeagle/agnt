//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/standardbeagle/agnt/internal/debug"
)

// reapSessionPGID terminates the setup child's process group before the coding
// phase spawns, so backgrounded jobs (npm run dev &, etc.) can't hold ports the
// coding child needs. SIGTERM, a brief grace window, then SIGKILL to survivors.
// A non-positive pgid is a no-op (the child had no usable group). The setup
// child has its own session/pgid via creack/pty's Setsid, so the negative-PID
// signal never reaches agnt itself.
func reapSessionPGID(pgid int) {
	if pgid <= 1 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(300 * time.Millisecond)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// watchResize blocks on SIGWINCH and forwards new dimensions to the
// platform-agnostic resize handler. Unix uses signal-driven resize
// (SIGWINCH); the Windows variant in run_windows.go polls because
// Windows has no SIGWINCH equivalent.
func watchResize(ctx context.Context, done <-chan struct{}, handle *ptyHandle, rt *pipelineRuntime) {
	sizeCh := make(chan os.Signal, 1)
	signal.Notify(sizeCh, syscall.SIGWINCH)
	defer signal.Stop(sizeCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-sizeCh:
			w, h, err := term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				continue
			}
			ch := h
			if rt.termOverlay != nil && rt.termOverlay.ShowIndicator() && h > 1 {
				ch = h - 1
			}
			if err := handle.Resize(w, ch); err != nil {
				debug.Warn("run", "error resizing pty: %s", err)
			}
			if rt.termOverlay != nil {
				rt.termOverlay.SetSize(w, h)
			}
			if rt.outputFilter != nil {
				rt.outputFilter.SetSize(w, h)
			}
		}
	}
}

// handleSIGCHLD implements Ctrl+Z support: when the child stops itself
// we must restore the terminal to cooked mode, stop ourselves so the
// shell can take over, and on resume restore raw mode + continue the
// child. Fundamentally Unix-only — Windows has no SIGCHLD/SIGSTOP.
func handleSIGCHLD(ctx context.Context, done <-chan struct{}, c *exec.Cmd, ptmx *os.File, oldState *term.State, rt *pipelineRuntime, width, height *int) {
	sigchldCh := make(chan os.Signal, 1)
	signal.Notify(sigchldCh, syscall.SIGCHLD)
	defer signal.Stop(sigchldCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-sigchldCh:
			if c.Process == nil {
				continue
			}
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(c.Process.Pid, &ws, syscall.WUNTRACED|syscall.WNOHANG, nil)
			if err != nil || pid <= 0 {
				continue
			}
			if !ws.Stopped() {
				continue
			}

			// Restore terminal to cooked mode so shell can use it.
			_ = term.Restore(int(os.Stdin.Fd()), oldState)

			// Stop ourselves — the shell will resume us on `fg`.
			_ = syscall.Kill(syscall.Getpid(), syscall.SIGSTOP)

			// We've been resumed (user ran 'fg').
			_, _ = term.MakeRaw(int(os.Stdin.Fd()))

			if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
				*width, *height = w, h
				ch := h
				if rt.termOverlay != nil && rt.termOverlay.ShowIndicator() && h > 1 {
					ch = h - 1
				}
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(ch), Cols: uint16(w)})
				if rt.termOverlay != nil {
					rt.termOverlay.SetSize(w, h)
				}
				if rt.outputFilter != nil {
					rt.outputFilter.SetSize(w, h)
				}
			}
			_ = syscall.Kill(c.Process.Pid, syscall.SIGCONT)
			if rt.termOverlay != nil && showIndicator {
				if rt.outputFilter != nil {
					rt.outputFilter.EnforceScrollRegion()
				}
				rt.termOverlay.Redraw()
			}
		}
	}
}

// jiggleRepaint forces a child TUI to repaint by momentarily shrinking the
// PTY one row, then restoring it. Each TIOCSWINSZ raises SIGWINCH with a
// genuine size delta, so even renderers that ignore a same-size SIGWINCH
// (Claude Code, opencode) repaint — unlike a bare SIGWINCH, which they dedupe
// against the unchanged winsize and skip. Used on overlay menu close while the
// child is in the alternate screen buffer (the overlay drew panels into that
// buffer, so the child must redraw). The original size is always restored;
// when the current size can't be read, it falls back to the supplied signal.
func jiggleRepaint(ptmx *os.File, fallback func()) {
	ws, err := pty.GetsizeFull(ptmx)
	if err != nil || ws == nil || ws.Rows <= 1 {
		if fallback != nil {
			fallback()
		}
		return
	}
	shrunk := *ws
	shrunk.Rows = ws.Rows - 1
	_ = pty.Setsize(ptmx, &shrunk)
	time.Sleep(15 * time.Millisecond)
	_ = pty.Setsize(ptmx, ws)
}
