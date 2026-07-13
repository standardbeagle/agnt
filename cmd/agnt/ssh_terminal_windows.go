//go:build windows

package main

import (
	"context"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/sshclient"
	"golang.org/x/term"
)

func sshRawTerminal(fd int) (func(), error) {
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}, err
	}
	restored := false
	return func() {
		if !restored {
			restored = true
			_ = term.Restore(fd, oldState)
		}
	}, nil
}

// Windows has no SIGWINCH. Polling the console size keeps resize lifecycle
// bound to the relay context and sends only changes.
func sshWatchResize(ctx context.Context, session *sshclient.PTYSession) func() {
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		lastCols, lastRows := 0, 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
				if err == nil && (cols != lastCols || rows != lastRows) {
					lastCols, lastRows = cols, rows
					_ = session.Resize(sshclient.TermSize{Cols: uint32(cols), Rows: uint32(rows)})
				}
			}
		}
	}()
	return func() { <-stopped }
}
