package readiness

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// ProcessReadiness is the single coordination point for dependency-ordered autostart.
// When script A depends on script B, A's goroutine calls Wait(B's processID) and blocks
// until B reaches any terminal state: Ready (URL detected or port bound), Failed (start
// error or crash), or Exited (process removed from ProcessManager).
//
// All three terminal states unblock waiters immediately. This is critical: if a dependency
// crashes or exits, dependents must not block for the full timeout — they start anyway with
// a warning. Before this design, the ReadySignaler only had one terminal state (Ready), so
// a crashed dependency caused 120s hangs across all dependents.
//
// Lifecycle flow:
//
//	autostartScript goroutine        onURLDetected callback      onProcessStopped callback
//	        |                               |                              |
//	  MarkStarting(pid)              MarkReady(pid)               MarkExited(pid)
//	  waitForDependencies(deps)      (URL detected)               (process removed)
//	  autostartScript() error → MarkFailed(pid)
//	  StartPortProbe(pid, port)  → MarkReady(pid) when port binds
//
// See docs/plans/2026-03-15-process-dependency-ordering-design.md for full context.

// ReadinessState represents the lifecycle state of a process for dependency ordering.
type ReadinessState uint32

const (
	// RStateUnknown is the initial state before any lifecycle event.
	RStateUnknown ReadinessState = iota
	// RStateStarting means the process has been launched but isn't serving yet.
	RStateStarting
	// RStateReady means the process is serving (URL detected or port bound).
	RStateReady
	// RStateFailed means the process failed to start or crashed.
	RStateFailed
	// RStateExited means the process exited (removed from ProcessManager).
	RStateExited
)

func (s ReadinessState) String() string {
	switch s {
	case RStateUnknown:
		return "unknown"
	case RStateStarting:
		return "starting"
	case RStateReady:
		return "ready"
	case RStateFailed:
		return "failed"
	case RStateExited:
		return "exited"
	default:
		return fmt.Sprintf("ReadinessState(%d)", s)
	}
}

// IsTerminal returns true if the state unblocks waiters.
func (s ReadinessState) IsTerminal() bool {
	return s == RStateReady || s == RStateFailed || s == RStateExited
}

// ReadinessResult is returned by Wait when a process reaches a terminal state.
type ReadinessResult struct {
	State ReadinessState
	Err   error // nil for Ready/clean exit, non-nil for Failed
}

type readinessEntry struct {
	state  ReadinessState
	err    error
	ch     chan struct{}      // closed when state becomes terminal
	cancel context.CancelFunc // cancels port probe if running
}

// ProcessReadiness is the single coordination point for dependency ordering.
// All process lifecycle events flow through here. All dependency waits resolve here.
type ProcessReadiness struct {
	mu      sync.Mutex
	entries map[string]*readinessEntry
}

// NewProcessReadiness creates a new ProcessReadiness coordinator.
func NewProcessReadiness() *ProcessReadiness {
	return &ProcessReadiness{
		entries: make(map[string]*readinessEntry),
	}
}

// getOrCreate returns the entry for processID, creating one in Unknown state if needed.
// Caller must hold pr.mu.
func (pr *ProcessReadiness) getOrCreate(processID string) *readinessEntry {
	e, ok := pr.entries[processID]
	if !ok {
		e = &readinessEntry{
			state: RStateUnknown,
			ch:    make(chan struct{}),
		}
		pr.entries[processID] = e
	}
	return e
}

// resolve transitions an entry to a terminal state and closes the waiter channel.
// No-op if already terminal. Caller must hold pr.mu.
func (pr *ProcessReadiness) resolve(e *readinessEntry, state ReadinessState, err error) {
	if e.state.IsTerminal() {
		return // already resolved
	}
	e.state = state
	e.err = err
	close(e.ch)
}

// MarkStarting registers that a process has begun starting.
// Idempotent if already starting or terminal.
func (pr *ProcessReadiness) MarkStarting(processID string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	e := pr.getOrCreate(processID)
	if e.state == RStateUnknown {
		e.state = RStateStarting
	}
}

// MarkReady signals the process is serving (URL detected or port bound).
// Unblocks all waiters.
func (pr *ProcessReadiness) MarkReady(processID string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	e := pr.getOrCreate(processID)
	pr.resolve(e, RStateReady, nil)
}

// MarkFailed signals the process failed to start or crashed.
// Unblocks all waiters with the error.
func (pr *ProcessReadiness) MarkFailed(processID string, err error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	e := pr.getOrCreate(processID)
	pr.resolve(e, RStateFailed, err)
}

// MarkExited signals the process is no longer in the ProcessManager.
// Unblocks all waiters. Uses RStateExited if not already terminal.
func (pr *ProcessReadiness) MarkExited(processID string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	e, ok := pr.entries[processID]
	if !ok {
		return // never tracked, nothing to unblock
	}
	pr.resolve(e, RStateExited, fmt.Errorf("process %s exited", processID))
}

// Wait blocks until processID reaches a terminal state or the timeout elapses.
// Returns immediately if already terminal.
func (pr *ProcessReadiness) Wait(processID string, timeout time.Duration) ReadinessResult {
	pr.mu.Lock()
	e := pr.getOrCreate(processID)
	// Fast path: already terminal.
	if e.state.IsTerminal() {
		result := ReadinessResult{State: e.state, Err: e.err}
		pr.mu.Unlock()
		return result
	}
	ch := e.ch
	pr.mu.Unlock()

	select {
	case <-ch:
		pr.mu.Lock()
		result := ReadinessResult{State: e.state, Err: e.err}
		pr.mu.Unlock()
		return result
	case <-time.After(timeout):
		return ReadinessResult{
			State: RStateUnknown,
			Err:   fmt.Errorf("timeout waiting for %q after %v", processID, timeout),
		}
	}
}

// StartPortProbe launches a goroutine that polls TCP connect on localhost:port.
// When the port accepts a connection, it calls MarkReady. The probe stops when
// ctx is cancelled, the port is reached, or Cleanup is called.
func (pr *ProcessReadiness) StartPortProbe(processID string, port int, ctx context.Context) {
	pr.mu.Lock()
	e := pr.getOrCreate(processID)

	// Already terminal — no probe needed.
	if e.state.IsTerminal() {
		pr.mu.Unlock()
		return
	}

	// Cancel any existing probe for this process.
	if e.cancel != nil {
		e.cancel()
	}

	probeCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	pr.mu.Unlock()

	addr := fmt.Sprintf("localhost:%d", port)
	go func() {
		// Check immediately before first tick.
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			pr.MarkReady(processID)
			return
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-probeCtx.Done():
				return
			case <-ticker.C:
				conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
				if err == nil {
					conn.Close()
					pr.MarkReady(processID)
					return
				}
			}
		}
	}()
}

// Cleanup removes the entry and cancels any running port probe for processID.
func (pr *ProcessReadiness) Cleanup(processID string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if e, ok := pr.entries[processID]; ok {
		if e.cancel != nil {
			e.cancel()
		}
		delete(pr.entries, processID)
	}
}
