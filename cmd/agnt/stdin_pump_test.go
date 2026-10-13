package main

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/overlay"
)

// chunkSource is a blocking io.Reader fed chunk by chunk from the test, like a
// terminal: Read parks until a chunk is sent.
type chunkSource struct {
	ch        chan []byte
	closeOnce sync.Once
}

// newChunkSource returns a source that reports EOF at test cleanup, so the
// pump goroutine reading it ends with the test (the package runs goleak).
func newChunkSource(t *testing.T) *chunkSource {
	c := &chunkSource{ch: make(chan []byte)}
	t.Cleanup(c.close)
	return c
}

// close ends the stream; Read then returns io.EOF.
func (c *chunkSource) close() { c.closeOnce.Do(func() { close(c.ch) }) }

func (c *chunkSource) Read(p []byte) (int, error) {
	b, ok := <-c.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(p, b), nil
}

// send delivers one chunk and returns once the pump's Read has taken it.
func (c *chunkSource) send(s string) { c.ch <- []byte(s) }

const pumpTestTimeout = 10 * time.Second

func readWithin(t *testing.T, r io.Reader, n int) (string, error) {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	done := make(chan res, 1)
	go func() {
		buf := make([]byte, n)
		got, err := r.Read(buf)
		done <- res{string(buf[:got]), err}
	}()
	select {
	case r := <-done:
		return r.s, r.err
	case <-time.After(pumpTestTimeout):
		t.Fatal("Read did not return")
		return "", nil
	}
}

// TestStdinLeaseCloseUnblocksRead is the regression for `agnt run
// --no-overlay` hanging after the agent exits: teardown joined a goroutine
// parked in a stdin Read that nothing could end.
func TestStdinLeaseCloseUnblocksRead(t *testing.T) {
	src := newChunkSource(t)
	pump := newStdinPump(src)
	lease := pump.Acquire()

	errCh := make(chan error, 1)
	go func() {
		_, err := lease.Read(make([]byte, 16))
		errCh <- err
	}()

	// No input is ever sent: only Close can end the Read.
	_ = lease.Close()
	select {
	case err := <-errCh:
		if err != io.EOF {
			t.Fatalf("Read after Close = %v, want io.EOF", err)
		}
	case <-time.After(pumpTestTimeout):
		t.Fatal("Close did not unblock a pending Read")
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

// TestStdinPumpSecondPhaseGetsEveryKeystroke is the regression for the
// first-run setup → relaunch flow: phase 1's reader outlived its router and
// took the first keystrokes of phase 2. With the pump, a closed lease takes
// nothing, and input typed between phases is delivered to the next lease.
// Input arrives in separate chunks, across the phase boundary.
func TestStdinPumpSecondPhaseGetsEveryKeystroke(t *testing.T) {
	src := newChunkSource(t)
	pump := newStdinPump(src)

	phase1 := pump.Acquire()
	src.send("a")
	if got, err := readWithin(t, phase1, 16); err != nil || got != "a" {
		t.Fatalf("phase 1 read = %q, %v", got, err)
	}
	_ = phase1.Close()

	// Typed while no phase is running.
	src.send("b")
	src.send("c")

	phase2 := pump.Acquire()
	src.send("d")

	var got strings.Builder
	for got.Len() < 3 {
		s, err := readWithin(t, phase2, 16)
		if err != nil {
			t.Fatalf("phase 2 read: %v (have %q)", err, got.String())
		}
		got.WriteString(s)
	}
	if got.String() != "bcd" {
		t.Fatalf("phase 2 received %q, want %q — keystrokes were lost or reordered", got.String(), "bcd")
	}

	// The dead phase-1 lease must not have been able to take any of it.
	if _, err := phase1.Read(make([]byte, 16)); err != io.EOF {
		t.Fatalf("closed lease Read = %v, want io.EOF", err)
	}
}

// TestStdinPumpNewLeaseSupersedesOld: an unclosed earlier lease stops
// receiving the moment a new one is acquired — one owner at a time.
func TestStdinPumpNewLeaseSupersedesOld(t *testing.T) {
	src := newChunkSource(t)
	pump := newStdinPump(src)
	old := pump.Acquire()

	errCh := make(chan error, 1)
	go func() {
		_, err := old.Read(make([]byte, 16))
		errCh <- err
	}()

	fresh := pump.Acquire()
	select {
	case err := <-errCh:
		if err != io.EOF {
			t.Fatalf("superseded Read = %v, want io.EOF", err)
		}
	case <-time.After(pumpTestTimeout):
		t.Fatal("acquiring a new lease did not end the old one's Read")
	}

	src.send("x")
	if got, err := readWithin(t, fresh, 16); err != nil || got != "x" {
		t.Fatalf("new lease read = %q, %v", got, err)
	}
}

// TestStdinPumpSourceEOF: buffered bytes are delivered before the stream's
// terminal error.
func TestStdinPumpSourceEOF(t *testing.T) {
	src := newChunkSource(t)
	pump := newStdinPump(src)
	lease := pump.Acquire()
	src.send("tail")
	src.close()

	if got, err := readWithin(t, lease, 16); err != nil || got != "tail" {
		t.Fatalf("read = %q, %v, want buffered bytes first", got, err)
	}
	if _, err := readWithin(t, lease, 16); err != io.EOF {
		t.Fatalf("read after drain = %v, want io.EOF", err)
	}
}

// TestRawStdinRelayModalCapture is the regression for the port-conflict
// prompt under --no-overlay reading os.Stdin while the stdin copy was also
// reading it: the answer could land in the agent. The relay is the single
// reader; during a modal capture bytes go to the prompt and never to the PTY.
func TestRawStdinRelayModalCapture(t *testing.T) {
	src := newChunkSource(t)
	pump := newStdinPump(src)
	lease := pump.Acquire()
	pty := &lockedBuffer{}
	relay := newRawStdinRelay(lease, pty)

	done := make(chan struct{})
	go func() { relay.Run(); close(done) }()

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(pumpTestTimeout)
		for pty.String() != want {
			if time.Now().After(deadline) {
				t.Fatalf("PTY received %q, want %q", pty.String(), want)
			}
			time.Sleep(time.Millisecond)
		}
	}

	src.send("ls")
	waitFor("ls")

	modal := relay.BeginModalCapture()
	src.send("n")
	select {
	case b := <-modal:
		if b != 'n' {
			t.Fatalf("modal got %q, want 'n'", b)
		}
	case <-time.After(pumpTestTimeout):
		t.Fatal("modal capture never received the keystroke")
	}
	relay.EndModalCapture()

	src.send("!")
	waitFor("ls!") // the 'n' never reached the child

	// Closing the lease ends the relay without any further input.
	_ = lease.Close()
	select {
	case <-done:
	case <-time.After(pumpTestTimeout):
		t.Fatal("relay did not stop when its lease closed")
	}
}

// recordingNotifier collects notifications.
type recordingNotifier struct {
	mu   sync.Mutex
	msgs []overlay.Notification
}

func (r *recordingNotifier) Notify(n overlay.Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, n)
}

