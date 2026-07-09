package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGoTrackedDaemon(t *testing.T) *Daemon {
	t.Helper()
	return NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})
}

// A goroutine started before Stop must finish before Stop returns: that is the
// whole reason these spawns are tracked by d.wg.
func TestGoTracked_StopWaitsForInFlightGoroutine(t *testing.T) {
	d := newGoTrackedDaemon(t)

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})

	require.True(t, d.goTracked(func() {
		close(started)
		<-release
		close(finished)
	}), "goTracked should start the goroutine before shutdown")

	<-started

	stopped := make(chan struct{})
	go func() {
		_ = d.Stop(context.Background())
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned while a tracked goroutine was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	<-finished

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned after the tracked goroutine finished")
	}
}

// After Stop has begun, goTracked must decline: Stop reaps those resources
// itself, and a goroutine started after its wg.Wait would run against managers
// that are already torn down. The unguarded Add it replaced could also race
// Wait, which is a WaitGroup misuse panic.
func TestGoTracked_DeclinesAfterShutdownBegins(t *testing.T) {
	d := newGoTrackedDaemon(t)
	require.NoError(t, d.Stop(context.Background()))

	var ran bool
	started := d.goTracked(func() { ran = true })

	assert.False(t, started, "goTracked started a goroutine after Stop")
	time.Sleep(50 * time.Millisecond)
	assert.False(t, ran, "the declined goroutine ran anyway")
}

// Concurrent spawns racing Stop must never panic ("WaitGroup misuse: Add called
// concurrently with Wait") and must never leave a goroutine running past Stop.
func TestGoTracked_ConcurrentSpawnsRacingStopDoNotPanic(t *testing.T) {
	d := newGoTrackedDaemon(t)

	var live sync.WaitGroup // counts goroutines that actually started
	var mu sync.Mutex
	runningAfterStop := 0
	stopReturned := false

	var spawners sync.WaitGroup
	for i := 0; i < 32; i++ {
		spawners.Add(1)
		go func() {
			defer spawners.Done()
			live.Add(1)
			if !d.goTracked(func() {
				mu.Lock()
				if stopReturned {
					runningAfterStop++
				}
				mu.Unlock()
				live.Done()
			}) {
				live.Done()
			}
		}()
	}

	_ = d.Stop(context.Background())
	mu.Lock()
	stopReturned = true
	mu.Unlock()

	spawners.Wait()
	live.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, runningAfterStop, "a tracked goroutine ran after Stop returned")
}
