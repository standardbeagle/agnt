package alert

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func entryAt(script, project, line string, ts time.Time) *AlertEntry {
	return &AlertEntry{
		PatternID:   "p",
		Severity:    "error",
		Category:    "go",
		Description: "desc",
		Line:        line,
		ScriptID:    script,
		ProjectPath: project,
		Timestamp:   ts,
	}
}

func TestClearProcessBefore_TimestampBoundary(t *testing.T) {
	s := NewProcessAlertStore(10)
	base := time.Now()
	s.Add(entryAt("web", "/p", "old-1", base.Add(-2*time.Minute)))
	s.Add(entryAt("web", "/p", "old-2", base.Add(-1*time.Minute)))
	s.Add(entryAt("api", "/p", "other-proc", base.Add(-1*time.Minute)))
	s.Add(entryAt("web", "/p", "after-success", base.Add(time.Second)))

	removed := s.ClearProcessBefore("web", base)
	assert.Equal(t, 2, removed, "only web entries at/before boundary clear")
	assert.Equal(t, 2, s.Len(), "Len must track the compaction")

	left := s.Query(AlertStoreFilter{})
	require.Len(t, left, 2)
	lines := []string{left[0].Line, left[1].Line}
	assert.Contains(t, lines, "other-proc")
	assert.Contains(t, lines, "after-success")

	// Clearing again is a no-op.
	assert.Zero(t, s.ClearProcessBefore("web", base))
}

func TestClearForProcess_CompactsLen(t *testing.T) {
	s := NewProcessAlertStore(4)
	now := time.Now()
	// Overfill so the ring wraps, then clear — Len must stay truthful.
	for i := 0; i < 6; i++ {
		s.Add(entryAt("web", "/p", fmt.Sprintf("l%d", i), now))
	}
	require.Equal(t, 4, s.Len())
	assert.Equal(t, 4, s.ClearForProcess("web"))
	assert.Zero(t, s.Len())
	assert.Empty(t, s.Query(AlertStoreFilter{}))

	// Ring still functional after compaction.
	s.Add(entryAt("web", "/p", "fresh", now))
	assert.Equal(t, 1, s.Len())
}

func TestClearProject_ScopedToProject(t *testing.T) {
	s := NewProcessAlertStore(10)
	now := time.Now()
	s.Add(entryAt("a", "/proj-one", "one", now))
	s.Add(entryAt("b", "/proj-two", "two", now))

	assert.Equal(t, 1, s.ClearProject("/proj-one"))
	left := s.Query(AlertStoreFilter{})
	require.Len(t, left, 1)
	assert.Equal(t, "/proj-two", left[0].ProjectPath)
}

func TestAdd_StampsUnifiedID(t *testing.T) {
	s := NewProcessAlertStore(10)
	e := entryAt("web", "/p", "boom", time.Now())
	s.Add(e)
	require.NotEmpty(t, e.ID)
	assert.Len(t, e.ID, 8)
	assert.Equal(t, e.UnifiedID(), e.ID)

	// Lifecycle entries merge description+line the way get_errors renders them.
	lifecycle := &AlertEntry{Category: "process_lifecycle", Description: "exited (code 1)", Line: "panic: boom", ScriptID: "web"}
	plain := &AlertEntry{Category: "process_lifecycle", Description: "exited (code 1)", ScriptID: "web"}
	assert.NotEqual(t, lifecycle.UnifiedID(), plain.UnifiedID())
}

func TestPinnedStore_PinUnpinListCap(t *testing.T) {
	ps := NewPinnedStore()

	require.Error(t, ps.Pin(PinnedError{}), "empty id must fail loud")

	p := PinnedError{ID: "abc", ProjectPath: "/p", Message: "boom", Tag: "keep"}
	require.NoError(t, ps.Pin(p))

	// Re-pin updates the tag, no duplicate.
	p.Tag = "updated"
	require.NoError(t, ps.Pin(p))
	list := ps.List("/p", false)
	require.Len(t, list, 1)
	assert.Equal(t, "updated", list[0].Tag)
	assert.False(t, list[0].PinnedAt.IsZero())

	// Project isolation: another project sees nothing non-globally.
	assert.Empty(t, ps.List("/other", false))
	require.NoError(t, ps.Pin(PinnedError{ID: "zzz", ProjectPath: "/other"}))
	assert.Len(t, ps.List("/p", true), 2, "global list spans projects")

	// Unpin.
	assert.True(t, ps.Unpin("/p", "abc"))
	assert.False(t, ps.Unpin("/p", "abc"), "second unpin reports not-found")
	assert.Empty(t, ps.List("/p", false))

	// Cap fails loud, and re-pinning an existing id still works at cap.
	for i := 0; i < MaxPinnedPerProject; i++ {
		require.NoError(t, ps.Pin(PinnedError{ID: fmt.Sprintf("id-%d", i), ProjectPath: "/cap"}))
	}
	err := ps.Pin(PinnedError{ID: "overflow", ProjectPath: "/cap"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pin cap")
	require.NoError(t, ps.Pin(PinnedError{ID: "id-0", ProjectPath: "/cap", Tag: "retag"}), "existing id bypasses cap")
}
