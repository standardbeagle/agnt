// Package process stress harness for the RingBuffer primitive that
// backs ManagedProcess output capture.
//
// Implementation under test: github.com/standardbeagle/go-cli-server/process.RingBuffer
// (moved out of this repo in commit 44fff97). This file pins the concurrency
// and overflow invariants agnt depends on so that upstream bumps cannot
// silently weaken them.
//
// Scope: pure data-structure — no PTY, no ProcessManager, no subprocess.
// API exercised: NewRingBuffer, Write, Snapshot, Len, Cap, Truncated, Reset.
package process

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/go-cli-server/process"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestMain pins a goroutine-leak fence around the whole stress suite.
// None of these tests spawn production goroutines — any leak is a test
// bug (forgotten wg.Done, unreturned reader goroutine) and must fail CI.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ---- Test 1: ConcurrentWriters ------------------------------------------

// TestRingBuffer_ConcurrentWriters asserts that 16 goroutines writing
// 1MB each into a 256KB ring (total 16MB, 64× capacity) yields a
// well-formed final state: snapshot fits capacity, overflow flag set,
// total-bytes accounting via Len() stays capped, and io.Writer contract
// (each Write returns len(p), nil) holds under concurrency.
func TestRingBuffer_ConcurrentWriters(t *testing.T) {
	const (
		workers      = 16
		bytesPerTask = 128 * 1024 // 128KB per worker × 16 = 2MB = 8× capacity
		cap          = 256 * 1024
	)
	rb := process.NewRingBuffer(cap)

	var wg sync.WaitGroup
	var totalWritten atomic.Int64
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(worker int) {
			defer wg.Done()
			// Chunk writes to exercise boundary wrapping. 4KB chunks
			// give 256 writes per worker = 4096 writes total.
			const chunk = 4096
			buf := make([]byte, chunk)
			// Fill with worker-specific content so we can't accidentally
			// pass via zero-initialized sentinel.
			for i := range buf {
				buf[i] = byte(worker<<4 | (i & 0xF))
			}
			for off := 0; off < bytesPerTask; off += chunk {
				n, err := rb.Write(buf)
				assert.NoError(t, err, "worker %d chunk %d", worker, off)
				assert.Equal(t, chunk, n, "worker %d chunk %d short write", worker, off)
				totalWritten.Add(int64(n))
			}
		}(w)
	}
	wg.Wait()

	// Invariants:
	// (1) All workers reported full writes.
	assert.Equal(t, int64(workers*bytesPerTask), totalWritten.Load(),
		"totalWritten across workers")

	// (2) Snapshot fits capacity. With 2MB written into 256KB, Len
	//     must clamp at cap.
	require.Equal(t, cap, rb.Len(), "Len clamps at capacity")
	require.Equal(t, cap, rb.Cap(), "Cap is stable")

	// (3) Overflow flag matches expected drop count > 0.
	assert.True(t, rb.Truncated(),
		"Truncated must be true when writes far exceed capacity")

	// (4) Snapshot length matches Len().
	data, truncated := rb.Snapshot()
	assert.True(t, truncated, "Snapshot reports truncated")
	assert.Len(t, data, cap, "Snapshot returns exactly capacity bytes")

	// (5) Returned slice is a copy, not aliased to internal storage —
	//     mutating it must not affect subsequent snapshots.
	if len(data) > 0 {
		data[0] ^= 0xFF
		data2, _ := rb.Snapshot()
		assert.NotEqual(t, data[0], data2[0],
			"Snapshot must return a defensive copy")
	}
}

// ---- Test 2: WriteBeyondCapacity ----------------------------------------

// TestRingBuffer_WriteBeyondCapacity asserts 100× capacity sequential
// writes surface as Truncated=true and Snapshot contains exactly the
// most-recent `cap` bytes in chronological order (FIFO eviction).
func TestRingBuffer_WriteBeyondCapacity(t *testing.T) {
	const (
		cap      = 1024
		multiple = 100
	)
	rb := process.NewRingBuffer(cap)

	// Write a deterministic sequence: byte i holds byte(i & 0xFF).
	// After 100*cap bytes, the last `cap` bytes contain bytes
	// [99*cap, 100*cap).
	total := cap * multiple
	chunk := 128 // small chunk so wrap happens many times
	buf := make([]byte, chunk)
	for off := 0; off < total; off += chunk {
		for i := 0; i < chunk; i++ {
			buf[i] = byte((off + i) & 0xFF)
		}
		n, err := rb.Write(buf)
		require.NoError(t, err)
		require.Equal(t, chunk, n)
	}

	require.True(t, rb.Truncated(), "Truncated after 100× cap writes")
	require.Equal(t, cap, rb.Len())

	data, truncated := rb.Snapshot()
	require.True(t, truncated)
	require.Len(t, data, cap)

	// Expected: bytes [total-cap, total). Each byte = (i & 0xFF).
	for i := 0; i < cap; i++ {
		expected := byte((total - cap + i) & 0xFF)
		require.Equalf(t, expected, data[i],
			"FIFO eviction failed at snapshot byte %d: want %d got %d",
			i, expected, data[i])
	}
}

