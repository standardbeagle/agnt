package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The heartbeat timeout was dead: CheckHeartbeats had no production caller, so
// SessionStatusDisconnected was unreachable. The sweep is what applies it.
func TestHeartbeatSweep_MarksStaleSessionDisconnected(t *testing.T) {
	t.Parallel()

	d := &Daemon{sessionRegistry: NewSessionRegistry(20 * time.Millisecond)}
	stale := time.Now().Add(-time.Hour)

	classic := &Session{Code: "claude-stale", Status: SessionStatusActive, LastSeen: stale}
	require.NoError(t, d.sessionRegistry.Register(classic))
	// A session-host session sends no heartbeat; the sweep must leave it alone.
	hosted := &Session{Code: "hosted-1", Kind: SessionKindSessionHost, Status: SessionStatusActive, LastSeen: stale}
	require.NoError(t, d.sessionRegistry.Register(hosted))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.runHeartbeatSweep(ctx, 5*time.Millisecond)
	}()

	require.Eventually(t, func() bool {
		return classic.GetStatus() == SessionStatusDisconnected
	}, 5*time.Second, 5*time.Millisecond, "a session with no recent heartbeat must become disconnected")
	assert.Equal(t, SessionStatusActive, hosted.GetStatus(), "session-host sessions have no heartbeat and never go stale")

	// A heartbeat revives it: the sweep flips status, it does not retire.
	require.NoError(t, d.sessionRegistry.Heartbeat("claude-stale"))
	assert.Equal(t, SessionStatusActive, classic.GetStatus())

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat sweep did not stop when its context was cancelled")
	}
}

// Persistence was write-only: Start restores from knownProjects, which is empty
// on a fresh daemon, so nothing a previous lifetime saved ever came back.
func TestScheduler_RestoreProject_LoadsPersistedTasks(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()

	// First daemon lifetime: schedule and persist.
	sm1 := NewSchedulerStateManager()
	reg1 := NewSessionRegistry(time.Minute)
	require.NoError(t, reg1.Register(&Session{Code: "claude-1", ProjectPath: projectDir, Status: SessionStatusActive, LastSeen: time.Now()}))
	s1 := NewScheduler(DefaultSchedulerConfig(), reg1, sm1)
	task, err := s1.Schedule("claude-1", time.Hour, "wake up", projectDir)
	require.NoError(t, err)
	require.NoError(t, sm1.Close())

	// Second lifetime: a fresh manager knows no projects.
	sm2 := NewSchedulerStateManager()
	t.Cleanup(func() { _ = sm2.Close() })
	s2 := NewScheduler(DefaultSchedulerConfig(), NewSessionRegistry(time.Minute), sm2)
	require.Empty(t, s2.ListTasks("", true), "nothing is restored until the project's session registers")

	assert.Equal(t, 1, s2.RestoreProject(projectDir))
	restored := s2.ListTasks("", true)
	require.Len(t, restored, 1)
	assert.Equal(t, task.ID, restored[0].ID)
	assert.Equal(t, "wake up", restored[0].Message)
	assert.Equal(t, "claude-1", restored[0].SessionCode)

	// Every session register calls it; a second call must not duplicate.
	assert.Equal(t, 0, s2.RestoreProject(projectDir))
	assert.Len(t, s2.ListTasks("", true), 1)

	// The ID counter moved past the restored ID.
	reg2 := NewSessionRegistry(time.Minute)
	require.NoError(t, reg2.Register(&Session{Code: "claude-2", ProjectPath: projectDir, Status: SessionStatusActive, LastSeen: time.Now()}))
	s2.registry = reg2
	fresh, err := s2.Schedule("claude-2", time.Hour, "second", projectDir)
	require.NoError(t, err)
	assert.NotEqual(t, task.ID, fresh.ID)
}

// A restored task whose ID this lifetime already issued is re-keyed, not lost.
func TestScheduler_RestoreProject_RekeysCollidingID(t *testing.T) {
	t.Parallel()
	projectA := t.TempDir()
	projectB := t.TempDir()

	sm1 := NewSchedulerStateManager()
	reg1 := NewSessionRegistry(time.Minute)
	require.NoError(t, reg1.Register(&Session{Code: "a-1", ProjectPath: projectA, Status: SessionStatusActive, LastSeen: time.Now()}))
	s1 := NewScheduler(DefaultSchedulerConfig(), reg1, sm1)
	old, err := s1.Schedule("a-1", time.Hour, "from project A", projectA)
	require.NoError(t, err)
	require.NoError(t, sm1.Close())

	sm2 := NewSchedulerStateManager()
	t.Cleanup(func() { _ = sm2.Close() })
	reg2 := NewSessionRegistry(time.Minute)
	require.NoError(t, reg2.Register(&Session{Code: "b-1", ProjectPath: projectB, Status: SessionStatusActive, LastSeen: time.Now()}))
	s2 := NewScheduler(DefaultSchedulerConfig(), reg2, sm2)
	// Project B schedules first and is handed the same ID.
	taken, err := s2.Schedule("b-1", time.Hour, "from project B", projectB)
	require.NoError(t, err)
	require.Equal(t, old.ID, taken.ID, "precondition: the new lifetime reissued the persisted ID")

	assert.Equal(t, 1, s2.RestoreProject(projectA))
	messages := map[string]string{}
	for _, task := range s2.ListTasks("", true) {
		messages[task.Message] = task.ID
	}
	require.Len(t, messages, 2, "both tasks must survive the ID collision")
	assert.NotEqual(t, messages["from project A"], messages["from project B"])
}

