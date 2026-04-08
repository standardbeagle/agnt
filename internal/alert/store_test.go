package alert

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessAlertStore_AddAndQuery(t *testing.T) {
	store := NewProcessAlertStore(100)

	now := time.Now()
	entries := []*AlertEntry{
		{PatternID: "go-panic", Severity: "error", Category: "go", Description: "Go panic", Line: "panic: runtime error", ScriptID: "app:dev", Timestamp: now.Add(-2 * time.Minute)},
		{PatternID: "go-warn", Severity: "warning", Category: "go", Description: "Data race", Line: "WARNING: DATA RACE", ScriptID: "app:dev", Timestamp: now.Add(-1 * time.Minute)},
		{PatternID: "webpack-err", Severity: "error", Category: "webpack", Description: "Build error", Line: "ERROR in ./src/App.tsx", ScriptID: "web:dev", Timestamp: now},
	}

	for _, e := range entries {
		store.Add(e)
	}

	assert.Equal(t, 3, store.Len())

	// Query all
	result := store.Query(AlertStoreFilter{})
	require.Len(t, result, 3)

	// Verify oldest-to-newest order
	assert.Equal(t, "go-panic", result[0].PatternID)
	assert.Equal(t, "go-warn", result[1].PatternID)
	assert.Equal(t, "webpack-err", result[2].PatternID)
}

func TestProcessAlertStore_QueryWithSinceFilter(t *testing.T) {
	store := NewProcessAlertStore(100)

	now := time.Now()
	store.Add(&AlertEntry{PatternID: "old", Severity: "error", Timestamp: now.Add(-10 * time.Minute)})
	store.Add(&AlertEntry{PatternID: "recent", Severity: "error", Timestamp: now.Add(-30 * time.Second)})
	store.Add(&AlertEntry{PatternID: "newest", Severity: "warning", Timestamp: now})

	// Query since 5 minutes ago
	result := store.Query(AlertStoreFilter{Since: now.Add(-5 * time.Minute)})
	require.Len(t, result, 2)
	assert.Equal(t, "recent", result[0].PatternID)
	assert.Equal(t, "newest", result[1].PatternID)
}

func TestProcessAlertStore_QueryWithProcessFilter(t *testing.T) {
	store := NewProcessAlertStore(100)

	now := time.Now()
	store.Add(&AlertEntry{PatternID: "a", ScriptID: "app:dev", Timestamp: now})
	store.Add(&AlertEntry{PatternID: "b", ScriptID: "web:dev", Timestamp: now})
	store.Add(&AlertEntry{PatternID: "c", ScriptID: "app:dev", Timestamp: now})

	result := store.Query(AlertStoreFilter{ProcessID: "app:dev"})
	require.Len(t, result, 2)
	assert.Equal(t, "a", result[0].PatternID)
	assert.Equal(t, "c", result[1].PatternID)

	result = store.Query(AlertStoreFilter{ProcessID: "web:dev"})
	require.Len(t, result, 1)
	assert.Equal(t, "b", result[0].PatternID)
}

func TestProcessAlertStore_QueryWithSeverityFilter(t *testing.T) {
	store := NewProcessAlertStore(100)

	now := time.Now()
	store.Add(&AlertEntry{PatternID: "a", Severity: "error", Timestamp: now})
	store.Add(&AlertEntry{PatternID: "b", Severity: "warning", Timestamp: now})
	store.Add(&AlertEntry{PatternID: "c", Severity: "error", Timestamp: now})

	result := store.Query(AlertStoreFilter{Severity: "error"})
	require.Len(t, result, 2)
	assert.Equal(t, "a", result[0].PatternID)
	assert.Equal(t, "c", result[1].PatternID)
}

func TestProcessAlertStore_QueryWithLimit(t *testing.T) {
	store := NewProcessAlertStore(100)

	now := time.Now()
	for i := 0; i < 10; i++ {
		store.Add(&AlertEntry{PatternID: "entry", Severity: "error", Timestamp: now})
	}

	result := store.Query(AlertStoreFilter{Limit: 3})
	require.Len(t, result, 3)
}

func TestProcessAlertStore_RingBufferOverflow(t *testing.T) {
	store := NewProcessAlertStore(5)

	now := time.Now()
	// Add 8 entries to a size-5 buffer
	for i := 0; i < 8; i++ {
		store.Add(&AlertEntry{
			PatternID: "entry",
			Severity:  "error",
			Line:      time.Duration(i).String(),
			Timestamp: now.Add(time.Duration(i) * time.Second),
		})
	}

	assert.Equal(t, 5, store.Len())

	// Should only have the 5 most recent (indices 3-7)
	result := store.Query(AlertStoreFilter{})
	require.Len(t, result, 5)

	// Verify oldest-to-newest: entries 3,4,5,6,7
	assert.Equal(t, now.Add(3*time.Second).Unix(), result[0].Timestamp.Unix())
	assert.Equal(t, now.Add(7*time.Second).Unix(), result[4].Timestamp.Unix())
}

func TestProcessAlertStore_Clear(t *testing.T) {
	store := NewProcessAlertStore(100)

	now := time.Now()
	store.Add(&AlertEntry{PatternID: "a", Severity: "error", Timestamp: now})
	store.Add(&AlertEntry{PatternID: "b", Severity: "warning", Timestamp: now})

	assert.Equal(t, 2, store.Len())

	store.Clear()

	assert.Equal(t, 0, store.Len())
	result := store.Query(AlertStoreFilter{})
	assert.Empty(t, result)
}

func TestProcessAlertStore_AddNil(t *testing.T) {
	store := NewProcessAlertStore(100)
	store.Add(nil)
	assert.Equal(t, 0, store.Len())
}

func TestProcessAlertStore_DefaultMaxSize(t *testing.T) {
	store := NewProcessAlertStore(0)
	assert.Equal(t, 500, store.maxSize)
}

func TestProcessAlertStore_CombinedFilters(t *testing.T) {
	store := NewProcessAlertStore(100)

	now := time.Now()
	store.Add(&AlertEntry{PatternID: "a", Severity: "error", ScriptID: "app:dev", Timestamp: now.Add(-10 * time.Minute)})
	store.Add(&AlertEntry{PatternID: "b", Severity: "error", ScriptID: "app:dev", Timestamp: now})
	store.Add(&AlertEntry{PatternID: "c", Severity: "warning", ScriptID: "app:dev", Timestamp: now})
	store.Add(&AlertEntry{PatternID: "d", Severity: "error", ScriptID: "web:dev", Timestamp: now})

	// Filter: errors from app:dev in last 5 minutes
	result := store.Query(AlertStoreFilter{
		Since:     now.Add(-5 * time.Minute),
		ProcessID: "app:dev",
		Severity:  "error",
	})
	require.Len(t, result, 1)
	assert.Equal(t, "b", result[0].PatternID)
}