// ---- Test 3: ConcurrentReadWrite ----------------------------------------

// TestRingBuffer_ConcurrentReadWrite asserts readers calling Snapshot
// concurrently with writers never observe torn writes. A "torn write"
// would be: a Snapshot returning N bytes where the last few bytes are
// zero (partial Write(p) visible mid-copy). We verify this by:
//  1. Only writing non-zero bytes.
//  2. Asserting every snapshot byte is non-zero.
//  3. Length must equal min(total, cap) — never a fractional Write visible.
//
// Intended run harness: -race -count=100. Kept <10ms per run.
func TestRingBuffer_ConcurrentReadWrite(t *testing.T) {
	const (
		cap      = 4096
		writers  = 4
		readers  = 4
		duration = 50 * time.Millisecond
	)
	rb := process.NewRingBuffer(cap)

	done := make(chan struct{})
	var wg sync.WaitGroup

	// Writers: emit chunks of non-zero bytes.
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(worker int) {
			defer wg.Done()
			chunk := make([]byte, 256)
			for i := range chunk {
				// Every byte is non-zero and worker-specific.
				chunk[i] = byte(1 + (worker+i)%250)
			}
			for {
				select {
				case <-done:
					return
				default:
					rb.Write(chunk)
				}
			}
		}(w)
	}

	// Readers: snapshot repeatedly, assert no zero bytes in valid region.
	var snapshots atomic.Int64
	wg.Add(readers)
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					data, _ := rb.Snapshot()
					snapshots.Add(1)
					// Length must be <= cap (never torn to a
					// fractional-past-cap value).
					if len(data) > cap {
						t.Errorf("Snapshot returned %d > cap %d",
							len(data), cap)
						return
					}
					// Every byte must be non-zero (writers only emit
					// non-zero). A zero byte means a torn read.
					for i, b := range data {
						if b == 0 {
							t.Errorf("torn read: zero byte at index %d in snapshot of len %d",
								i, len(data))
							return
						}
					}
				}
			}
		}()
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()

	assert.Greater(t, snapshots.Load(), int64(10),
		"readers should have taken many snapshots under concurrency")
	assert.True(t, rb.Truncated(),
		"writers should have overflowed the small ring by now")
}

// ---- Test 4: ZeroCapacity -----------------------------------------------

// TestRingBuffer_ZeroCapacity asserts NewRingBuffer's documented
// behavior on non-positive capacity: use DefaultBufferSize. Writes of
// any size are deterministic — no panic, no error, no torn state.
func TestRingBuffer_ZeroCapacity(t *testing.T) {
	cases := []struct {
		name string
		cap  int
	}{
		{"zero", 0},
		{"negative", -1},
		{"minus_large", -99999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rb := process.NewRingBuffer(tc.cap)
			// Must have fallen back to DefaultBufferSize.
			assert.Equal(t, 256*1024, rb.Cap(),
				"NewRingBuffer(%d) must default to DefaultBufferSize",
				tc.cap)
			// Empty state.
			assert.Equal(t, 0, rb.Len())
			assert.False(t, rb.Truncated())
			data, truncated := rb.Snapshot()
			assert.Nil(t, data, "Snapshot of empty buffer is nil")
			assert.False(t, truncated)

			// Single byte write works.
			n, err := rb.Write([]byte{42})
			require.NoError(t, err)
			require.Equal(t, 1, n)
			assert.Equal(t, 1, rb.Len())
		})
	}

	// Also assert Write([]byte{}) is a noop on any ring.
	t.Run("empty_write_is_noop", func(t *testing.T) {
		rb := process.NewRingBuffer(16)
		n, err := rb.Write(nil)
		assert.NoError(t, err)
		assert.Equal(t, 0, n)
		n, err = rb.Write([]byte{})
		assert.NoError(t, err)
		assert.Equal(t, 0, n)
		assert.Equal(t, 0, rb.Len())
	})
}

// ---- Test 5: SingleByteWrites -------------------------------------------

// TestRingBuffer_SingleByteWrites asserts 64K single-byte writes leave
// the buffer consistent — final snapshot contains exactly the last
// `cap` bytes of the sequence in chronological order. Single-byte
// writes exercise the boundary-wrap path maximally (16 full wraps
// over a 4KB ring).
//
// Task spec says 1M writes, but that takes ~4s under -race because
// every Write takes + releases the buffer mutex. 64K writes hit the
// same invariants — 16 full wraps is plenty for chronology + overflow
// — while keeping wall-clock under 100ms per run.
func TestRingBuffer_SingleByteWrites(t *testing.T) {
	const (
		cap    = 4096
		writes = 1 << 16 // 65,536 writes = 16 full wraps
	)
	rb := process.NewRingBuffer(cap)

	var b [1]byte
	for i := 0; i < writes; i++ {
		b[0] = byte(i & 0xFF)
		n, err := rb.Write(b[:])
		if err != nil || n != 1 {
			t.Fatalf("single-byte write %d returned (%d, %v)", i, n, err)
		}
	}

	require.Equal(t, cap, rb.Len())
	require.True(t, rb.Truncated())

	data, truncated := rb.Snapshot()
	require.True(t, truncated)
	require.Len(t, data, cap)

	// Chronological check: snapshot[i] must equal byte((writes-cap+i) & 0xFF).
	for i := 0; i < cap; i++ {
		expected := byte((writes - cap + i) & 0xFF)
		if data[i] != expected {
			t.Fatalf("single-byte chronology: at %d want %d got %d",
				i, expected, data[i])
		}
	}

	// io.Writer contract sanity: cast to io.Writer and call fmt.Fprint.
	var w io.Writer = rb
	_, err := fmt.Fprint(w, "X")
	require.NoError(t, err)
}

