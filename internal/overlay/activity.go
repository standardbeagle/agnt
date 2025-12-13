package overlay

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ActivityState represents the current activity state.
type ActivityState int

const (
	ActivityIdle ActivityState = iota
	ActivityActive
)

// ActivityMonitor wraps an io.Writer to monitor output activity.
// It detects when data is being written (active) and when writing stops (idle).
type ActivityMonitor struct {
	writer          io.Writer
	idleTimeout     time.Duration
	onStateChange   func(ActivityState)
	state           atomic.Int32 // 0 = idle, 1 = active
	lastActivity    atomic.Int64 // Unix nano timestamp of last write
	stopCh          chan struct{}
	wg              sync.WaitGroup
	minActiveBytes  int // Minimum bytes to trigger active state
	activityCounter atomic.Int64
}

// ActivityMonitorConfig configures the activity monitor.
type ActivityMonitorConfig struct {
	// IdleTimeout is how long to wait with no output before transitioning to idle.
	// Default: 2 seconds
	IdleTimeout time.Duration

	// OnStateChange is called when activity state changes.
	OnStateChange func(ActivityState)

	// MinActiveBytes is the minimum bytes written to trigger active state.
	// This prevents brief flickers of activity for small outputs.
	// Default: 10
	MinActiveBytes int
}

// DefaultActivityMonitorConfig returns the default configuration.
func DefaultActivityMonitorConfig() ActivityMonitorConfig {
	return ActivityMonitorConfig{
		IdleTimeout:    2 * time.Second,
		MinActiveBytes: 10,
	}
}

// NewActivityMonitor creates a new activity monitor wrapping the given writer.
func NewActivityMonitor(w io.Writer, cfg ActivityMonitorConfig) *ActivityMonitor {
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 2 * time.Second
	}
	if cfg.MinActiveBytes == 0 {
		cfg.MinActiveBytes = 10
	}

	am := &ActivityMonitor{
		writer:         w,
		idleTimeout:    cfg.IdleTimeout,
		onStateChange:  cfg.OnStateChange,
		minActiveBytes: cfg.MinActiveBytes,
		stopCh:         make(chan struct{}),
	}

	// Start the idle check goroutine
	am.wg.Add(1)
	go am.checkIdle()

	return am
}

// Write implements io.Writer and tracks activity.
func (am *ActivityMonitor) Write(p []byte) (n int, err error) {
	n, err = am.writer.Write(p)
	if n > 0 {
		am.lastActivity.Store(time.Now().UnixNano())
		am.activityCounter.Add(int64(n))

		// Check if we should transition to active
		if am.state.Load() == 0 {
			// Only trigger active if we've accumulated enough bytes
			if am.activityCounter.Load() >= int64(am.minActiveBytes) {
				am.setState(ActivityActive)
			}
		}
	}
	return n, err
}

// setState changes the activity state and notifies the callback.
func (am *ActivityMonitor) setState(newState ActivityState) {
	oldState := ActivityState(am.state.Swap(int32(newState)))
	if oldState != newState {
		if newState == ActivityIdle {
			am.activityCounter.Store(0) // Reset counter on idle
		}
		if am.onStateChange != nil {
			am.onStateChange(newState)
		}
	}
}

// checkIdle periodically checks if the output has gone idle.
func (am *ActivityMonitor) checkIdle() {
	defer am.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-am.stopCh:
			return
		case <-ticker.C:
			if am.state.Load() == 1 { // Currently active
				lastActivity := time.Unix(0, am.lastActivity.Load())
				if time.Since(lastActivity) > am.idleTimeout {
					am.setState(ActivityIdle)
				}
			}
		}
	}
}

// State returns the current activity state.
func (am *ActivityMonitor) State() ActivityState {
	return ActivityState(am.state.Load())
}

// IsActive returns true if currently active.
func (am *ActivityMonitor) IsActive() bool {
	return am.state.Load() == 1
}

// Stop stops the activity monitor.
func (am *ActivityMonitor) Stop() {
	select {
	case <-am.stopCh:
		// Already stopped
	default:
		close(am.stopCh)
	}
	am.wg.Wait()
}
