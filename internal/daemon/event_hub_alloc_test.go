package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// With no STREAM-EVENTS subscriber, fanning out an entry must not allocate:
// it runs once per process output line and per traffic entry.
func TestEventHub_BroadcastWithoutSinksDoesNotAllocate(t *testing.T) {
	h := NewEventHub()
	entry := proxy.LogEntry{}
	allocs := testing.AllocsPerRun(100, func() {
		h.BroadcastProcessOutput(entry, "/p")
		h.BroadcastLogEntryForProject(entry, "/p")
	})
	if allocs != 0 {
		t.Fatalf("broadcast with no sinks allocated %v times per run, want 0", allocs)
	}
}
