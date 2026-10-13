//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
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
		awaitChildExit(ctx, done, c, ptmx, 100*time.Millisecond)
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
	awaitChildExit(context.Background(), done, nil, nil, time.Hour)
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
