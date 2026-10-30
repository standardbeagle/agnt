//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

// TestChildStoppedObservesStopWithoutReaping pins the Ctrl+Z peek. The
// original wait4+WNOWAIT form returned EINVAL on Linux on every call, so the
// suspend path never ran; nothing exercised it. Starts a real process, so no
// t.Parallel().
func TestChildStoppedObservesStopWithoutReaping(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()

	if childStopped(pid) {
		t.Fatal("running child reported as stopped")
	}

	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !childStopped(pid) {
		if time.Now().After(deadline) {
			t.Fatal("stopped child never reported as stopped")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The peek must not consume the stop report either: a second look still
	// sees it.
	if !childStopped(pid) {
		t.Fatal("stop state was consumed by the first peek")
	}

	// Non-reaping: the exit path's Wait must still collect the real status.
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait() = %v, want *exec.ExitError (child was reaped by the peek?)", err)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("Wait() status = %v, want killed by SIGKILL", exitErr)
	}
}

// TestAwaitChildExitEscalatesPastIgnoredSignals: a child that ignores SIGINT
// and SIGTERM used to leave `agnt run` unkillable after a cancel — one SIGINT
// was forwarded and then agnt waited forever. The wait now escalates to
// SIGKILL. Uses a real PTY and process, so no t.Parallel().
func TestAwaitChildExitEscalatesPastIgnoredSignals(t *testing.T) {
	// Ignored dispositions are inherited across exec, so the sleep children
	// ignore INT/TERM too: only SIGKILL ends this group.
	c := exec.Command("sh", "-c", `trap "" INT TERM; while :; do sleep 1; done`)
	ptmx, err := pty.Start(c)
	if err != nil {
		t.Skipf("cannot start a PTY child: %v", err)
	}
	defer ptmx.Close()
	defer func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }()

	// done mirrors the pipeline's rt.Done(): closed when PTY output ends.
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, ptmx)
		close(done)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // agnt received SIGINT/SIGTERM

	returned := make(chan struct{})
	go func() {
		awaitChildExit(ctx, done, nil, c, ptmx, 100*time.Millisecond)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(30 * time.Second):
		t.Fatal("awaitChildExit did not return for a child ignoring SIGINT and SIGTERM")
	}

	err = c.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait() = %v, want the child killed", err)
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("child status = %v, want killed by SIGKILL (escalation skipped a step?)", exitErr)
	}
}

// TestAwaitChildExitReturnsOnNormalExit: without a cancel, the wait simply
// follows the output stream and sends nothing.
func TestAwaitChildExitReturnsOnNormalExit(t *testing.T) {
	done := make(chan struct{})
	close(done)
	awaitChildExit(context.Background(), done, nil, nil, nil, time.Hour)
}

// TestAwaitChildExitEndsWhenAJobOutlivesTheAgent: the agent exits while a
// job it backgrounded still holds the PTY slave, so the output stream never
// ends. That is Ctrl+C out of Claude Code with an MCP server or dev server
// still running, and agnt used to wait forever. The agent's exit must end the
// session, and reapSessionPGID must leave nothing of its group alive. Real
// PTY and processes, so no t.Parallel().
func TestAwaitChildExitEndsWhenAJobOutlivesTheAgent(t *testing.T) {
	// The job ignores SIGHUP (as nohup and many Node servers do): a plain
	// job dies of the hangup when the session leader exits, ends the output
	// by itself, and the test would pass against the old wait.
	// The ignore is set before the fork so the job inherits it: a trap set
	// inside the job races the leader's exit and its hangup.
	c := exec.Command("sh", "-c", `trap "" HUP; sleep 600 & echo $!; exit 0`)
	ptmx, err := startPollablePTY(c)
	if err != nil {
		t.Skipf("cannot start a PTY child: %v", err)
	}
	defer ptmx.Close()
	pgid := c.Process.Pid
	defer func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) }()

	var out strings.Builder
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, ptmx)
		close(done)
	}()
	childExited := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(childExited)
	}()

	returned := make(chan struct{})
	go func() {
		awaitChildExit(context.Background(), done, childExited, c, ptmx, time.Hour)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(30 * time.Second):
		t.Fatal("awaitChildExit did not return after the agent exited with a job still holding the PTY")
	}
	select {
	case <-childExited:
	default:
		t.Fatal("awaitChildExit returned before the agent exited")
	}

	jobPID, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0]))
	if err != nil {
		t.Fatalf("job pid not echoed: %q", out.String())
	}
	if err := syscall.Kill(jobPID, 0); err != nil {
		t.Fatalf("job %d already gone before the reap (%v): the test proves nothing", jobPID, err)
	}

	reapSessionPGID(pgid)
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	}, 10*time.Second, 10*time.Millisecond, "session group %d still has members after reapSessionPGID", pgid)
}

// TestChildStoppedLeavesExitedChildForWait covers the other half of the
// contract: a child that exited is not reported as stopped and is not reaped,
// so c.Wait() reports the clean exit rather than "no child processes".
func TestChildStoppedLeavesExitedChildForWait(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start true: %v", err)
	}
	// Give the child time to exit and become a zombie.
	time.Sleep(200 * time.Millisecond)
	if childStopped(cmd.Process.Pid) {
		t.Fatal("exited child reported as stopped")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want nil (peek must not reap an exited child)", err)
	}
}
