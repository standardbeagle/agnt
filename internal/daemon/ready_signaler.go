package daemon

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// ReadySignaler manages per-process readiness channels. It provides a
// coordination primitive for waiting until a process (or its port) is ready.
type ReadySignaler struct {
	mu      sync.Mutex
	signals map[string]chan struct{}
	cancels map[string]context.CancelFunc
}

// NewReadySignaler creates a new ReadySignaler.
func NewReadySignaler() *ReadySignaler {
	return &ReadySignaler{
		signals: make(map[string]chan struct{}),
		cancels: make(map[string]context.CancelFunc),
	}
}

// GetOrCreate returns the signal channel for processID, creating one if needed.
func (rs *ReadySignaler) GetOrCreate(processID string) chan struct{} {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	ch, ok := rs.signals[processID]
	if !ok {
		ch = make(chan struct{})
		rs.signals[processID] = ch
	}
	return ch
}

// SignalReady marks processID as ready by closing its channel. This is
// idempotent: calling it multiple times is safe and has no effect after
// the first call.
func (rs *ReadySignaler) SignalReady(processID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	ch, ok := rs.signals[processID]
	if !ok {
		// No channel exists yet; create a pre-closed one.
		ch = make(chan struct{})
		close(ch)
		rs.signals[processID] = ch
		return
	}
	// Close only if not already closed.
	select {
	case <-ch:
		// Already closed.
	default:
		close(ch)
	}
}

// WaitReady blocks until processID is signaled ready or the timeout elapses.
// Returns nil if ready, or an error on timeout.
func (rs *ReadySignaler) WaitReady(processID string, timeout time.Duration) error {
	ch := rs.GetOrCreate(processID)
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("ready_signaler: timeout waiting for %q after %v", processID, timeout)
	}
}

// StartPortProbe launches a goroutine that polls TCP connect on localhost:port
// every 500ms. When the port accepts a connection, it calls SignalReady. The
// probe stops when ctx is cancelled or the port is reached.
func (rs *ReadySignaler) StartPortProbe(processID string, port int, ctx context.Context) {
	// Ensure a channel exists before starting the probe.
	rs.GetOrCreate(processID)

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
					rs.SignalReady(processID)
					return
				}
			}
		}
	}()
}

// Cleanup removes the signal channel and stops any running port probe for
// processID.
func (rs *ReadySignaler) Cleanup(processID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if cancel, ok := rs.cancels[processID]; ok {
		cancel()
		delete(rs.cancels, processID)
	}
	delete(rs.signals, processID)
}
