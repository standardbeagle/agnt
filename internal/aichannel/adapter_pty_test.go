package aichannel

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePTYSession is a ptySession whose output stream and exit are scripted, so
// the drain barrier can be tested without a real terminal.
type fakePTYSession struct {
	r         *io.PipeReader
	w         *io.PipeWriter
	exited    chan struct{}
	closeOnce sync.Once
	closed    chan struct{}
	// ignoreClose makes Close leave the reader blocked, modelling a backend
	// whose Close fails to end a pending Read.
	ignoreClose bool
}

func newFakePTYSession() *fakePTYSession {
	r, w := io.Pipe()
	return &fakePTYSession{r: r, w: w, exited: make(chan struct{}), closed: make(chan struct{})}
}

func (f *fakePTYSession) Read(p []byte) (int, error)  { return f.r.Read(p) }
func (f *fakePTYSession) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakePTYSession) Wait() error                 { <-f.exited; return nil }
func (f *fakePTYSession) Close() error {
	f.closeOnce.Do(func() {
		close(f.closed)
		if !f.ignoreClose {
			_ = f.r.Close()
		}
	})
	return nil
}

func shortPTYDrain(t *testing.T, grace, ceiling time.Duration) {
	t.Helper()
	prevGrace, prevCeiling := ptyDrainGrace, ptyDrainCeiling
	ptyDrainGrace, ptyDrainCeiling = grace, ceiling
	t.Cleanup(func() { ptyDrainGrace, ptyDrainCeiling = prevGrace, prevCeiling })
}

// TestCollectPTYOutputWaitsForLateOutput is the regression for the fixed
// 100ms wait: output that is still arriving when the command exits used to be
// cut off (and the buffer read while the copier was writing it). The writer
// here is released only after the command has "exited", and the grace is set
// far beyond the test's own timeout, so the result can only be complete if
// collection waited for end-of-stream rather than for a clock.
func TestCollectPTYOutputWaitsForLateOutput(t *testing.T) {
	shortPTYDrain(t, time.Hour, time.Hour)
	sess := newFakePTYSession()

	go func() {
		_, _ = sess.w.Write([]byte("first "))
		close(sess.exited) // the command exits…
		_, _ = sess.w.Write([]byte("late tail"))
		_ = sess.w.Close() // …and only then does the stream end
	}()

	out, waitErr, err := collectPTYOutput(sess)
	if err != nil || waitErr != nil {
		t.Fatalf("collectPTYOutput: err=%v waitErr=%v", err, waitErr)
	}
	if out != "first late tail" {
		t.Fatalf("output = %q, want the full stream including the late tail", out)
	}
}

// TestCollectPTYOutputClosesAStuckStream: when the stream does not end by
// itself after exit (ConPTY; a descendant holding the slave open), the
// terminal is closed to end it and what was read is returned.
func TestCollectPTYOutputClosesAStuckStream(t *testing.T) {
	shortPTYDrain(t, 10*time.Millisecond, time.Hour)
	sess := newFakePTYSession()

	wrote := make(chan struct{})
	go func() {
		_, _ = sess.w.Write([]byte("partial"))
		close(wrote)
		close(sess.exited) // exits, but the write end is never closed
	}()

	out, _, err := collectPTYOutput(sess)
	if err != nil {
		t.Fatalf("collectPTYOutput: %v", err)
	}
	<-wrote
	select {
	case <-sess.closed:
	default:
		t.Fatal("stuck stream was not closed")
	}
	if out != "partial" {
		t.Fatalf("output = %q, want %q", out, "partial")
	}
}

// TestCollectPTYOutputNeverReadsUnderALiveReader: if even closing the terminal
// does not end the reader, the buffer is still being written and must not be
// read — the call fails instead.
func TestCollectPTYOutputNeverReadsUnderALiveReader(t *testing.T) {
	shortPTYDrain(t, 10*time.Millisecond, 20*time.Millisecond)
	sess := newFakePTYSession()
	sess.ignoreClose = true
	t.Cleanup(func() { _ = sess.w.Close() }) // let the copier goroutine go
	close(sess.exited)

	out, _, err := collectPTYOutput(sess)
	if err == nil {
		t.Fatalf("collectPTYOutput returned %q with a reader still running, want an error", out)
	}
	if !strings.Contains(err.Error(), "did not drain") {
		t.Fatalf("err = %v, want a drain failure", err)
	}
}

// TestStartPTYCommandRunsOnThisPlatform runs a real command through the
// platform PTY backend. On Windows this is the regression for creack/pty's
// ErrUnsupported stub: the backend is ConPTY there. Starts a real process, so
// no t.Parallel().
func TestStartPTYCommandRunsOnThisPlatform(t *testing.T) {
	const marker = "agnt-pty-ok"
	var cmd *exec.Cmd
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", "echo "+marker)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", "echo "+marker)
	}
	cmd.Dir = t.TempDir()

	sess, err := startPTYCommand(ctx, cmd)
	if err != nil {
		t.Fatalf("startPTYCommand: %v", err)
	}
	defer sess.Close()

	out, waitErr, err := collectPTYOutput(sess)
	if err != nil || waitErr != nil {
		t.Fatalf("collectPTYOutput: err=%v waitErr=%v", err, waitErr)
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("output %q does not contain %q", out, marker)
	}
}
