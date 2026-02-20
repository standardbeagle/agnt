package overlay

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- Race Condition Tests for AlertScanner ---

func TestAlertScanner_ConcurrentProcessLine(t *testing.T) {
	var batchCount atomic.Int64
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 10 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			batchCount.Add(1)
		},
	})
	defer scanner.Stop()

	var wg sync.WaitGroup

	// 10 goroutines sending different error lines concurrently
	errorLines := []string{
		"panic: runtime error: index out of range",
		"ERROR in ./src/App.tsx",
		"WARNING: DATA RACE",
		"Error: ENOENT: no such file or directory",
		"Traceback (most recent call last):",
		"FATAL ERROR: CALL_AND_RETRY_LAST Allocation failed",
		"error TS2304: Cannot find name 'foo'",
		"[error] Failed to compile",
		"Exception in thread \"main\"",
		"segfault at 0x0",
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				line := errorLines[j%len(errorLines)]
				scanner.ProcessLine(
					fmt.Sprintf("[%d-%d] %s", id, j, line),
					fmt.Sprintf("script-%d", id),
				)
			}
		}(i)
	}

	wg.Wait()

	// Wait for batch timer to fire
	time.Sleep(100 * time.Millisecond)

	// Should have delivered at least one batch
	assert.Greater(t, batchCount.Load(), int64(0))
}

func TestAlertScanner_ConcurrentProcessLineAndStop(t *testing.T) {
	var delivered atomic.Int64
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  20 * time.Millisecond,
		DedupeWindow: 5 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			delivered.Add(int64(len(batch.Matches)))
		},
	})

	var wg sync.WaitGroup

	// Writers sending lines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				scanner.ProcessLine(
					fmt.Sprintf("panic: error %d-%d", id, j),
					"test-script",
				)
			}
		}(i)
	}

	// Stop after a brief delay while writes are still happening
	time.Sleep(5 * time.Millisecond)
	scanner.Stop()

	wg.Wait()

	// No deadlock or panic should occur. Stop delivers pending alerts.
}

func TestAlertScanner_ConcurrentEnableDisable(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 10 * time.Millisecond,
		OnAlert:      func(batch *AlertBatch) {},
	})
	defer scanner.Stop()

	var wg sync.WaitGroup

	// Toggle enabled state while processing lines
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				scanner.SetEnabled(j%2 == 0)
			}
		}()
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				scanner.ProcessLine(
					fmt.Sprintf("error: test %d-%d", id, j),
					"script",
				)
			}
		}(i)
	}

	wg.Wait()
}

func TestAlertScanner_ConcurrentAddDisablePattern(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 10 * time.Millisecond,
		OnAlert:      func(batch *AlertBatch) {},
	})
	defer scanner.Stop()

	var wg sync.WaitGroup

	// Add patterns while processing lines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			scanner.AddPattern(&AlertPattern{
				ID:       fmt.Sprintf("custom-%d", id),
				Pattern:  DefaultAlertPatterns()[0].Pattern,
				Severity: AlertSeverityWarning,
				Category: "test",
			})
		}(i)
	}

	// Disable patterns concurrently
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			scanner.DisablePattern(fmt.Sprintf("custom-%d", id))
		}(i)
	}

	// Process lines concurrently
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				scanner.ProcessLine("panic: test error", "script")
			}
		}(i)
	}

	wg.Wait()
}

func TestAlertScanner_ConcurrentRecentMatches(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  100 * time.Millisecond,
		DedupeWindow: 5 * time.Millisecond,
		OnAlert:      func(batch *AlertBatch) {},
	})
	defer scanner.Stop()

	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				scanner.ProcessLine(
					fmt.Sprintf("panic: concurrent error %d-%d", id, j),
					fmt.Sprintf("script-%d", id),
				)
			}
		}(i)
	}

	// Readers of ring buffer
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_ = scanner.RecentMatches(time.Time{})
				_ = scanner.RecentMatches(time.Now().Add(-1 * time.Second))
			}
		}()
	}

	wg.Wait()

	// Ring buffer should contain matches
	matches := scanner.RecentMatches(time.Time{})
	assert.Greater(t, len(matches), 0)
}

func TestAlertScanner_ConcurrentWithActivityState(t *testing.T) {
	var activityState atomic.Int32

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  30 * time.Millisecond,
		DedupeWindow: 5 * time.Millisecond,
		ActivityState: func() ActivityState {
			if activityState.Load() == 1 {
				return ActivityActive
			}
			return ActivityIdle
		},
		OnAlert: func(batch *AlertBatch) {},
	})
	defer scanner.Stop()

	var wg sync.WaitGroup

	// Toggle activity state
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				activityState.Store(int32(j % 2))
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Process lines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				scanner.ProcessLine(
					fmt.Sprintf("error: activity test %d-%d", id, j),
					"script",
				)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// Wait for batch delivery
	time.Sleep(200 * time.Millisecond)
}

// --- Race Tests for AlertBatch ---

func TestAlertBatch_ConcurrentAccess(t *testing.T) {
	patterns := DefaultAlertPatterns()
	if len(patterns) == 0 {
		t.Skip("no default patterns available")
	}

	batch := &AlertBatch{
		ScriptID: "test",
		Matches: []*AlertMatch{
			{Pattern: patterns[0], Line: "error 1", Timestamp: time.Now(), ScriptID: "test"},
			{Pattern: patterns[0], Line: "error 2", Timestamp: time.Now(), ScriptID: "test"},
		},
	}

	var wg sync.WaitGroup

	// Concurrent reads of batch properties
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = batch.MaxSeverity()
			_ = batch.Format()
		}()
	}

	wg.Wait()
}

// --- Race Tests for ActivityMonitor ---

func TestActivityMonitor_ConcurrentWriteAndState(t *testing.T) {
	am := NewActivityMonitor(io.Discard, ActivityMonitorConfig{
		IdleTimeout: 100 * time.Millisecond,
	})

	var wg sync.WaitGroup

	// Concurrent writes (simulating PTY output)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				data := []byte(fmt.Sprintf("output from goroutine %d iteration %d\n", id, j))
				_, _ = am.Write(data)
			}
		}(i)
	}

	// Concurrent state reads
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = am.State()
			}
		}()
	}

	wg.Wait()
}

// --- Race Tests for OutputGate ---

func TestOutputGate_ConcurrentFreezeWrite(t *testing.T) {
	gate := NewOutputGate(nil)

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = gate.Write([]byte(fmt.Sprintf("data-%d-%d\n", id, j)))
			}
		}(i)
	}

	// Concurrent freeze/unfreeze
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				gate.Freeze()
				time.Sleep(time.Millisecond)
				gate.Unfreeze()
			}
		}()
	}

	wg.Wait()
}
