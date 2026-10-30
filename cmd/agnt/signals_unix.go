//go:build unix

package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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

// childExitGrace is how long each step of awaitChildExit's escalation waits
// for the child to go before trying something stronger.
const childExitGrace = 2 * time.Second

// outputDrainGrace is how long the session waits, after the agent itself
// exits, for the PTY output to end on its own.
const outputDrainGrace = 500 * time.Millisecond

// awaitChildExit blocks until the session is over: the PTY output stream ends
// (done), which is how a normal session finishes, or the agent process exits
// (childExited) and the output does not end within outputDrainGrace.
//
// The output ends only when every holder of the PTY slave has closed it, and
// anything the agent started inherits the slave: an MCP server, a hook, a
// backgrounded `npm run dev &`. One of those outliving the agent (Ctrl+C out
// of Claude Code) used to keep agnt waiting forever. The agent's exit ends
// the session, so the master is closed to end the output copy (which works
// only because startPollablePTY keeps the master in the poller), and the
// caller's reapSessionPGID stops what the agent left in its group.
//
// If ctx is cancelled first (SIGINT/SIGTERM to agnt), the shared pipeline
// has already forwarded one SIGINT to the child's process group; an agent
// that ignores or merely debounces that signal would otherwise leave agnt
// unkillable short of SIGKILL, since further signals to agnt are absorbed by
// the already-cancelled context. So escalate, one grace period per step:
// SIGTERM to the group, SIGKILL to the group, and finally close the PTY
// master so the output copy ends even when something outside the group still
// holds the slave open. Mirrors the Windows path, which kills and waits 2s.
// Kept here (not run.go) for that file's line budget.
func awaitChildExit(ctx context.Context, done, childExited <-chan struct{}, c *exec.Cmd, ptmx *os.File, grace time.Duration) {
	select {
	case <-done:
		return
	case <-childExited:
		select {
		case <-done:
			return
		case <-time.After(outputDrainGrace):
		}
		debug.Log("run", "agent exited but the PTY is still held open; closing it and reaping the session group")
		_ = ptmx.Close()
		<-done
		return
	case <-ctx.Done():
	}
	exited := func() bool {
		select {
		case <-done:
			return true
		case <-time.After(grace):
			return false
		}
	}
	if exited() {
		return
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		signalChildGroup(c, sig)
		if exited() {
			return
		}
	}
	_ = ptmx.Close()
	<-done
}

// awaitSessionEnd waits out the PTY session, stops the overlay pipeline and
// returns the child's exit status. The child's own exit ends the session too
// (awaitChildExit), so it is waited for alongside the output stream rather
// than after it. Kept here for run.go's line budget.
func awaitSessionEnd(ctx context.Context, rt *pipelineRuntime, c *exec.Cmd, ptmx *os.File) error {
	childExited := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = c.Wait()
		close(childExited)
	}()
	awaitChildExit(ctx, rt.Done(), childExited, c, ptmx, childExitGrace)
	rt.Stop()
	<-childExited
	return waitErr
}

// signalChildGroup sends sig to the PTY child's process group, falling back
// to the child alone when the group cannot be resolved.
func signalChildGroup(c *exec.Cmd, sig syscall.Signal) {
	if c == nil || c.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(c.Process.Pid); err == nil && pgid > 1 {
		_ = syscall.Kill(-pgid, sig)
		return
	}
	_ = c.Process.Signal(sig)
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
			if am := rt.activityMon.Load(); am != nil {
				// Keep the preview's virtual screen matched to the child PTY.
				am.Resize(w, ch)
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
			// childStopped is a non-destructive peek (WNOWAIT): it leaves the
			// child in a waitable state so the exit path's c.Wait() (run.go)
			// can still reap it. A reaping wait here would collect a normal
			// child exit, then c.Wait() returns "no child processes" and
			// classifyChildExit misreports a clean exit as an unexpected
			// crash. We only need to observe the *stopped* state here
			// (Ctrl+Z); the exited state is left for the main path. The peek
			// is per-OS because Linux's wait4 rejects WNOWAIT outright.
			if !childStopped(c.Process.Pid) {
				continue
			}

			// Leave the tty sane for the shell: clear any DEC private
			// modes the stopped child left enabled (it cannot clean up
			// itself while stopped), then restore cooked termios.
			_, _ = os.Stdout.WriteString(terminalSaneReset)
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
				_ = setPTYSize(ptmx, &unix.Winsize{Row: uint16(ch), Col: uint16(w)})
				if rt.termOverlay != nil {
					rt.termOverlay.SetSize(w, h)
				}
				if rt.outputFilter != nil {
					rt.outputFilter.SetSize(w, h)
				}
			}
			_ = syscall.Kill(c.Process.Pid, syscall.SIGCONT)
			// Force a repaint after the continue: a size-jiggle raises
			// SIGWINCH with a genuine delta, so the child redraws and
			// re-establishes its own DEC modes (mouse, bracketed paste,
			// alt screen) that terminalSaneReset cleared on suspend.
			// Without this, ENTER lands as a bare newline because the
			// child's input modes were never re-armed.
			jiggleRepaint(ptmx, func() {
				_ = c.Process.Signal(syscall.SIGWINCH)
			})
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
	ws, err := getPTYSize(ptmx)
	if err != nil || ws == nil || ws.Row <= 1 {
		if fallback != nil {
			fallback()
		}
		return
	}
	shrunk := *ws
	shrunk.Row = ws.Row - 1
	_ = setPTYSize(ptmx, &shrunk)
	time.Sleep(15 * time.Millisecond)
	_ = setPTYSize(ptmx, ws)
}
