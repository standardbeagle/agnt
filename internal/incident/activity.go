package incident

import (
	"sync"
	"time"
)

// ActivityDetector tracks agent hook activity to defer non-critical pings
// while the agent is busy making tool calls. When the agent goes idle (no
// hook events for idleWindow), onIdle is called so pending pings can flush.
//
// Critical-severity events bypass the detector entirely — callers should not
// gate critical pings on IsActive().
type ActivityDetector struct {
	activeWindow time.Duration // hook events within this window count as "active"
	idleWindow   time.Duration // silence after this triggers onIdle

	mu        sync.Mutex
	lastHook  time.Time
	idleTimer *time.Timer
	onIdle    func()
}

// NewActivityDetector creates a detector. activeWindow is how long after the
// last hook event the agent is considered active (default 500ms). idleWindow
// is how long without activity before onIdle fires (default 3s). onIdle must
// not block.
func NewActivityDetector(activeWindow, idleWindow time.Duration, onIdle func()) *ActivityDetector {
	return &ActivityDetector{
		activeWindow: activeWindow,
		idleWindow:   idleWindow,
		onIdle:       onIdle,
	}
}

// RecordHook records a hook event (pre-tool-use, post-tool-use, etc.).
// Resets the idle timer so onIdle fires idleWindow from now.
func (ad *ActivityDetector) RecordHook() {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	ad.lastHook = time.Now()
	if ad.idleTimer != nil {
		ad.idleTimer.Stop()
	}
	if ad.onIdle != nil {
		ad.idleTimer = time.AfterFunc(ad.idleWindow, ad.onIdle)
	}
}

// IsActive returns true if a hook event was seen within the last activeWindow.
func (ad *ActivityDetector) IsActive() bool {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	return time.Since(ad.lastHook) <= ad.activeWindow
}

// Stop cancels the idle timer without firing it. Call on shutdown.
func (ad *ActivityDetector) Stop() {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if ad.idleTimer != nil {
		ad.idleTimer.Stop()
	}
}