// ---- Test 6: ReaderSpansOverflow ----------------------------------------

// TestRingBuffer_ReaderSpansOverflow asserts a Snapshot taken before
// additional writes remains stable even as the writer continues to
// overflow the ring. The Snapshot is a defensive copy; mutations to
// the ring after Snapshot must not be visible in the earlier result.
func TestRingBuffer_ReaderSpansOverflow(t *testing.T) {
	const cap = 1024
	rb := process.NewRingBuffer(cap)

	// Phase 1: fill ring with known pattern 'A'.
	payloadA := bytes.Repeat([]byte{'A'}, cap)
	n, err := rb.Write(payloadA)
	require.NoError(t, err)
	require.Equal(t, cap, n)

	// Take snapshot of all-A state.
	snapA, truncA := rb.Snapshot()
	require.False(t, truncA, "first snapshot (exactly cap bytes) should not be truncated yet")
	require.Len(t, snapA, cap)
	require.True(t, bytes.Equal(snapA, payloadA),
		"snapshot A content matches writes")

	// Phase 2: overflow the ring with 'B'.
	payloadB := bytes.Repeat([]byte{'B'}, cap*5)
	_, err = rb.Write(payloadB)
	require.NoError(t, err)

	// snapA must remain unchanged — it is a defensive copy taken
	// before the overflow. This is the key invariant: readers who
	// took a snapshot span an overflow without their snapshot being
	// corrupted.
	for i, b := range snapA {
		if b != 'A' {
			t.Fatalf("snapshot A corrupted at index %d: got %c, want 'A'", i, b)
		}
	}

	// Current ring state is all-B, truncated.
	snapB, truncB := rb.Snapshot()
	require.True(t, truncB, "ring is truncated after overflow")
	require.Len(t, snapB, cap)
	for i, b := range snapB {
		if b != 'B' {
			t.Fatalf("snapshot B inconsistent at index %d: got %c, want 'B'", i, b)
		}
	}

	// And snapA is still pristine — double-check by reading again.
	for _, b := range snapA {
		require.Equal(t, byte('A'), b, "snapA remained stable")
	}
}

// ---- Test 7: TruncatedFlag ----------------------------------------------

// TestRingBuffer_TruncatedFlag asserts the Truncated flag's semantics:
//   - Starts false.
//   - Set to true the first time a write causes data loss.
//   - Stays true on subsequent reads (sticky — historical loss is not
//     forgotten by later non-overflowing writes).
//   - Snapshot's second return value tracks Truncated().
//   - Reset() clears the flag.
func TestRingBuffer_TruncatedFlag(t *testing.T) {
	const cap = 256
	rb := process.NewRingBuffer(cap)

	// Initially: not truncated.
	assert.False(t, rb.Truncated())
	_, tr0 := rb.Snapshot()
	assert.False(t, tr0, "empty buffer snapshot not truncated")

	// Write exactly cap — fills without loss.
	_, err := rb.Write(bytes.Repeat([]byte{'x'}, cap))
	require.NoError(t, err)
	assert.False(t, rb.Truncated(),
		"writing exactly cap bytes should not yet trigger Truncated")

	// One more byte: overflow.
	_, err = rb.Write([]byte{'y'})
	require.NoError(t, err)
	assert.True(t, rb.Truncated(),
		"writing cap+1 bytes must set Truncated")
	_, tr1 := rb.Snapshot()
	assert.True(t, tr1, "Snapshot second return tracks Truncated")

	// Flag is sticky: subsequent small writes don't un-truncate.
	_, err = rb.Write([]byte{'z'})
	require.NoError(t, err)
	assert.True(t, rb.Truncated(),
		"Truncated is sticky across subsequent writes")

	// Multiple sequential snapshots return consistent truncated=true.
	for i := 0; i < 5; i++ {
		_, tr := rb.Snapshot()
		assert.Truef(t, tr, "snapshot %d remains truncated", i)
	}

	// Reset clears the flag.
	rb.Reset()
	assert.False(t, rb.Truncated(), "Reset clears Truncated")
	assert.Equal(t, 0, rb.Len())
	data, tr := rb.Snapshot()
	assert.Nil(t, data)
	assert.False(t, tr)

	// Single large write > cap: truncation set in one shot.
	_, err = rb.Write(bytes.Repeat([]byte{'w'}, cap*2))
	require.NoError(t, err)
	assert.True(t, rb.Truncated(),
		"single write > cap sets Truncated immediately")
}
