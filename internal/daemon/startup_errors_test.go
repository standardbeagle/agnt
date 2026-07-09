package daemon

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mkEntry(id, level string, ts time.Time) *StartupLogEntry {
	return &StartupLogEntry{ProcessID: id, Level: level, EventType: "started", Message: id, Timestamp: ts}
}

func TestNewStartupLogStore_DefaultMaxSize(t *testing.T) {
	for _, sz := range []int{0, -1, -100} {
		s := NewStartupLogStore(sz)
		assert.Len(t, s.entries, 100, "non-positive maxSize %d defaults to 100", sz)
		assert.Equal(t, 100, s.maxSize)
	}
	s := NewStartupLogStore(7)
	assert.Len(t, s.entries, 7)
	assert.Equal(t, 7, s.maxSize)
}

func TestStartupLogStore_WraparoundOrdering(t *testing.T) {
	s := NewStartupLogStore(3)
	base := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		s.Add(mkEntry(fmt.Sprintf("p%d", i), "info", base.Add(time.Duration(i)*time.Second)))
	}
	got := s.Query(StartupLogFilter{})

	require.Equal(t, 3, s.Len(), "ring caps at maxSize")
	require.Len(t, got, 3, "only 3 retained")
	// Oldest two (p0,p1) evicted; remaining oldest->newest p2,p3,p4.
	assert.Equal(t, "p2", got[0].ProcessID)
	assert.Equal(t, "p3", got[1].ProcessID)
	assert.Equal(t, "p4", got[2].ProcessID)
	// Timestamps strictly increasing (ordering preserved through wraparound).
	assert.True(t, got[0].Timestamp.Before(got[1].Timestamp))
	assert.True(t, got[1].Timestamp.Before(got[2].Timestamp))
}

func TestStartupLogStore_QueryTailLimit(t *testing.T) {
	s := NewStartupLogStore(10)
	base := time.Unix(2000, 0)
	for i := 0; i < 6; i++ {
		s.Add(mkEntry(fmt.Sprintf("p%d", i), "info", base.Add(time.Duration(i)*time.Second)))
	}
	got := s.Query(StartupLogFilter{Limit: 2})

	require.Len(t, got, 2, "limit returns newest 2")
	assert.Equal(t, "p4", got[0].ProcessID, "still oldest->newest within the tail window")
	assert.Equal(t, "p5", got[1].ProcessID)

	all := s.Query(StartupLogFilter{Limit: 0})
	assert.Len(t, all, 6, "limit 0 returns everything")
}

func TestStartupLogStore_FilterCombos(t *testing.T) {
	s := NewStartupLogStore(10)
	base := time.Unix(3000, 0)
	s.Add(mkEntry("a", "info", base))
	s.Add(mkEntry("b", "error", base.Add(time.Second)))
	s.Add(mkEntry("a", "error", base.Add(2*time.Second)))
	s.Add(mkEntry("c", "info", base.Add(3*time.Second)))

	byProc := s.Query(StartupLogFilter{ProcessID: "a"})
	require.Len(t, byProc, 2)
	for _, e := range byProc {
		assert.Equal(t, "a", e.ProcessID)
	}

	byLevel := s.Query(StartupLogFilter{Level: "error"})
	require.Len(t, byLevel, 2)
	for _, e := range byLevel {
		assert.Equal(t, "error", e.Level)
	}

	since := s.Query(StartupLogFilter{Since: base.Add(2 * time.Second)})
	require.Len(t, since, 2, "only entries at/after the Since cutoff")
	assert.Equal(t, "a", since[0].ProcessID)
	assert.Equal(t, "c", since[1].ProcessID)

	combo := s.Query(StartupLogFilter{ProcessID: "a", Level: "error"})
	require.Len(t, combo, 1)
	assert.Equal(t, "a", combo[0].ProcessID)
	assert.Equal(t, "error", combo[0].Level)

	assert.Len(t, s.Query(StartupLogFilter{}), 4, "empty filter returns all")
}

