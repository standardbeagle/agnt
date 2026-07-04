//go:build linux

// Termios comparison here uses unix.TCGETS directly (the Linux ioctl
// constant; BSD/darwin use TIOCGETA instead), so this file is linux-only —
// consistent with CI running on Linux. The functions under test
// (rawTerminal, signalRestoreWatcher, panicSafeRestore) are themselves
// platform-generic (attach_unix.go is `!windows`), only this test's
// termios-flag assertion is Linux-specific.

package main

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const ioctlGetTermios = unix.TCGETS

// openTestPTY opens a real pty pair so raw-mode/termios operations have a
// genuine tty fd to act on (term.MakeRaw/IsTerminal fail on a plain pipe).
func openTestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	return master, slave
}

// termiosEqual compares the ICANON/ECHO-relevant local-mode flags. Raw mode
// clears ICANON and ECHO among others; this is sufficient to distinguish
// "raw" from "restored to original" without depending on every flag.
func localModeFlags(t *testing.T, fd int) uint32 {
	t.Helper()
	tios, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		t.Fatalf("IoctlGetTermios: %v", err)
	}
	return tios.Lflag
}

func TestRawTerminal_PutsIntoRawModeAndRestores(t *testing.T) {
	// No t.Parallel(): shares no process-global state that would race, but
	// keeping consistent with the rest of this package's real-fd tests.
	_, slave := openTestPTY(t)
	fd := int(slave.Fd())

	if !term.IsTerminal(fd) {
		t.Fatalf("pty slave should report as a terminal")
	}

	before := localModeFlags(t, fd)

	restore, err := rawTerminal(fd)
	if err != nil {
		t.Fatalf("rawTerminal: %v", err)
	}

	during := localModeFlags(t, fd)
	if during == before {
		t.Fatalf("expected local mode flags to change after entering raw mode")
	}
	if during&unix.ICANON != 0 {
		t.Fatalf("ICANON should be cleared in raw mode")
	}

	restore()

	after := localModeFlags(t, fd)
	if after != before {
		t.Fatalf("termios not restored: before=%#x after=%#x", before, after)
	}
}

func TestRawTerminal_RestoreIsIdempotent(t *testing.T) {
	_, slave := openTestPTY(t)
	fd := int(slave.Fd())

	before := localModeFlags(t, fd)
	restore, err := rawTerminal(fd)
	if err != nil {
		t.Fatalf("rawTerminal: %v", err)
	}

	restore()
	restore() // must not panic or re-corrupt state on a second call
	restore()

	after := localModeFlags(t, fd)
	if after != before {
		t.Fatalf("termios not restored after repeated restore() calls: before=%#x after=%#x", before, after)
	}
}

func TestSignalRestoreWatcher_RestoresOnSignal(t *testing.T) {
	restored := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)

	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})

	go signalRestoreWatcher(sigCh, done,
		func() { restored <- struct{}{} },
		func() { canceled <- struct{}{} },
	)

	sigCh <- os.Interrupt

	select {
	case <-restored:
	case <-time.After(2 * time.Second):
		t.Fatalf("restore() was not called after a signal")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatalf("cancel() was not called after restore()")
	}
}

func TestSignalRestoreWatcher_DoneWithoutSignal_NeverRestores(t *testing.T) {
	restoreCalled := false
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	close(done)

	signalRestoreWatcher(sigCh, done, func() { restoreCalled = true }, func() {})

	if restoreCalled {
		t.Fatalf("restore() must not fire when the relay finished normally (done closed, no signal)")
	}
}

func TestPanicSafeRestore_RestoresBeforeRePanicking(t *testing.T) {
	restored := false
	restore := func() { restored = true }

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected the panic to propagate")
		}
		if !restored {
			t.Fatalf("restore() must run before the panic re-raises")
		}
	}()

	panicSafeRestore(restore, func() {
		panic(errors.New("boom"))
	})
}

func TestPanicSafeRestore_NoPanic_DoesNotRestore(t *testing.T) {
	restoreCalled := false
	panicSafeRestore(func() { restoreCalled = true }, func() {})
	if restoreCalled {
		t.Fatalf("restore() must only run on panic, not on a clean return (the caller's own defer restore() owns the clean-exit path)")
	}
}
