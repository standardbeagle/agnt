//go:build windows

package main

import (
	"context"
	"log"
	"time"
)

// reapSessionPGID is a no-op on Windows: the setup child's process tree is
// owned by a Job Object that cascade-kills it when runConPTYChild's jobCloser
// runs, so there is no separate process group to reap before phase 2. The pgid
// argument is always 0 here.
func reapSessionPGID(_ int) {}

// watchResize polls the host terminal size on a 500ms cadence and forwards
// new dimensions to the platform-agnostic handler. Windows has no SIGWINCH
// equivalent — see the SIGWINCH-driven Unix variant in signals_unix.go.
func watchResize(ctx context.Context, done <-chan struct{}, handle *ptyHandle, rt *pipelineRuntime) {
	lastWidth, lastHeight := handle.Width, handle.Height
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			w, h := getTerminalSize()
			if w == lastWidth && h == lastHeight {
				continue
			}
			lastWidth, lastHeight = w, h

			ch := h
			if rt.termOverlay != nil && rt.termOverlay.ShowIndicator() && h > 1 {
				ch = h - 1
			}
			if err := handle.Resize(w, ch); err != nil {
				log.Printf("error resizing PTY: %s", err)
			}
			if rt.activityMonitor != nil {
				// Keep the preview's virtual screen matched to the child PTY.
				rt.activityMonitor.Resize(w, ch)
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
