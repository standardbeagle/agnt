package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/pathutil"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tools"
)

// The MCP digest sink's own subscription filter, resolved through the real
// STREAM-EVENTS scope chokepoint, must exclude another project's digest. The
// sink used to subscribe with Global:true, which resolves to an unscoped filter
// and delivered every project's "you have incidents" ping to every agent.
func TestIncidentDigestSinkFilter_ExcludesOtherProjects(t *testing.T) {
	t.Parallel()
	d := NewForTest(t, DaemonConfig{})

	projA := pathutil.NormalizeProjectPath(t.TempDir())
	projB := pathutil.NormalizeProjectPath(t.TempDir())

	resolve := func(f protocol.StreamEventFilter) streamFilter {
		t.Helper()
		sf, err := d.resolveStreamFilter(f, "")
		if err != nil {
			t.Fatalf("resolveStreamFilter: %v", err)
		}
		return sf
	}

	sinkA := d.eventHub.AddStreamSink(resolve(tools.IncidentDigestFilter("", projA)))
	defer d.eventHub.RemoveStreamSink(sinkA)
	sinkB := d.eventHub.AddStreamSink(resolve(tools.IncidentDigestFilter("", projB)))
	defer d.eventHub.RemoveStreamSink(sinkB)
	// Mutation guard: the pre-fix subscription shape really does receive it,
	// so the exclusion below is the filter's doing, not a dead broadcast.
	legacy := d.eventHub.AddStreamSink(resolve(protocol.StreamEventFilter{
		Types:  []string{string(proxy.LogTypeIncidentDigest)},
		Global: true,
	}))
	defer d.eventHub.RemoveStreamSink(legacy)

	d.broadcastIncidentDigest("error", projA, "", "", incident.PingPayload{
		Type: "agnt.incident_ping", Version: 1,
		Summary: incident.PingStats{Error: 1, New: 1},
	})

	for name, sink := range map[string]*StreamSink{"owning project": sinkA, "legacy global": legacy} {
		select {
		case <-sink.Ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s sink never received the digest", name)
		}
	}
	select {
	case <-sinkB.Ch:
		t.Fatal("project A's digest reached project B's digest sink")
	default:
		// Broadcast is synchronous, so an absent entry here is final.
	}
}
