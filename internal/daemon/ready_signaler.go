package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ErrDependencyFailed is returned (wrapped) by the wait methods when the
// awaited process failed to start. It is distinct from readiness so a
// dependent can refuse to launch against a prerequisite that never came up.
var ErrDependencyFailed = errors.New("dependency failed to start")

// readyState is one start generation of a process. A generation resolves at
// most once (ready or failed) and is retired when the process stops or is
// about to be started again; a waiter that sees its generation retired moves
// on to the next one instead of holding a channel nobody will ever close.
type readyState struct {
	// resolved is closed when the generation becomes ready or failed.
	resolved chan struct{}
	// err is written before resolved closes: nil means ready.
	err error
	// retired is closed when Reset replaces this generation.
	retired chan struct{}
}

func newReadyState() *readyState {
	return &readyState{resolved: make(chan struct{}), retired: make(chan struct{})}
}

func (st *readyState) isResolved() bool {
	select {
	case <-st.resolved:
		return true
	default:
		return false
	}
}

// readyOutcome is how a single-generation wait ended.
type readyOutcome int

const (
	readyOutcomeReady readyOutcome = iota
	readyOutcomeFailed
	// readyOutcomeRetired means the generation was reset (process stopped or
	// restarting) before it resolved; the caller decides whether to re-wait.
	readyOutcomeRetired
	readyOutcomeCancelled
)

// ReadySignaler manages per-process readiness state. It provides a
// coordination primitive for waiting until a process (or its port) is ready,
// with explicit semantics for the three ways a wait can end other than
// readiness: the process failed to start (SignalFailed), the process stopped
// or is restarting (Reset — waiters carry over to the next start), and the
// waiter's own context ended.
type ReadySignaler struct {
	mu      sync.Mutex
	states  map[string]*readyState
	cancels map[string]context.CancelFunc
}

// NewReadySignaler creates a new ReadySignaler.
func NewReadySignaler() *ReadySignaler {
	return &ReadySignaler{
		states:  make(map[string]*readyState),
		cancels: make(map[string]context.CancelFunc),
	}
}

// current returns the live generation for processID, creating one if needed.
func (rs *ReadySignaler) current(processID string) *readyState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	st, ok := rs.states[processID]
	if !ok {
		st = newReadyState()
		rs.states[processID] = st
	}
	return st
}

// GetOrCreate returns the signal channel for processID's current generation,
// creating one if needed. The channel closes when the generation resolves,
// ready or failed; use the wait methods to tell the two apart.
func (rs *ReadySignaler) GetOrCreate(processID string) chan struct{} {
	return rs.current(processID).resolved
}

// SignalReady marks processID as ready. This is idempotent: calling it
// multiple times is safe and has no effect after the first call. A ready
// signal supersedes an earlier failure of the same generation.
func (rs *ReadySignaler) SignalReady(processID string) {
	rs.resolve(processID, nil)
}

// SignalFailed marks processID as failed to start, waking its waiters with
// an error wrapping ErrDependencyFailed and cause. The failure stands until
// the next Reset (a new start attempt) or a later SignalReady.
func (rs *ReadySignaler) SignalFailed(processID string, cause error) {
	if cause == nil {
		cause = ErrDependencyFailed
	}
	rs.resolve(processID, cause)
}

// resolve settles the current generation with err (nil = ready). If the
// generation already settled the other way, it is retired and replaced by a
// pre-settled one so waiters parked on the old outcome re-evaluate.
func (rs *ReadySignaler) resolve(processID string, err error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	st, ok := rs.states[processID]
	if ok && !st.isResolved() {
		st.err = err
		close(st.resolved)
		return
	}
	if ok {
		if (st.err == nil) == (err == nil) {
			return // same outcome already recorded
		}
		close(st.retired)
	}
	st = newReadyState()
	st.err = err
	close(st.resolved)
	rs.states[processID] = st
}

// waitGeneration blocks until processID's current generation resolves, is
// retired, or ctx ends. The error is non-nil only for readyOutcomeFailed
// (the start failure) and readyOutcomeCancelled (ctx.Err()).
func (rs *ReadySignaler) waitGeneration(processID string, ctx context.Context) (readyOutcome, error) {
	st := rs.current(processID)
	select {
	case <-st.resolved:
		if st.err != nil {
			return readyOutcomeFailed, st.err
		}
		return readyOutcomeReady, nil
	case <-st.retired:
		return readyOutcomeRetired, nil
	case <-ctx.Done():
		return readyOutcomeCancelled, ctx.Err()
	}
}

