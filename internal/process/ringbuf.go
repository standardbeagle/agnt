package process

import (
	"sync"
	"sync/atomic"
)

// DefaultBufferSize is the default capacity for ring buffers (256KB)
const DefaultBufferSize = 256 * 1024

// RingBuffer is a thread-safe circular buffer for process output.
// It bounds memory usage by discarding oldest data when full.
type RingBuffer struct {
	buffer   []byte
	capacity int

	// writePos tracks where the next byte will be written (mod capacity)
	writePos atomic.Int64

	// totalWritten tracks total bytes written (for overflow detection)
	totalWritten atomic.Int64

	// overflowed indicates if data has been lost due to buffer wrap
	overflowed atomic.Bool

	// mu protects writes that span the buffer boundary
	mu sync.Mutex
}

// NewRingBuffer creates a new ring buffer with the specified capacity.
// If capacity <= 0, DefaultBufferSize is used.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = DefaultBufferSize
	}
	return &RingBuffer{
		buffer:   make([]byte, capacity),
		capacity: capacity,
	}
}

// Write implements io.Writer interface.
// Thread-safe. Returns len(p), nil (never fails).
func (rb *RingBuffer) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	rb.mu.Lock()
	defer rb.mu.Unlock()

	n = len(p)

	// If input is larger than capacity, only keep the tail
	if n > rb.capacity {
		p = p[n-rb.capacity:]
		rb.overflowed.Store(true)
	}

	pos := int(rb.writePos.Load()) % rb.capacity

	// Check if this write will cause overflow
	if rb.totalWritten.Load() > 0 && int(rb.totalWritten.Load()) >= rb.capacity {
		rb.overflowed.Store(true)
	}

	// Write data, wrapping around if necessary
	written := len(p)
	firstPart := rb.capacity - pos

	if firstPart >= written {
		// Fits without wrapping
		copy(rb.buffer[pos:], p)
	} else {
		// Needs to wrap around
		copy(rb.buffer[pos:], p[:firstPart])
		copy(rb.buffer[0:], p[firstPart:])
	}

	rb.writePos.Add(int64(written))
	rb.totalWritten.Add(int64(written))

	return n, nil // Return original length
}

// Snapshot returns a copy of the current buffer contents.
// Returns (data, truncated) where truncated indicates data loss.
// Thread-safe for concurrent reads.
func (rb *RingBuffer) Snapshot() (data []byte, truncated bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	total := rb.totalWritten.Load()
	if total == 0 {
		return nil, false
	}

	truncated = rb.overflowed.Load()

	if total <= int64(rb.capacity) {
		// Buffer hasn't wrapped yet - data is from 0 to total
		result := make([]byte, total)
		copy(result, rb.buffer[:total])
		return result, truncated
	}

	// Buffer has wrapped - reconstruct in chronological order
	result := make([]byte, rb.capacity)
	pos := int(rb.writePos.Load()) % rb.capacity

	// Data from pos to end is oldest, 0 to pos is newest
	// We want oldest first, so copy in that order
	oldestLen := rb.capacity - pos
	copy(result[:oldestLen], rb.buffer[pos:])
	copy(result[oldestLen:], rb.buffer[:pos])

	return result, true
}

// Len returns the current number of bytes stored (up to capacity).
func (rb *RingBuffer) Len() int {
	total := rb.totalWritten.Load()
	if total > int64(rb.capacity) {
		return rb.capacity
	}
	return int(total)
}

// Cap returns the buffer capacity.
func (rb *RingBuffer) Cap() int {
	return rb.capacity
}

// Reset clears the buffer.
func (rb *RingBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.writePos.Store(0)
	rb.totalWritten.Store(0)
	rb.overflowed.Store(false)
}

// Truncated returns whether any data has been lost due to overflow.
func (rb *RingBuffer) Truncated() bool {
	return rb.overflowed.Load()
}
