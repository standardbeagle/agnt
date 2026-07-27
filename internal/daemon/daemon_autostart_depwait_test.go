package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
)

// depWaitDaemon builds the minimum daemon surface waitForSingleDependency
// touches: a startup log store and a ready signaler. Deliberately not a full
// daemon — this exercises message shape, not lifecycle.
func depWaitDaemon() *Daemon {
	return &Daemon{
		startupErrorStore: NewStartupLogStore(50),
		readySignaler:     NewReadySignaler(),
	}
}

func depWaitEntries(t *testing.T, d *Daemon, projectPath string) []*StartupLogEntry {
	t.Helper()
	return d.startupErrorStore.Query(StartupLogFilter{ProjectPath: projectPath})
}

func findEntry(entries []*StartupLogEntry, eventType string) *StartupLogEntry {
	for _, e := range entries {
		if e.EventType == eventType {
			return e
		}
	}
	return nil
}

// TestWaitForSingleDependency_TimeoutMessageIsActionable pins that an expired
// declared timeout is reported as a timeout — naming the dependency, the
// declared bound, and the fact that the dependent starts anyway. The old
// message said only "cancelled ... (starting anyway)" for every exit path,
// which gave no way to tell a configured timeout from a shutdown.
func TestWaitForSingleDependency_TimeoutMessageIsActionable(t *testing.T) {
	d := depWaitDaemon()
	projectPath := t.TempDir()
	dep := config.ScriptDependency{Name: "api", Timeout: 20 * time.Millisecond}

	d.waitForSingleDependency(context.Background(), "web", dep, projectPath, 1, nil)

	entries := depWaitEntries(t, d, projectPath)

	start := findEntry(entries, "dependency_wait_start")
	if start == nil {
		t.Fatal("no dependency_wait_start entry: the wait itself is invisible")
	}
	if !strings.Contains(start.Message, "timeout=20ms") {
		t.Errorf("wait-start does not state the bound: %q", start.Message)
	}

	got := findEntry(entries, "dependency_wait_timeout")
	if got == nil {
		t.Fatalf("expected a dependency_wait_timeout entry, got %+v", entries)
	}
	if findEntry(entries, "dependency_wait_cancelled") != nil {
		t.Error("an expired timeout must not be reported as a cancellation")
	}
	for _, want := range []string{`"api"`, "timeout=20ms", `"web"`, ".agnt.kdl"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("timeout message missing %q: %q", want, got.Message)
		}
	}
}

// TestWaitForSingleDependency_CancelMessageIsDistinct pins the other branch: a
// cancelled autostart is expected during shutdown and must not read as a
// misconfigured timeout.
func TestWaitForSingleDependency_CancelMessageIsDistinct(t *testing.T) {
	d := depWaitDaemon()
	projectPath := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d.waitForSingleDependency(ctx, "web", config.ScriptDependency{Name: "api"}, projectPath, 1, nil)

	entries := depWaitEntries(t, d, projectPath)

	start := findEntry(entries, "dependency_wait_start")
	if start == nil || !strings.Contains(start.Message, "unbounded") {
		t.Errorf("an undeclared timeout must be reported as unbounded, got %+v", start)
	}

	got := findEntry(entries, "dependency_wait_cancelled")
	if got == nil {
		t.Fatalf("expected a dependency_wait_cancelled entry, got %+v", entries)
	}
	if findEntry(entries, "dependency_wait_timeout") != nil {
		t.Error("a cancellation must not be reported as a timeout")
	}
	if !strings.Contains(got.Message, "cancelled") || !strings.Contains(got.Message, `"api"`) {
		t.Errorf("cancel message is not specific: %q", got.Message)
	}
}
