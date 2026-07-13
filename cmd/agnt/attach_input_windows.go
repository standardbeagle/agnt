//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

var cancelSynchronousIo = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

// windowsAttachInput owns a CONIN$ handle distinct from the process standard
// handle. Reads run on a locked OS thread, allowing operation-scoped
// CancelSynchronousIo without touching unrelated stdin users.
type windowsAttachInput struct {
	mu              sync.Mutex
	handle          windows.Handle
	thread          windows.Handle
	readDone        chan struct{}
	closed          bool
	cancelRequested bool
}

func openWindowsAttachInput() (*windowsAttachInput, error) {
	if err := cancelSynchronousIo.Find(); err != nil {
		return nil, fmt.Errorf("load CancelSynchronousIo: %w", err)
	}
	h, err := windows.CreateFile(windows.StringToUTF16Ptr("CONIN$"), windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("open owned CONIN$ input: %w", err)
	}
	return &windowsAttachInput{handle: h}, nil
}

func (in *windowsAttachInput) Read(p []byte) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	thread, err := windows.OpenThread(0x0001, false, windows.GetCurrentThreadId()) // THREAD_TERMINATE
	if err != nil {
		return 0, fmt.Errorf("open console reader thread: %w", err)
	}
	in.mu.Lock()
	if in.closed {
		in.mu.Unlock()
		windows.CloseHandle(thread)
		return 0, io.EOF
	}
	in.thread = thread
	done := make(chan struct{})
	in.readDone = done
	if in.cancelRequested {
		in.thread = 0
		in.readDone = nil
		close(done)
		in.mu.Unlock()
		windows.CloseHandle(thread)
		return 0, io.EOF
	}
	h := in.handle
	in.mu.Unlock()
	var n uint32
	err = windows.ReadFile(h, p, &n, nil)
	in.mu.Lock()
	in.thread = 0
	in.readDone = nil
	close(done)
	in.mu.Unlock()
	windows.CloseHandle(thread)
	if errors.Is(err, windows.ERROR_OPERATION_ABORTED) || errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return int(n), io.EOF
	}
	return int(n), err
}

func (in *windowsAttachInput) Interrupt() error {
	// ERROR_NOT_FOUND is ambiguous: the read may have completed, or the
	// reader may have published its thread just before entering ReadFile.
	// Retry against the same dedicated thread while observing read completion.
	in.mu.Lock()
	in.cancelRequested = true
	in.mu.Unlock()
	var firstUnexpected error
	for {
		in.mu.Lock()
		thread, done := in.thread, in.readDone
		if thread == 0 {
			in.mu.Unlock()
			return firstUnexpected
		}
		r1, _, callErr := cancelSynchronousIo.Call(uintptr(thread))
		in.mu.Unlock()
		if r1 != 0 {
			return firstUnexpected
		}
		if !errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			if firstUnexpected == nil {
				firstUnexpected = fmt.Errorf("CancelSynchronousIo invariant failure: %w", callErr)
			}
		}
		select {
		case <-done:
			return firstUnexpected
		case <-time.After(time.Millisecond):
		}
	}
}

func (in *windowsAttachInput) Close() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.closeLocked()
}

func (in *windowsAttachInput) closeLocked() error {
	if in.closed {
		return nil
	}
	in.closed = true
	return windows.CloseHandle(in.handle)
}
