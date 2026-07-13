//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/standardbeagle/agnt/internal/daemon"
	"golang.org/x/term"
)

func init() {
	runAttachTerminal = runAttachTerminalUnix
}

// rawTerminal puts fd into raw mode and returns a restore func that is safe
// to call more than once (only the first call restores; later calls are
// no-ops) — this is what guarantees termios is never restored twice or left
// dirty regardless of which of the several exit paths below fires first.
func rawTerminal(fd int) (restore func(), err error) {
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { _ = term.Restore(fd, oldState) })
	}, nil
}

// signalRestoreWatcher blocks until sigCh delivers a signal or done closes.
// On a signal it guarantees restore() has returned before calling cancel(),
// so a SIGINT/SIGTERM delivered mid-relay leaves the terminal clean before
// the process has any chance to exit. Extracted as its own function (rather
// than an inline closure) so it can be unit-tested without depending on
// real OS signal delivery.
func signalRestoreWatcher(sigCh <-chan os.Signal, done <-chan struct{}, restore func(), cancel func()) {
	select {
	case <-sigCh:
		restore()
		cancel()
	case <-done:
	}
}

// attachWatchResize watches SIGWINCH and reports the new terminal size via
// onResize until ctx is canceled. Returns a stop func.
func attachWatchResize(ctx context.Context, onResize func(cols, rows int)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				if cols, rows, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
					onResize(cols, rows)
				}
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		<-stopped
	}
}

// relayStdin reads raw stdin bytes and forwards them to the remote session
// (when this attach is primary), watching for the detach chord BEFORE any
// byte is forwarded — the chord itself is never sent to the remote. Returns
// when the chord is detected (clean detach, nil error) or stdin hits EOF/an
// error.
// runAttachTerminalUnix drives the interactive attach relay: raw terminal
// mode, bidirectional byte relay over SESSION-HOST, SIGWINCH forwarding, and
// guaranteed termios restore on every exit path (normal return, detach,
// panic, SIGINT/SIGTERM).
func runAttachTerminalUnix(client *daemon.Client, sessionID string, detachChord []byte) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("agnt attach: stdin is not a terminal")
	}

	restore, err := rawTerminal(fd)
	if err != nil {
		return fmt.Errorf("agnt attach: failed to enter raw mode: %w", err)
	}
	defer restore()
	if attachRawModeEntered != nil {
		attachRawModeEntered()
	}

	attachCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	sigDone := make(chan struct{})
	defer close(sigDone)
	go signalRestoreWatcher(sigCh, sigDone, restore, cancel)

	type attachInfo struct {
		id      string
		primary bool
	}
	attachedCh := make(chan attachInfo, 1)
	var attachedOnce sync.Once

	frameErrCh := make(chan error, 1)
	go panicSafeRestore(restore, func() {
		frameErrCh <- client.SessionHostAttach(attachCtx, sessionID,
			func(id string, primary bool) {
				attachedOnce.Do(func() { attachedCh <- attachInfo{id, primary} })
			},
			renderAttachFrame,
		)
	})

	var info attachInfo
	select {
	case info = <-attachedCh:
	case err := <-frameErrCh:
		cancel()
		return err
	case <-attachCtx.Done():
		return attachCtx.Err()
	}

	if info.primary {
		stopResize := attachWatchResize(attachCtx, func(cols, rows int) {
			_ = client.SessionHostResize(sessionID, cols, rows)
		})
		defer stopResize()
		if cols, rows, sizeErr := term.GetSize(fd); sizeErr == nil {
			_ = client.SessionHostResize(sessionID, cols, rows)
		}
	}

	stdinDoneCh := make(chan error, 1)
	go panicSafeRestore(restore, func() {
		stdinDoneCh <- relayAttachInput(os.Stdin, client, sessionID, info.id, info.primary, detachChord)
	})

	select {
	case <-attachCtx.Done():
		return nil
	case err := <-frameErrCh:
		cancel()
		return err
	case <-stdinDoneCh:
		// Either the detach chord fired (clean detach) or stdin hit
		// EOF/closed — both are a normal exit; the session keeps running.
		cancel()
		return nil
	}
}
