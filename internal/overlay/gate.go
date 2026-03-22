package overlay

import (
	"io"
	"sync"
)

// OutputGate wraps a writer and can freeze/unfreeze output.
// When frozen, all writes are discarded. When unfrozen, writes pass through.
// This is used to prevent PTY output from corrupting the overlay menu.
type OutputGate struct {
	out        io.Writer
	mu         sync.Mutex
	frozen     bool
	onFreeze   func()
	onUnfreeze func()
}

// NewOutputGate creates a new OutputGate.
func NewOutputGate(out io.Writer) *OutputGate {
	return &OutputGate{
		out: out,
	}
}

// SetCallbacks sets the freeze/unfreeze callbacks.
func (og *OutputGate) SetCallbacks(onFreeze, onUnfreeze func()) {
	og.mu.Lock()
	defer og.mu.Unlock()
	og.onFreeze = onFreeze
	og.onUnfreeze = onUnfreeze
}

// Write writes to the underlying writer if not frozen.
// When frozen, writes are discarded but return success.
func (og *OutputGate) Write(p []byte) (n int, err error) {
	og.mu.Lock()
	defer og.mu.Unlock()

	if og.frozen {
		// Discard but report success
		return len(p), nil
	}

	return og.out.Write(p)
}

// Freeze stops writing to the underlying writer (discards writes).
// Calls onFreeze callback if set and not already frozen.
// The callback runs after the lock is released to prevent deadlock
// when the callback writes to this gate.
func (og *OutputGate) Freeze() {
	og.mu.Lock()
	if og.frozen {
		og.mu.Unlock()
		return
	}
	og.frozen = true
	cb := og.onFreeze
	og.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// Unfreeze resumes writing to the underlying writer.
// Calls onUnfreeze callback if set and was frozen.
// The callback runs after the lock is released to prevent deadlock
// when the callback writes to this gate (e.g. EnforceScrollRegion).
func (og *OutputGate) Unfreeze() {
	og.mu.Lock()
	if !og.frozen {
		og.mu.Unlock()
		return
	}
	og.frozen = false
	cb := og.onUnfreeze
	og.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// IsFrozen returns whether the gate is currently frozen.
func (og *OutputGate) IsFrozen() bool {
	og.mu.Lock()
	defer og.mu.Unlock()
	return og.frozen
}

// WriteDirect writes to the underlying writer regardless of freeze state,
// while holding the gate's mutex. This is used by the overlay renderer to
// write indicator/menu content without interleaving with PTY output.
func (og *OutputGate) WriteDirect(p []byte) (n int, err error) {
	og.mu.Lock()
	defer og.mu.Unlock()
	return og.out.Write(p)
}
