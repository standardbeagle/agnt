package process

import (
	"bytes"
	"sync"
	"testing"
)

func TestRingBuffer_BasicWrite(t *testing.T) {
	rb := NewRingBuffer(100)

	n, err := rb.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected n=5, got %d", n)
	}

	data, truncated := rb.Snapshot()
	if truncated {
		t.Error("expected truncated=false")
	}
	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", string(data))
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	rb := NewRingBuffer(10)

	// Write more than capacity
	rb.Write([]byte("12345"))
	rb.Write([]byte("67890"))
	rb.Write([]byte("ABCDE")) // This should overflow

	data, truncated := rb.Snapshot()
	if !truncated {
		t.Error("expected truncated=true")
	}

	// Should contain the most recent data
	if len(data) != 10 {
		t.Errorf("expected len=10, got %d", len(data))
	}

	// Data should be: oldest first = "67890ABCDE" -> wraps to "0ABCDE6789"
	// Actually let's verify: we wrote 15 bytes total
	// writePos = 15 % 10 = 5
	// Buffer state: positions 0-4 have "ABCDE", positions 5-9 have "67890"
	// Snapshot reconstructs: pos(5) to end = "67890", then 0 to pos = "ABCDE"
	// Result: "67890ABCDE"
	expected := "67890ABCDE"
	if string(data) != expected {
		t.Errorf("expected %q, got %q", expected, string(data))
	}
}

func TestRingBuffer_LargeWrite(t *testing.T) {
	rb := NewRingBuffer(10)

	// Write something larger than capacity
	n, _ := rb.Write([]byte("this is a very long string"))
	if n != 26 {
		t.Errorf("expected n=26, got %d", n)
	}

	data, truncated := rb.Snapshot()
	if !truncated {
		t.Error("expected truncated=true for large write")
	}

	// Should keep only the last 10 bytes
	if string(data) != "ong string" {
		t.Errorf("expected 'ong string', got %q", string(data))
	}
}

func TestRingBuffer_ConcurrentWrites(t *testing.T) {
	rb := NewRingBuffer(1000)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rb.Write([]byte("test"))
			}
		}(i)
	}
	wg.Wait()

	// Should have written 4000 bytes total (10 * 100 * 4)
	// Buffer should contain last 1000 bytes
	data, truncated := rb.Snapshot()
	if !truncated {
		t.Error("expected truncated=true")
	}
	if len(data) != 1000 {
		t.Errorf("expected len=1000, got %d", len(data))
	}
}

func TestRingBuffer_Reset(t *testing.T) {
	rb := NewRingBuffer(100)

	rb.Write([]byte("hello"))
	rb.Reset()

	data, truncated := rb.Snapshot()
	if truncated {
		t.Error("expected truncated=false after reset")
	}
	if len(data) != 0 {
		t.Errorf("expected empty buffer, got len=%d", len(data))
	}
}

func TestRingBuffer_EmptyWrite(t *testing.T) {
	rb := NewRingBuffer(100)

	n, err := rb.Write([]byte{})
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 0 {
		t.Errorf("expected n=0, got %d", n)
	}

	data, _ := rb.Snapshot()
	if len(data) != 0 {
		t.Errorf("expected empty buffer")
	}
}

func TestRingBuffer_DefaultSize(t *testing.T) {
	rb := NewRingBuffer(0)
	if rb.Cap() != DefaultBufferSize {
		t.Errorf("expected capacity=%d, got %d", DefaultBufferSize, rb.Cap())
	}

	rb = NewRingBuffer(-1)
	if rb.Cap() != DefaultBufferSize {
		t.Errorf("expected capacity=%d, got %d", DefaultBufferSize, rb.Cap())
	}
}

func TestRingBuffer_ImplementsWriter(t *testing.T) {
	rb := NewRingBuffer(100)

	// Use bytes.Buffer API to verify it works as io.Writer
	var buf bytes.Buffer
	buf.WriteString("prefix: ")

	// This proves RingBuffer implements io.Writer
	rb.Write(buf.Bytes())

	data, _ := rb.Snapshot()
	if string(data) != "prefix: " {
		t.Errorf("expected 'prefix: ', got %q", string(data))
	}
}
