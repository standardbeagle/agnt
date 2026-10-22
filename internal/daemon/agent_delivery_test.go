package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/pathutil"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tools"
	"github.com/stretchr/testify/require"
)

func TestConnectorFor(t *testing.T) {
	for _, tc := range []struct {
		overlay string
		want    agentConnector
	}{
		{"/tmp/run.sock", connectorStdin}, // agnt run owns the PTY
		{"", connectorMCP},                // MCP-only agent
		{"-", connectorMCP},               // channel session / mod: no overlay socket
	} {
		if got := connectorFor(&Session{OverlayPath: tc.overlay}); got != tc.want {
			t.Errorf("overlay %q: connector %q, want %q", tc.overlay, got, tc.want)
		}
	}
	if connectorFor(nil) != connectorMCP {
		t.Error("nil session must fall back to mcp")
	}
}

// One ping is one delivery, and the sender never picks it: the same sink
// callbacks serve both sessions, and what differs is how each agent was started.
// The stdin session is typed to and its digest is stamped so the MCP process
// stays quiet; the MCP-only session is not typed to and its digest is the
// notification.
func TestAgentMessage_RoutedByHowTheSessionStarted(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	project := pathutil.NormalizeProjectPath(t.TempDir())

	var typed []string
	d.incidentPTYInject = func(socket, line string) error {
		typed = append(typed, socket+"|"+line)
		return nil
	}
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code: "run", ProjectPath: project, OverlayPath: "/tmp/run.sock",
		StartedAt: time.Now(), Status: SessionStatusActive, LastSeen: time.Now(),
	}))
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code: "mcponly", ProjectPath: project, OverlayPath: "-",
		StartedAt: time.Now(), Status: SessionStatusActive, LastSeen: time.Now(),
	}))
	sink := d.eventHub.AddStreamSink(streamFilter{projectPath: project})
	defer d.eventHub.RemoveStreamSink(sink)

	next := func() proxy.LogEntry {
		t.Helper()
		select {
		case e := <-sink.Ch:
			return e
		case <-time.After(2 * time.Second):
			t.Fatal("no digest")
			return proxy.LogEntry{}
		}
	}
	ping := incident.PingPayload{Type: "agnt.incident_ping", Version: 1, Summary: incident.PingStats{Error: 1, New: 1}}
	notified := func(e proxy.LogEntry) bool {
		logged := false
		tools.ForwardIncidentDigest(context.Background(), e, false,
			func(context.Context, string, string) error { logged = true; return nil }, nil)
		return logged
	}

	for _, tc := range []struct {
		session    string
		wantTyped  int
		wantVia    string
		wantNotify bool
	}{
		{"run", 1, "stdin", false},
		{"mcponly", 0, "mcp", true},
	} {
		typed = nil
		mcpNotify, _, ptyInject := d.incidentSinkCallbacks(tc.session)
		require.NoError(t, mcpNotify("error", ping))
		require.NoError(t, ptyInject("agnt: 1 error"))

		e := next()
		require.Equal(t, tc.wantVia, e.Custom.Data["via"], tc.session)
		require.Equal(t, "agent_message", e.Custom.Data["kind"], tc.session)
		require.Equal(t, tc.session, e.Custom.Data["session_code"])
		require.Len(t, typed, tc.wantTyped, "%s: typed lines", tc.session)
		require.Equal(t, tc.wantNotify, notified(e), "%s: MCP notification", tc.session)
	}
}

// Reading the history never depends on the connector. A stdin session's agent
// got a one-line hint, so the pull surface must still return the incident behind
// it, and must return it again after it was read. Pause gates the push only.
func TestIncidentHistory_PullableWhateverTheConnector(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	project := pathutil.NormalizeProjectPath(t.TempDir())
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code: "run", ProjectPath: project, OverlayPath: "/tmp/run.sock",
		StartedAt: time.Now(), Status: SessionStatusActive, LastSeen: time.Now(),
	}))
	d.addIncidentSession("run")
	d.registerIncidentProxyOwner("px", "run")
	d.eventHub.RegisterProxyPath("px", project)
	d.SetForwardingPaused("run", true)

	d.fireToIncidentBus(channelTestError(), "px")

	require.Eventually(t, func() bool {
		entries, stats := d.incidentBus.QuerySession("run", incident.QueryFilter{})
		_ = stats
		return len(entries) == 1
	}, 10*time.Second, 20*time.Millisecond, "incident never reached the inbox")

	for pass := 0; pass < 2; pass++ { // second pass: already read
		entries, _ := d.incidentBus.QueryAndMarkSession("run", incident.QueryFilter{},
			func(snap []incident.InboxEntry) []string {
				var fps []string
				for _, e := range snap {
					fps = append(fps, e.Fingerprint)
				}
				return fps
			}, true)
		require.Len(t, entries, 1, "pass %d: history must survive being read", pass)
	}
}