// fakeModal is a stdinModal whose channel the test controls.
type fakeModal struct {
	ch    chan byte
	began bool
	ended bool
}

func (f *fakeModal) BeginModalCapture() <-chan byte { f.began = true; return f.ch }
func (f *fakeModal) EndModalCapture()               { f.ended = true }

func conflictHandle() *daemonSessionHandle {
	h := &daemonSessionHandle{registrationDone: make(chan struct{}), heartbeatStop: make(chan struct{})}
	h.portConflicts = []portConflictInfo{{ScriptName: "dev", Port: 3000, PIDs: []int{42}, ProcessName: "node"}}
	close(h.registrationDone)
	return h
}

// TestPortConflictPromptDeclinesWithoutAnAnswer pins the timeout-means-decline
// rule on every path now that the prompt has a single way to read input:
// killing another process must never happen unattended.
func TestPortConflictPromptDeclinesWithoutAnAnswer(t *testing.T) {
	const declined = "proceeding without killing"

	t.Run("no stdin owner", func(t *testing.T) {
		var out bytes.Buffer
		displayAutostartResults(conflictHandle(), &recordingNotifier{}, nil, &out, time.Second)
		if !strings.Contains(out.String(), declined) {
			t.Fatalf("output %q: with no way to ask, the prompt must decline", out.String())
		}
	})

	t.Run("timeout", func(t *testing.T) {
		var out bytes.Buffer
		modal := &fakeModal{ch: make(chan byte)} // never answers
		displayAutostartResults(conflictHandle(), &recordingNotifier{}, modal, &out, time.Millisecond)
		if !strings.Contains(out.String(), declined) {
			t.Fatalf("output %q: a timeout must decline", out.String())
		}
		if !modal.began || !modal.ended {
			t.Fatalf("modal capture began=%v ended=%v, want both", modal.began, modal.ended)
		}
	})

	t.Run("explicit n", func(t *testing.T) {
		var out bytes.Buffer
		modal := &fakeModal{ch: make(chan byte, 1)}
		modal.ch <- 'n'
		displayAutostartResults(conflictHandle(), &recordingNotifier{}, modal, &out, time.Hour)
		if !strings.Contains(out.String(), declined) {
			t.Fatalf("output %q: 'n' must decline", out.String())
		}
	})

	t.Run("enter accepts through the modal channel", func(t *testing.T) {
		var out bytes.Buffer
		modal := &fakeModal{ch: make(chan byte, 1)}
		modal.ch <- '\r'
		displayAutostartResults(conflictHandle(), &recordingNotifier{}, modal, &out, time.Hour)
		if strings.Contains(out.String(), declined) {
			t.Fatalf("output %q: Enter is the [Y] default and must not decline", out.String())
		}
	})
}