func TestStartupLogStore_NilEntryHandling(t *testing.T) {
	s := NewStartupLogStore(5)
	lenBefore := s.Len()
	s.Add(nil)
	assert.Equal(t, lenBefore, s.Len(), "Add(nil) is a no-op")

	// Defensive: a nil slot WITHIN the active range is skipped by Query.
	// Add cannot produce this, so inject it via same-package field access:
	// real entry at index 0, then force len=2 with index 1 left nil.
	s.Add(mkEntry("x", "info", time.Unix(4000, 0)))
	s.entries[1] = nil
	s.len = 2
	got := s.Query(StartupLogFilter{})
	require.Len(t, got, 1, "nil slot in active range skipped, real entry returned")
	assert.Equal(t, "x", got[0].ProcessID)
}

func TestStartupLogStore_ClearReset(t *testing.T) {
	s := NewStartupLogStore(5)
	for i := 0; i < 4; i++ {
		s.Add(mkEntry(fmt.Sprintf("p%d", i), "info", time.Unix(int64(5000+i), 0)))
	}
	require.Equal(t, 4, s.Len())

	s.Clear()
	assert.Equal(t, 0, s.Len(), "Len zero after Clear")
	assert.Empty(t, s.Query(StartupLogFilter{}), "Query empty after Clear")

	// Reusable after Clear.
	s.Add(mkEntry("fresh", "info", time.Unix(6000, 0)))
	got := s.Query(StartupLogFilter{})
	require.Len(t, got, 1)
	assert.Equal(t, "fresh", got[0].ProcessID)
}

func TestStartupLogStore_InfoAndErrorHelpers(t *testing.T) {
	s := NewStartupLogStore(5)
	s.Info("p1", "dev", "started", "up")
	s.Error("p2", "api", "start_failed", "boom")

	got := s.Query(StartupLogFilter{})
	require.Len(t, got, 2)
	assert.Equal(t, "info", got[0].Level)
	assert.Equal(t, "started", got[0].EventType)
	assert.Equal(t, "error", got[1].Level)
	assert.Equal(t, "p2", got[1].ProcessID)
	assert.False(t, got[0].Timestamp.IsZero(), "helpers stamp a timestamp")
}

func TestStartupLogStore_Recent(t *testing.T) {
	s := NewStartupLogStore(10)
	now := time.Now()
	// One old (outside window), two recent.
	s.Add(mkEntry("old", "info", now.Add(-time.Hour)))
	s.Add(mkEntry("r1", "info", now.Add(-time.Second)))
	s.Add(mkEntry("r2", "info", now))

	got := s.Recent(time.Minute, 0)
	require.Len(t, got, 2, "only entries within the window")
	assert.Equal(t, "r1", got[0].ProcessID)
	assert.Equal(t, "r2", got[1].ProcessID)

	assert.Len(t, s.Recent(time.Minute, 1), 1, "limit applied to recent window")
}

// TestStartupLogger_StampsProjectScope proves the prefactor's core guarantee:
// every entry recorded through the project-bound logger carries the project's
// "basename-hash:" ProcessID prefix, so it survives a project-scoped query —
// including project-level events recorded with an empty name. The control
// (a raw store entry with an empty ProcessID, the old footgun) is invisible to
// the scoped query and only reachable globally.
func TestStartupLogger_StampsProjectScope(t *testing.T) {
	projectPath := "/home/u/proj"
	store := NewStartupLogStore(100)
	log := &startupLogger{store: store, projectPath: projectPath}

	log.Error("", "config_error", "bad kdl")                            // project-level, no name
	log.Error("api", "start_failed", "boom")                            // named script
	log.ErrorPort("web", "proxy_listen_port_conflict", "port busy", 80) // named proxy + port

	// Control: the pre-fix footgun — a project-relevant event recorded with an
	// empty ProcessID. It must NOT appear in a scoped query.
	store.Error("", "ghost", "legacy_unstamped", "invisible to scope")

	prefix := makeProcessID(projectPath, "")
	scoped := store.Query(StartupLogFilter{ProjectPath: projectPath})
	require.Len(t, scoped, 3, "every logger entry is project-scoped; the unstamped control is excluded")

	var sawPort bool
	for _, e := range scoped {
		assert.True(t, strings.HasPrefix(e.ProcessID, prefix),
			"entry %q must carry the project prefix %q", e.ProcessID, prefix)
		assert.NotEqual(t, "legacy_unstamped", e.EventType,
			"unstamped raw entry leaked into the scoped query")
		if e.EventType == "proxy_listen_port_conflict" {
			assert.Equal(t, 80, e.Port, "ErrorPort preserves the conflicting port")
			assert.Equal(t, "web", e.ScriptName, "name is stamped onto ScriptName")
			sawPort = true
		}
	}
	assert.True(t, sawPort, "port-bearing entry present in scoped view")

	// The unstamped control is still reachable via an unscoped (global) query.
	assert.Len(t, store.Query(StartupLogFilter{}), 4, "global query sees all four entries")
}

