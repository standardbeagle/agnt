package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/pathutil"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tools"
)

// mcpProcess stands in for one `agnt mcp` process: it holds the two
// STREAM-EVENTS subscriptions that process opens (digest sink + channel sink,
// both resolved through the real scope chokepoint) and counts what it would
// send to the agent on each transport.
type mcpProcess struct {
	t         *testing.T
	channelOn bool
	digest    *StreamSink
	events    *StreamSink
	sink      *tools.ChannelSink

	channel int
	logs    int
}

func newMCPProcess(t *testing.T, d *Daemon, project string, channelOn bool) *mcpProcess {
	t.Helper()
	m := &mcpProcess{t: t, channelOn: channelOn}

	df, err := d.resolveStreamFilter(tools.IncidentDigestFilter("", project), "")
	if err != nil {
		t.Fatalf("resolve digest filter: %v", err)
	}
	m.digest = d.eventHub.AddStreamSink(df)
	t.Cleanup(func() { d.eventHub.RemoveStreamSink(m.digest) })

	if channelOn {
		ef, err := d.resolveStreamFilter(protocol.StreamEventFilter{Directory: project, GlobalOverride: protocol.Bool(false)}, "")
		if err != nil {
			t.Fatalf("resolve channel filter: %v", err)
		}
		m.events = d.eventHub.AddStreamSink(ef)
		t.Cleanup(func() { d.eventHub.RemoveStreamSink(m.events) })

		enabled := true
		m.sink = tools.NewChannelSink(&config.ChannelConfig{Enabled: &enabled, Severity: "info", DedupeWindow: 2000}, m.notify)
	}
	return m
}

func (m *mcpProcess) notify(context.Context, string, any) error { m.channel++; return nil }
func (m *mcpProcess) log(context.Context, string, string) error { m.logs++; return nil }

// drain delivers everything already queued on both subscriptions. Broadcasts
// are synchronous, so whatever a completed daemon call produced is queued.
func (m *mcpProcess) drain() {
	for {
		select {
		case e := <-m.digest.Ch:
			tools.ForwardIncidentDigest(context.Background(), e, m.channelOn, m.log, m.notify)
			continue
		default:
		}
		if m.events != nil {
			select {
			case e := <-m.events.Ch:
				// The channel sink subscribes unfiltered by type and sees the
				// digest entry too; it has no extractor for it and drops it.
				m.sink.HandleEntry(context.Background(), e)
				continue
			default:
			}
		}
		return
	}
}

func (m *mcpProcess) total() int { return m.channel + m.logs }

func channelTestError() proxy.LogEntry {
	return proxy.LogEntry{Type: proxy.LogTypeError, Error: &proxy.FrontendError{
		Message: "TypeError: Cannot read property 'map' of undefined",
		Stack:   "    at ProductList (src/components/List.tsx:42:15)",
		URL:     "http://localhost:3000/list",
	}}
}

var channelTestPing = incident.PingPayload{
	Type: "agnt.incident_ping", Version: 1, Session: "chan",
	Summary: incident.PingStats{Error: 1, New: 1},
}

