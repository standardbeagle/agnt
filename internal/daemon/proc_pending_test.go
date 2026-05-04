package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingProcessTracker_RegisterAndGet(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()

	snapshot := tracker.Register(PendingProcess{
		ProcessID:   "/proj/api",
		Name:        "api",
		ProjectPath: "/proj",
		Command:     "go run ./cmd/api",
		Deadline:    time.Now().Add(30 * time.Second),
	}, []string{"db", "redis"})

	assert.Equal(t, []string{"db", "redis"}, snapshot.WaitingFor,
		"WaitingFor should be sorted alphabetically")
	assert.Equal(t, PendingWaiting, snapshot.State)
	assert.Equal(t, "/proj/api", snapshot.ProcessID)

	got, ok := tracker.Get("/proj/api")
	require.True(t, ok)
	assert.Equal(t, []string{"db", "redis"}, got.WaitingFor)
	assert.Equal(t, "go run ./cmd/api", got.Command)
}

func TestPendingProcessTracker_GetMissing(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	_, ok := tracker.Get("nonexistent")
	assert.False(t, ok)
}

func TestPendingProcessTracker_MarkReady(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	tracker.Register(PendingProcess{ProcessID: "api"}, []string{"db", "redis"})

	remaining := tracker.MarkReady("api", "db")
	assert.Equal(t, 1, remaining)

	got, _ := tracker.Get("api")
	assert.Equal(t, []string{"redis"}, got.WaitingFor)

	remaining = tracker.MarkReady("api", "redis")
	assert.Equal(t, 0, remaining)

	got, _ = tracker.Get("api")
	assert.Empty(t, got.WaitingFor)
}

func TestPendingProcessTracker_MarkReadyUnknown(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	// Should not panic.
	remaining := tracker.MarkReady("missing", "dep")
	assert.Equal(t, 0, remaining)
}

func TestPendingProcessTracker_MarkFailed(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	tracker.Register(PendingProcess{ProcessID: "api"}, []string{"db"})

	tracker.MarkFailed("api", "db")

	got, ok := tracker.Get("api")
	require.True(t, ok)
	assert.Equal(t, PendingFailed, got.State)
	assert.Equal(t, "dependency_timeout:db", got.FailureReason)
}

func TestPendingProcessTracker_Remove(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	tracker.Register(PendingProcess{ProcessID: "api"}, []string{"db"})

	tracker.Remove("api")

	_, ok := tracker.Get("api")
	assert.False(t, ok)
}

func TestPendingProcessTracker_RemoveUnknown(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	// Should not panic.
	tracker.Remove("never-registered")
}

func TestPendingProcessTracker_ListByProject(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	tracker.Register(PendingProcess{ProcessID: "/p1/api", ProjectPath: "/p1"}, []string{"db"})
	tracker.Register(PendingProcess{ProcessID: "/p1/web", ProjectPath: "/p1"}, []string{"api"})
	tracker.Register(PendingProcess{ProcessID: "/p2/svc", ProjectPath: "/p2"}, []string{"db"})

	all := tracker.ListByProject("")
	assert.Len(t, all, 3)

	p1 := tracker.ListByProject("/p1")
	assert.Len(t, p1, 2)
	assert.Equal(t, "/p1/api", p1[0].ProcessID)
	assert.Equal(t, "/p1/web", p1[1].ProcessID)

	p2 := tracker.ListByProject("/p2")
	assert.Len(t, p2, 1)
	assert.Equal(t, "/p2/svc", p2[0].ProcessID)

	none := tracker.ListByProject("/missing")
	assert.Empty(t, none)
}

func TestPendingProcessTracker_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			id := procIDForBench(i)
			tracker.Register(PendingProcess{ProcessID: id}, []string{"d1", "d2", "d3"})
			tracker.MarkReady(id, "d1")
			_, _ = tracker.Get(id)
			tracker.MarkReady(id, "d2")
			tracker.ListByProject("")
			tracker.MarkReady(id, "d3")
			tracker.Remove(id)
		}(i)
	}
	wg.Wait()

	// All entries should be removed.
	assert.Empty(t, tracker.ListByProject(""))
}

func TestPendingProcessTracker_ReregisterReplaces(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	tracker.Register(PendingProcess{ProcessID: "api"}, []string{"db"})

	// Re-register with new deps replaces the entry.
	tracker.Register(PendingProcess{ProcessID: "api"}, []string{"redis", "kafka"})

	got, _ := tracker.Get("api")
	assert.Equal(t, []string{"kafka", "redis"}, got.WaitingFor)
}

func TestPendingProcessTracker_GetSnapshotIsolation(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()
	tracker.Register(PendingProcess{ProcessID: "api"}, []string{"db", "redis"})

	got, _ := tracker.Get("api")
	// Mutate the returned slice.
	got.WaitingFor[0] = "MUTATED"

	// Tracker state must be unaffected.
	again, _ := tracker.Get("api")
	assert.Equal(t, []string{"db", "redis"}, again.WaitingFor,
		"snapshot mutation must not bleed back into tracker state")
}

// procIDForBench formats a stable process ID used by the concurrency test.
func procIDForBench(i int) string {
	return "proc-" + itoa(i)
}

// itoa avoids strconv import in this test file; tiny non-negative int formatter.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
