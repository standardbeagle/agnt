package overlay

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// Complete lines must reach onOutputLine in the order the child printed them.
// The consumer correlates a signal line with the line before it, so a reordered
// stream silently mis-attributes causes.
//
// Each line used to be delivered by its own `go onOutputLine(line)`, which
// races every other line's goroutine.
func TestActivityMonitor_OutputLinesArriveInOrder(t *testing.T) {
	const lines = 500

	var mu sync.Mutex
	var got []string
	done := make(chan struct{})

	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout:    time.Hour, // never go idle mid-test
		MinActiveBytes: 1,
		OnOutputLine: func(line string) {
			mu.Lock()
			got = append(got, line)
			n := len(got)
			mu.Unlock()
			if n == lines {
				close(done)
			}
		},
	})
	defer am.Stop()

	var buf strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&buf, "line-%04d\n", i)
	}
	if _, err := am.Write([]byte(buf.String())); err != nil {
		t.Fatalf("Write: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		mu.Lock()
		n := len(got)
		mu.Unlock()
		t.Fatalf("only %d of %d lines delivered", n, lines)
	}

	mu.Lock()
	defer mu.Unlock()
	for i, line := range got {
		if want := fmt.Sprintf("line-%04d", i); line != want {
			t.Fatalf("line %d = %q, want %q (delivery is out of order)", i, line, want)
		}
	}
}

// Every complete line reaches the consumer. A burst far larger than any internal
// buffer must not drop lines: the tap scans them for host-resource errors that
// explain an abrupt agent exit, and a dropped line is a lost explanation.
//
// onOutputLine runs synchronously on the PTY write path, the same contract
// onStateChange has. It must be fast; it must not block.
func TestActivityMonitor_BurstDeliversEveryLine(t *testing.T) {
	const lines = 5000

	var mu sync.Mutex
	var got int
	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout:    time.Hour,
		MinActiveBytes: 1,
		OnOutputLine: func(string) {
			mu.Lock()
			got++
			mu.Unlock()
		},
	})
	defer am.Stop()

	var buf strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&buf, "line-%d\n", i)
	}
	if _, err := am.Write([]byte(buf.String())); err != nil {
		t.Fatalf("Write: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got != lines {
		t.Fatalf("delivered %d of %d lines; a burst must not drop any", got, lines)
	}
}

// Lines written before Stop are delivered, and Stop does not hang.
func TestActivityMonitor_StopAfterDeliveryDoesNotHang(t *testing.T) {
	var mu sync.Mutex
	var got []string

	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout:    time.Hour,
		MinActiveBytes: 1,
		OnOutputLine: func(line string) {
			mu.Lock()
			got = append(got, line)
			mu.Unlock()
		},
	})

	if _, err := am.Write([]byte("alpha\nbeta\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	stopped := make(chan struct{})
	go func() {
		am.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("delivered %v, want [alpha beta]", got)
	}
}