// With channel mode on, one browser error produces exactly one agent-facing
// notification across both transports — through the real pipeline (bus, inbox,
// pinger, digest broadcast) and the real raw fan-out. It used to be two: the
// raw event from the channel sink plus the digest.
func TestChannelMode_OneBrowserErrorOneNotification_EndToEnd(t *testing.T) {
	for _, channelOn := range []bool{true, false} {
		name := "channel off"
		if channelOn {
			name = "channel on"
		}
		t.Run(name, func(t *testing.T) {
			d := NewForTest(t, DaemonConfig{})
			project := pathutil.NormalizeProjectPath(t.TempDir())
			registerSession(t, d, "chan", project)
			d.addIncidentSession("chan")
			d.registerIncidentProxyOwner("px", "chan")
			d.eventHub.RegisterProxyPath("px", project)
			m := newMCPProcess(t, d, project, channelOn)

			// What the proxy logger does for one browser error: raw fan-out to
			// stream subscribers, and ingest into the incident pipeline.
			entry := channelTestError()
			d.eventHub.BroadcastLogEntry(entry, "px")
			d.fireToIncidentBus(entry, "px")

			// The raw entry is queued already. It must produce nothing.
			m.drain()
			if m.total() != 0 {
				t.Fatalf("raw error produced %d notification(s) before any digest (channel=%d log=%d)", m.total(), m.channel, m.logs)
			}

			// Liveness: the pinger emits the digest on its own (coalesced)
			// schedule. A generous ceiling, not a latency assertion.
			select {
			case e := <-m.digest.Ch:
				tools.ForwardIncidentDigest(context.Background(), e, channelOn, m.log, m.notify)
			case <-time.After(30 * time.Second):
				t.Fatal("no incident digest reached the MCP process")
			}
			m.drain()

			if m.total() != 1 {
				t.Fatalf("one error must produce exactly one notification, got channel=%d log=%d", m.channel, m.logs)
			}
			if channelOn && m.channel != 1 {
				t.Errorf("channel mode on: the notification must be on the channel (channel=%d log=%d)", m.channel, m.logs)
			}
			if !channelOn && m.logs != 1 {
				t.Errorf("channel mode off: the notification must be the MCP log, unchanged (channel=%d log=%d)", m.channel, m.logs)
			}
		})
	}
}

// The channel delivery sits behind the same gates as the log delivery: a paused
// session and an alerts.push that deselects MCP notifications both yield zero
// notifications on either transport, and lifting the gate restores exactly one.
// Driven through the session's real sink callback, which is synchronous — no
// timing involved.
func TestChannelMode_DigestHonoursPauseAndAlertsPush(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	project := pathutil.NormalizeProjectPath(t.TempDir())
	registerSession(t, d, "chan", project)
	m := newMCPProcess(t, d, project, true)
	push, _, _ := d.incidentSinkCallbacks("chan")

	ping := func() int {
		t.Helper()
		before := m.total()
		if err := push("error", channelTestPing); err != nil {
			t.Fatal(err)
		}
		m.drain()
		return m.total() - before
	}

	if n := ping(); n != 1 {
		t.Fatalf("baseline: want 1 notification, got %d", n)
	}

	d.SetForwardingPaused("chan", true)
	if n := ping(); n != 0 {
		t.Fatalf("forwarding paused: want 0 notifications on any transport, got %d", n)
	}
	d.SetForwardingPaused("chan", false)
	if n := ping(); n != 1 {
		t.Fatalf("forwarding resumed: want 1 notification, got %d", n)
	}

	off := false
	d.ApplyAlertsConfig(project, &config.AlertsConfig{Push: &config.PushConfig{MCPNotifications: &off}})
	if n := ping(); n != 0 {
		t.Fatalf("alerts.push deselects mcp-notifications: want 0 notifications on any transport, got %d", n)
	}
	on := true
	d.ApplyAlertsConfig(project, &config.AlertsConfig{Push: &config.PushConfig{MCPNotifications: &on}})
	if n := ping(); n != 1 {
		t.Fatalf("alerts.push reselects mcp-notifications: want 1 notification, got %d", n)
	}
	if m.logs != 0 {
		t.Errorf("channel mode on must never also send the MCP log (logs=%d)", m.logs)
	}
}

// A panel message is not an incident: it reaches a channel-mode agent raw, as
// soon as it is broadcast, and produces no digest.
func TestChannelMode_PanelMessageStillArrivesRaw(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	project := pathutil.NormalizeProjectPath(t.TempDir())
	registerSession(t, d, "chan", project)
	d.eventHub.RegisterProxyPath("px", project)
	m := newMCPProcess(t, d, project, true)

	d.eventHub.BroadcastLogEntry(proxy.LogEntry{Type: proxy.LogTypePanelMessage,
		PanelMessage: &proxy.PanelMessage{Message: "make this button blue"}}, "px")
	m.drain()
	if m.channel != 1 || m.logs != 0 {
		t.Fatalf("panel message: want 1 raw channel event, got channel=%d log=%d", m.channel, m.logs)
	}
}