func TestLogSessionTargetStarting_IsProjectScoped(t *testing.T) {
	projectPath := "/home/u/proj"
	store := NewStartupLogStore(100)
	d := &Daemon{startupErrorStore: store}

	d.logSessionTargetStarting(&Session{
		ProjectPath: projectPath,
		Command:     "cdsp",
		Args:        []string{"--model", "sonnet"},
	})

	scoped := store.Query(StartupLogFilter{ProjectPath: projectPath})
	require.Len(t, scoped, 1)
	assert.Equal(t, "target_starting", scoped[0].EventType)
	assert.Equal(t, "starting cdsp --model sonnet", scoped[0].Message)
	assert.True(t, strings.HasPrefix(scoped[0].ProcessID, makeProcessID(projectPath, "")))
}

// TestStartupLogger_NilSafe verifies the logger tolerates a nil receiver / nil
// store (the scheduleFallbackPortChecks path used to guard the store for nil).
func TestStartupLogger_NilSafe(t *testing.T) {
	var nilLogger *startupLogger
	assert.NotPanics(t, func() { nilLogger.Error("x", "e", "m") })

	emptyLogger := &startupLogger{store: nil, projectPath: "/p"}
	assert.NotPanics(t, func() { emptyLogger.Info("x", "e", "m") })
}

func TestDaemonStartupLogIsGlobalOnly(t *testing.T) {
	projectPath := "/home/u/proj"
	store := NewStartupLogStore(100)
	d := &Daemon{startupErrorStore: store}

	d.daemonStartupLog("info", "daemon_starting", "daemon bootstrap starting")
	d.daemonStartupLog("info", "daemon_commands_registered", "daemon hub commands registered")
	d.daemonStartupLog("info", "hub_starting", "starting daemon hub")
	d.daemonStartupLog("info", "hub_started", "daemon hub listening")

	global := store.Query(StartupLogFilter{})
	require.Len(t, global, 4)
	assert.Equal(t, "daemon_starting", global[0].EventType)
	assert.Equal(t, "daemon_commands_registered", global[1].EventType)
	assert.Equal(t, "hub_starting", global[2].EventType)
	assert.Equal(t, "hub_started", global[3].EventType)

	scoped := store.Query(StartupLogFilter{ProjectPath: projectPath})
	assert.Empty(t, scoped, "daemon lifecycle entries are not project-scoped")
}

func TestStartupLogStore_Concurrent(t *testing.T) {
	s := NewStartupLogStore(64)
	const writers, perWriter = 8, 100
	var wg sync.WaitGroup
	wg.Add(writers + 2)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				s.Add(mkEntry(fmt.Sprintf("w%d", w), "info", time.Unix(int64(i), 0)))
			}
		}(w)
	}
	for r := 0; r < 2; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = s.Query(StartupLogFilter{})
				_ = s.Len()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 64, s.Len(), "ring saturated at maxSize, no race-induced corruption")
	assert.LessOrEqual(t, len(s.Query(StartupLogFilter{})), 64)
}
