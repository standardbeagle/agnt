package overlay

import (
	"io"
	"sync"
)

const defaultFrozenBufSize = 64 * 1024 // 64KB

// OutputGate wraps a writer and can freeze/unfreeze output.
// When frozen, writes are buffered in a ring buffer. On unfreeze,
// the buffered content is flushed to the underlying writer.
// This is used to prevent PTY output from corrupting the overlay menu.
type OutputGate struct {
	out        io.Writer
	mu         sync.Mutex
	frozen     bool
	onFreeze   func()
	onUnfreeze func()

	// Ring buffer for frozen output. Data lives in buf[head:tail] with
	// wraparound. When full, oldest bytes are dropped.
	buf  []byte
	head int // read position
	tail int // write position
	full bool
}

// NewOutputGate creates a new OutputGate with the default 64KB frozen buffer.
func NewOutputGate(out io.Writer) *OutputGate {
	return &OutputGate{
		out: out,
		buf: make([]byte, defaultFrozenBufSize),
	}
}

// NewOutputGateWithSize creates a new OutputGate with a custom buffer size.
func NewOutputGateWithSize(out io.Writer, bufSize int) *OutputGate {
	if bufSize <= 0 {
		bufSize = defaultFrozenBufSize
	}
	return &OutputGate{
		out: out,
		buf: make([]byte, bufSize),
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
// When frozen, writes are buffered in the ring buffer.
func (og *OutputGate) Write(p []byte) (n int, err error) {
	og.mu.Lock()
	defer og.mu.Unlock()

	if og.frozen {
		og.bufWrite(p)
		return len(p), nil
	}

	return og.out.Write(p)
}

// Freeze stops writing to the underlying writer (buffers writes).
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
// Flushes any buffered content to the writer before resuming.
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
	data := og.bufDrain()
	// Flush while still holding the lock so no concurrent Write() can
	// slip through to og.out before the buffered content is written.
	if len(data) > 0 {
		og.out.Write(data)
	}
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

// ReadBuffered returns any buffered content without draining it.
// Returns nil if the buffer is empty. This is intended for panels
// that want to inspect buffered output while the gate is still frozen.
func (og *OutputGate) ReadBuffered() []byte {
	og.mu.Lock()
	defer og.mu.Unlock()
	return og.bufPeek()
}

// Buffered returns the number of bytes currently in the ring buffer.
func (og *OutputGate) Buffered() int {
	og.mu.Lock()
	defer og.mu.Unlock()
	return og.bufLen()
}

// --- ring buffer internals (caller must hold og.mu) ---

func (og *OutputGate) bufLen() int {
	if og.full {
		return len(og.buf)
	}
	if og.tail >= og.head {
		return og.tail - og.head
	}
	return len(og.buf) - og.head + og.tail
}

// bufWrite appends p to the ring buffer, dropping oldest bytes on overflow.
func (og *OutputGate) bufWrite(p []byte) {
	cap := len(og.buf)
	if cap == 0 {
		return
	}

	// If p is larger than the buffer, only keep the last cap bytes.
	if len(p) >= cap {
		copy(og.buf, p[len(p)-cap:])
		og.head = 0
		og.tail = 0
		og.full = true
		return
	}

	for _, b := range p {
		og.buf[og.tail] = b
		og.tail = (og.tail + 1) % cap
		if og.full {
			og.head = og.tail // drop oldest
		}
		if og.tail == og.head {
			og.full = true
		}
	}
}

// bufDrain returns all buffered data and resets the buffer.
func (og *OutputGate) bufDrain() []byte {
	n := og.bufLen()
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	og.bufReadInto(out)
	og.head = 0
	og.tail = 0
	og.full = false
	return out
}

// bufPeek returns a copy of buffered data without draining.
func (og *OutputGate) bufPeek() []byte {
	n := og.bufLen()
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	og.bufReadInto(out)
	return out
}

// bufReadInto copies buffered data into dst. dst must be at least bufLen() bytes.
func (og *OutputGate) bufReadInto(dst []byte) {
	if og.head < og.tail && !og.full {
		// Contiguous: head..tail
		copy(dst, og.buf[og.head:og.tail])
	} else {
		// Wraps: head..end, then 0..tail
		first := copy(dst, og.buf[og.head:])
		copy(dst[first:], og.buf[:og.tail])
	}
}
