package daemon

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A share the publish store quarantines is not a load failure, so nothing in
// the store-load path reported it: the share silently stopped being served.
// Each one must produce a loud, project-scoped startup-log entry that says
// which share, why, and which file to remove.
func TestLogQuarantinedShares_OneErrorEntryPerShareInItsProject(t *testing.T) {
	t.Parallel()
	d := &Daemon{startupErrorStore: NewStartupLogStore(100)}
	projectA := normalizePath(t.TempDir())
	projectB := normalizePath(t.TempDir())
	const reason = "addScript is not permitted on a published walkthrough (INV-14)"

	shares := []publish.QuarantinedShare{
		{ID: "legacy1", Title: "Checkout tour", ProjectPath: projectA, Reason: reason, RecordPath: "/state/publish/legacy1.json", SourceFile: "tours/checkout.json"},
		{ID: "legacy2", Title: "", ProjectPath: projectB, Reason: reason, RecordPath: "/state/publish/legacy2.json"},
		{ID: "orphan", Title: "No project", Reason: reason, RecordPath: "/state/publish/orphan.json"},
	}
	d.logQuarantinedShares(shares)

	quarantineEntries := func(filter StartupLogFilter) []*StartupLogEntry {
		var out []*StartupLogEntry
		for _, e := range d.startupErrorStore.Query(filter) {
			if e.EventType == publishShareQuarantinedEvent {
				out = append(out, e)
			}
		}
		return out
	}

	// Project A's scoped log — what `daemon startup_log` shows its agent.
	a := quarantineEntries(StartupLogFilter{ProjectPath: projectA})
	require.Len(t, a, 1, "project A sees exactly its own quarantined share")
	assert.Equal(t, "error", a[0].Level)
	for _, want := range []string{"legacy1", "Checkout tour", "INV-14", "addScript", "/state/publish/legacy1.json", "tours/checkout.json", "NOT being served"} {
		assert.Contains(t, a[0].Message, want)
	}
	assert.NotContains(t, a[0].Message, "legacy2", "another project's share must not leak into this one's entry")

	b := quarantineEntries(StartupLogFilter{ProjectPath: projectB})
	require.Len(t, b, 1)
	assert.Contains(t, b[0].Message, "legacy2")
	assert.Contains(t, b[0].Message, "(untitled)", "an untitled share still gets a readable entry")

	// A share with no project is daemon-wide: visible to a global query only.
	all := quarantineEntries(StartupLogFilter{})
	require.Len(t, all, 3)
	var orphan *StartupLogEntry
	for _, e := range all {
		if strings.Contains(e.Message, "orphan") {
			orphan = e
		}
	}
	require.NotNil(t, orphan)
	assert.Equal(t, "error", orphan.Level)

	// Reported again (a session registering): no duplicates while the entry
	// is still in the log.
	d.logQuarantinedShares(shares)
	assert.Len(t, quarantineEntries(StartupLogFilter{}), 3)

	// The project's log is cleared when its last session leaves; the next
	// report must put the entry back.
	d.startupErrorStore.ClearByPrefix(makeProcessID(projectA, ""))
	require.Empty(t, quarantineEntries(StartupLogFilter{ProjectPath: projectA}))
	d.logQuarantinedShares(shares)
	assert.Len(t, quarantineEntries(StartupLogFilter{ProjectPath: projectA}), 1)
	assert.Len(t, quarantineEntries(StartupLogFilter{}), 3)
}

// A store with nothing quarantined writes nothing, and a daemon whose store
// failed to load does not panic when a session registers.
func TestLogQuarantinedShares_NothingToReport(t *testing.T) {
	t.Parallel()
	d := &Daemon{startupErrorStore: NewStartupLogStore(10)}
	d.logQuarantinedShares(nil)
	d.logQuarantinedSharesForProject(t.TempDir()) // publishStore is nil
	assert.Empty(t, d.startupErrorStore.Query(StartupLogFilter{}))
}
