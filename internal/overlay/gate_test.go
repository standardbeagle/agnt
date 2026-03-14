package overlay

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestOutputGate_Write(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	// Write should work when not frozen
	n, err := gate.Write([]byte("hello"))
	if err != nil {
		t.Errorf("Write error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected n=5, got %d", n)
	}
	if buf.String() != "hello" {
		t.Errorf("expected 'hello', got %q", buf.String())
	}
}

func TestOutputGate_Freeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	// Write before freeze
	gate.Write([]byte("before"))

	// Freeze
	gate.Freeze()
	if !gate.IsFrozen() {
		t.Error("expected gate to be frozen")
	}

	// Write while frozen should be discarded
	n, err := gate.Write([]byte("during"))
	if err != nil {
		t.Errorf("Write error while frozen: %v", err)
	}
	if n != 6 {
		t.Errorf("expected n=6 (discarded), got %d", n)
	}

	// Buffer should only have "before"
	if buf.String() != "before" {
		t.Errorf("expected 'before', got %q", buf.String())
	}
}

func TestOutputGate_Unfreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	gate.Freeze()
	gate.Write([]byte("frozen"))

	gate.Unfreeze()
	if gate.IsFrozen() {
		t.Error("expected gate to be unfrozen")
	}

	// Write after unfreeze should work
	gate.Write([]byte("after"))
	if buf.String() != "after" {
		t.Errorf("expected 'after', got %q", buf.String())
	}
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
	if !freezeCalled {
		t.Error("freeze callback not called")
	}

	gate.Unfreeze()
	if !unfreezeCalled {
		t.Error("unfreeze callback not called")
	}
}

func TestOutputGate_DoubleFreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	callCount := 0
	gate.SetCallbacks(func() { callCount++ }, nil)

	gate.Freeze()
	gate.Freeze() // Should not call callback again

	if callCount != 1 {
		t.Errorf("expected callback to be called once, got %d", callCount)
	}
}

func TestOutputGate_DoubleUnfreeze(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	callCount := 0
	gate.SetCallbacks(nil, func() { callCount++ })

	gate.Freeze()
	gate.Unfreeze()
	gate.Unfreeze() // Should not call callback again

	if callCount != 1 {
		t.Errorf("expected callback to be called once, got %d", callCount)
	}
}

// TestOutputGate_CallbackWritesToGate verifies that an onUnfreeze callback
// can write to the gate without deadlocking. This reproduces the scenario
// where EnforceScrollRegion writes through the gate from within the callback.
func TestOutputGate_CallbackWritesToGate(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	done := make(chan struct{})
	gate.SetCallbacks(nil, func() {
		// This write acquires gate.mu — previously deadlocked when
		// the callback ran inside Unfreeze's lock.
		gate.Write([]byte("from-callback"))
		close(done)
	})

	gate.Freeze()
	gate.Write([]byte("frozen-write")) // discarded

	gate.Unfreeze()

	// If we reach here without hanging, the deadlock is fixed
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: onUnfreeze callback did not complete")
	}

	if buf.String() != "from-callback" {
		t.Errorf("expected 'from-callback', got %q", buf.String())
	}
}

func TestOutputGate_Concurrent(t *testing.T) {
	var buf bytes.Buffer
	gate := NewOutputGate(&buf)

	var wg sync.WaitGroup

	// Multiple writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				gate.Write([]byte("x"))
			}
		}()
	}

	// Freeze/unfreeze in parallel
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			gate.Freeze()
			gate.Unfreeze()
		}
	}()

	wg.Wait()
	// Just ensure no panic or deadlock
}
