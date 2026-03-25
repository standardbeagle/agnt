package overlay

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputGate_Write(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	n, err := gate.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", buf.String())
}

func TestOutputGate_Freeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Write([]byte("before"))
	gate.Freeze()
	assert.True(t, gate.IsFrozen())

	// Write while frozen should be buffered, not sent to writer
	n, err := gate.Write([]byte("during"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)

	// Underlying writer should only have "before"
	assert.Equal(t, "before", buf.String())
}

func TestOutputGate_Unfreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Freeze()
	gate.Write([]byte("frozen"))

	gate.Unfreeze()
	assert.False(t, gate.IsFrozen())

	// Buffered content should have been flushed
	assert.Equal(t, "frozen", buf.String())

	// Write after unfreeze should also work
	gate.Write([]byte("after"))
	assert.Equal(t, "frozenafter", buf.String())
}

func TestOutputGate_UnfreezeFlushesBeforeCallback(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.SetCallbacks(nil, func() {
		gate.Write([]byte("-cb"))
	})

	gate.Freeze()
	gate.Write([]byte("buffered"))

	done := make(chan struct{})
	go func() {
		gate.Unfreeze()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: unfreeze did not complete")
	}

	// Buffered content must appear before callback content
	assert.Equal(t, "buffered-cb", buf.String())
}

func TestOutputGate_Callbacks(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	freezeCalled := false
	unfreezeCalled := false

	gate.SetCallbacks(
		func() { freezeCalled = true },
		func() { unfreezeCalled = true },
	)

	gate.Freeze()
	assert.True(t, freezeCalled)

	gate.Unfreeze()
	assert.True(t, unfreezeCalled)
}

func TestOutputGate_DoubleFreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	callCount := 0
	gate.SetCallbacks(func() { callCount++ }, nil)

	gate.Freeze()
	gate.Freeze()

	assert.Equal(t, 1, callCount)
}

func TestOutputGate_DoubleUnfreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	callCount := 0
	gate.SetCallbacks(nil, func() { callCount++ })

	gate.Freeze()
	gate.Unfreeze()
	gate.Unfreeze()

	assert.Equal(t, 1, callCount)
}

func TestOutputGate_CallbackWritesToGate(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	done := make(chan struct{})
	gate.SetCallbacks(nil, func() {
		gate.Write([]byte("from-callback"))
		close(done)
	})

	gate.Freeze()
	gate.Write([]byte("frozen-write"))

	gate.Unfreeze()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: onUnfreeze callback did not complete")
	}

	// Buffered content flushed first, then callback writes
	assert.Equal(t, "frozen-writefrom-callback", buf.String())
}

func TestOutputGate_Concurrent(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				gate.Write([]byte("x"))
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			gate.Freeze()
			gate.Unfreeze()
		}
	}()

	wg.Wait()
}

// --- Ring buffer tests ---

func TestOutputGate_BufferAccumulates(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Freeze()
	gate.Write([]byte("aaa"))
	gate.Write([]byte("bbb"))

	assert.Equal(t, 6, gate.Buffered())
	assert.Equal(t, []byte("aaabbb"), gate.ReadBuffered())

	// ReadBuffered does not drain
	assert.Equal(t, 6, gate.Buffered())
}

func TestOutputGate_BufferFlushedOnUnfreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Write([]byte("pre-"))
	gate.Freeze()
	gate.Write([]byte("mid"))
	gate.Unfreeze()
	gate.Write([]byte("-post"))

	assert.Equal(t, "pre-mid-post", buf.String())
	assert.Equal(t, 0, gate.Buffered())
}

func TestOutputGate_BufferOverflow(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGateWithSize(&buf, 8)

	gate.Freeze()
	gate.Write([]byte("12345678")) // fills buffer exactly
	gate.Write([]byte("AB"))       // overwrites oldest 2 bytes

	// Should have "345678AB" (dropped "12")
	assert.Equal(t, []byte("345678AB"), gate.ReadBuffered())

	gate.Unfreeze()
	assert.Equal(t, "345678AB", buf.String())
}

func TestOutputGate_BufferOverflowLargerThanCap(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGateWithSize(&buf, 4)

	gate.Freeze()
	gate.Write([]byte("abcdefgh")) // write 8 bytes into 4-byte buffer

	// Only last 4 bytes kept
	assert.Equal(t, []byte("efgh"), gate.ReadBuffered())
}

func TestOutputGate_BufferEmptyOnUnfreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Freeze()
	// No writes while frozen
	gate.Unfreeze()

	assert.Equal(t, "", buf.String())
}

func TestOutputGate_BufferResetAfterUnfreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Freeze()
	gate.Write([]byte("first"))
	gate.Unfreeze()

	buf.Reset()

	gate.Freeze()
	gate.Write([]byte("second"))
	gate.Unfreeze()

	assert.Equal(t, "second", buf.String())
}

func TestOutputGate_ReadBufferedEmpty(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	assert.Nil(t, gate.ReadBuffered())
	assert.Equal(t, 0, gate.Buffered())
}

func TestOutputGate_WriteDirect(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Freeze()
	n, err := gate.WriteDirect([]byte("direct"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, "direct", buf.String())

	// WriteDirect should NOT affect the ring buffer
	assert.Equal(t, 0, gate.Buffered())
}

func TestOutputGate_NewOutputGateWithSize(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGateWithSize(&buf, 16)

	gate.Freeze()
	gate.Write([]byte("0123456789abcdef")) // exactly 16 bytes
	assert.Equal(t, 16, gate.Buffered())

	gate.Write([]byte("XY")) // overflow
	got := gate.ReadBuffered()
	assert.Equal(t, 16, len(got))
	// Oldest 2 bytes dropped, last 16 kept
	assert.Equal(t, "23456789abcdefXY", string(got))
}

func TestOutputGate_NewOutputGateWithSizeZero(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGateWithSize(&buf, 0)

	// Should use default size, not panic
	gate.Freeze()
	gate.Write([]byte("data"))
	assert.Equal(t, 4, gate.Buffered())
}

func TestOutputGate_ConcurrentBufferedReads(t *testing.T) {
	gate := NewOutputGateWithSize(bytes.NewBuffer(nil), 1024)

	gate.Freeze()
	gate.Write([]byte("concurrent-data"))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := gate.ReadBuffered()
			assert.Equal(t, "concurrent-data", string(data))
		}()
	}
	wg.Wait()
}