// WaitReady blocks until processID is signaled ready or the timeout elapses.
// Returns nil if ready, or an error on timeout or start failure.
func (rs *ReadySignaler) WaitReady(processID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := rs.WaitReadyCtx(processID, ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("ready_signaler: timeout waiting for %q after %v", processID, timeout)
	}
	return err
}

// WaitReadyCtx blocks until processID is signaled ready, fails to start, or
// the context is cancelled. Returns nil if ready, an error wrapping
// ErrDependencyFailed on start failure, or ctx.Err() wrapped with context on
// cancellation. A Reset while waiting (the process stopped or is restarting)
// does not end the wait: the waiter follows the process to its next start.
func (rs *ReadySignaler) WaitReadyCtx(processID string, ctx context.Context) error {
	for {
		outcome, err := rs.waitGeneration(processID, ctx)
		switch outcome {
		case readyOutcomeReady:
			return nil
		case readyOutcomeFailed:
			return fmt.Errorf("ready_signaler: %q: %w: %v", processID, ErrDependencyFailed, err)
		case readyOutcomeCancelled:
			return fmt.Errorf("ready_signaler: context cancelled waiting for %q: %w", processID, err)
		}
	}
}

// WaitRestart blocks while processID's current generation is a recorded
// start failure, returning nil once it is superseded (Reset or SignalReady)
// and immediately if it is not failed. Lets a long-lived waiter sit out a
// failed dependency and resume when someone starts it again.
func (rs *ReadySignaler) WaitRestart(processID string, ctx context.Context) error {
	rs.mu.Lock()
	st, ok := rs.states[processID]
	rs.mu.Unlock()
	if !ok || !st.isResolved() || st.err == nil {
		return nil
	}
	select {
	case <-st.retired:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartPortProbe launches a goroutine that polls TCP connect on localhost:port
// every 500ms. When the port accepts a connection, it calls SignalReady. The
// probe stops when ctx is cancelled or the port is reached.
func (rs *ReadySignaler) StartPortProbe(processID string, port int, ctx context.Context) {
	// Ensure a generation exists before starting the probe.
	rs.current(processID)

	probeCtx, cancel := context.WithCancel(ctx)

	rs.mu.Lock()
	// Cancel any existing probe for this process.
	if prev, ok := rs.cancels[processID]; ok {
		prev()
	}
	rs.cancels[processID] = cancel
	rs.mu.Unlock()

	addr := fmt.Sprintf("localhost:%d", port)
	go func() {
		// Check immediately before waiting for the first tick, so an
		// already-bound port is detected without a 500ms delay.
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			rs.signalFromProbe(processID, probeCtx)
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
					rs.signalFromProbe(processID, probeCtx)
					return
				}
			}
		}
	}()
}

// signalFromProbe marks the existing generation for processID ready only if
// the probe has not been cancelled. Unlike SignalReady, it never resurrects
// state that Reset already removed — that would defeat Reset's guarantee
// that no further probe signal can make WaitReady return success. Runs under
// rs.mu so Reset can't race with this observation.
func (rs *ReadySignaler) signalFromProbe(processID string, probeCtx context.Context) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if probeCtx.Err() != nil {
		return
	}
	st, ok := rs.states[processID]
	if !ok || st.isResolved() {
		return
	}
	close(st.resolved)
}

// Reset retires processID's current generation and stops any running port
// probe. Call it when the process stops and before every (re)start: the old
// ready/failed fact no longer describes the process, and waiters still
// blocked on it move to the next generation rather than being stranded.
func (rs *ReadySignaler) Reset(processID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.resetLocked(processID)
}

// ResetPrefix resets every process whose ID starts with prefix (a project's
// makeProcessID prefix), so waiters for a torn-down project wake and can
// notice their own resource is gone.
func (rs *ReadySignaler) ResetPrefix(prefix string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for processID := range rs.states {
		if strings.HasPrefix(processID, prefix) {
			rs.resetLocked(processID)
		}
	}
	for processID := range rs.cancels {
		if strings.HasPrefix(processID, prefix) {
			rs.resetLocked(processID)
		}
	}
}

func (rs *ReadySignaler) resetLocked(processID string) {
	if cancel, ok := rs.cancels[processID]; ok {
		cancel()
		delete(rs.cancels, processID)
	}
	if st, ok := rs.states[processID]; ok {
		close(st.retired)
		delete(rs.states, processID)
	}
}