// A scheduled message outlived its session and was delivered to whichever
// session later registered under the same code.
func TestScheduler_CancelSession_CancelsOnlyThatSessionsPendingTasks(t *testing.T) {
	t.Parallel()
	scheduler, registry, cleanup := setupSchedulerTest(t)
	defer cleanup()
	require.NoError(t, registry.Register(&Session{Code: "other", ProjectPath: "/project", Status: SessionStatusActive, LastSeen: time.Now()}))

	mine, err := scheduler.Schedule("test-session", time.Hour, "mine", "/project")
	require.NoError(t, err)
	theirs, err := scheduler.Schedule("other", time.Hour, "theirs", "/project")
	require.NoError(t, err)

	assert.Equal(t, 1, scheduler.CancelSession("test-session"))
	_, found := scheduler.GetTask(mine.ID)
	assert.False(t, found, "the retired session's task is gone")
	_, found = scheduler.GetTask(theirs.ID)
	assert.True(t, found, "another session's task is untouched")
	assert.Equal(t, TaskStatusCancelled, mine.Status())
	assert.Equal(t, 0, scheduler.CancelSession(""), "an empty code cancels nothing")
}

// The message is typed into an agent's PTY. A task must not be delivered to a
// session that shares the code but sits in another project.
func TestScheduler_DeliverTask_RefusesSessionInAnotherProject(t *testing.T) {
	t.Parallel()
	registry := NewSessionRegistry(time.Minute)
	scheduler := NewScheduler(DefaultSchedulerConfig(), registry, nil)
	scheduler.ctx = context.Background()

	projectA := t.TempDir()
	projectB := t.TempDir()
	// The code is now held by a session in project B.
	require.NoError(t, registry.Register(&Session{
		Code: "claude-1", ProjectPath: normalizePath(projectB), OverlayPath: "/nonexistent/overlay.sock",
		Status: SessionStatusActive, LastSeen: time.Now(),
	}))

	task := NewScheduledTask("task-1", "claude-1", "for project A", normalizePath(projectA),
		time.Now().Add(-time.Second), time.Now(), TaskStatusDelivering)
	scheduler.tasks.Store(task.ID, task)

	scheduler.deliverTask(task)

	assert.Equal(t, 1, task.Attempts())
	assert.Contains(t, task.LastError(), "different project",
		"the failure must be the project mismatch, not an attempt to reach the overlay")
	assert.NotEqual(t, TaskStatusDelivered, task.Status())
}

// A dropped ScriptStopped event leaves a stopped script's proxies running. It
// was reported to the debug log only.
func TestEmitScriptStopped_ChannelFullIsRecorded(t *testing.T) {
	t.Parallel()
	d := &Daemon{
		proxyEvents:       make(chan ProxyEvent, 1),
		startupErrorStore: NewStartupLogStore(10),
	}
	d.proxyEvents <- ProxyEvent{Type: ScriptStopped, ScriptID: "filler"}

	d.emitScriptStopped("myapp-abc1:dev")

	var dropped []string
	for _, e := range d.startupErrorStore.Query(StartupLogFilter{Limit: 10}) {
		if e.EventType == "proxy_event_dropped" {
			dropped = append(dropped, e.ProcessID+"|"+e.Level)
		}
	}
	require.Equal(t, []string{"myapp-abc1:dev|warning"}, dropped)
	assert.True(t, strings.Contains(
		d.startupErrorStore.Query(StartupLogFilter{Limit: 10})[0].Message, "proxies may not be cleaned up"))
}

// Autostart errors carried no ProjectPath, so the default project-scoped
// ALERTS QUERY never returned them.
func TestAutostartErrors_AreStampedWithProject(t *testing.T) {
	t.Parallel()
	d := &Daemon{alertStore: NewProcessAlertStore(10)}
	projectDir := normalizePath(t.TempDir())

	emitAutostartErrorsToAlertStore(d, projectDir, &AutostartResult{Errors: []string{"script dev failed"}})

	scoped := d.alertStore.Query(AlertStoreFilter{ProjectPath: projectDir})
	require.Len(t, scoped, 1, "a project-scoped query must return the project's autostart errors")
	assert.Equal(t, "script dev failed", scoped[0].Description)
	assert.Empty(t, d.alertStore.Query(AlertStoreFilter{ProjectPath: normalizePath(t.TempDir())}),
		"and no other project's")
}
