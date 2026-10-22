package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/pathutil"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tools"
)

// Inboxes are per session, so digests must be too: two agents in one checkout
// each subscribe with their own session code and must not receive the other's
// "you have incidents" ping.
func TestIncidentDigestSinkFilter_ExcludesOtherSessionsInSameProject(t *testing.T) {
	t.Parallel()
	d := NewForTest(t, DaemonConfig{})

	proj := pathutil.NormalizeProjectPath(t.TempDir())
	payload := incident.PingPayload{
		Type: "agnt.incident_ping", Version: 1,
		Summary: incident.PingStats{Error: 1, New: 1},
	}

	// Register two sessions in one project so the session scope resolves.
	for _, code := range []string{"agent-a", "agent-b"} {
		if err := d.sessionRegistry.Register(&Session{Code: code, ProjectPath: proj}); err != nil {
			t.Fatalf("register %s: %v", code, err)
		}
	}

	resolve := func(f protocol.StreamEventFilter) streamFilter {
		t.Helper()
		sf, err := d.resolveStreamFilter(f, "")
		if err != nil {
			t.Fatalf("resolveStreamFilter: %v", err)
		}
		return sf
	}
	sinkA := d.eventHub.AddStreamSink(resolve(tools.IncidentDigestFilter("agent-a", proj)))
	defer d.eventHub.RemoveStreamSink(sinkA)
	sinkB := d.eventHub.AddStreamSink(resolve(tools.IncidentDigestFilter("agent-b", proj)))
	defer d.eventHub.RemoveStreamSink(sinkB)
	// Directory-selected subscriber (no session): keeps project scope.
	sinkDir := d.eventHub.AddStreamSink(resolve(tools.IncidentDigestFilter("", proj)))
	defer d.eventHub.RemoveStreamSink(sinkDir)

	d.broadcastIncidentDigest("error", proj, "agent-a", "", payload)

	recv := func(s *StreamSink) (proxy.LogEntry, bool) {
		select {
		case e := <-s.Ch:
			return e, true
		default: // broadcast is synchronous, so absent here is final
			return proxy.LogEntry{}, false
		}
	}
	if _, ok := recv(sinkA); !ok {
		t.Fatal("agent-a never received its own digest")
	}
	if _, ok := recv(sinkB); ok {
		t.Fatal("agent-a's digest reached agent-b in the same project")
	}
	if _, ok := recv(sinkDir); !ok {
		t.Fatal("project-scoped subscriber lost the digest")
	}

	// An unstamped digest is not delivered to a session-selecting subscriber.
	d.broadcastIncidentDigest("error", proj, "", "", payload)
	if _, ok := recv(sinkA); ok {
		t.Fatal("digest with no session reached a session-selecting subscriber")
	}
}
