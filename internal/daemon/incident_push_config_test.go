package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/incident"
)

func TestIncidentPushPresetSelectsRuntimeSinks(t *testing.T) {
	d := &Daemon{
		eventHub:        NewEventHub(),
		sessionRegistry: NewSessionRegistry(0),
	}
	defer func() {
		if hold := d.holdBuffer.Swap(nil); hold != nil {
			hold.Stop()
		}
	}()
	d.sessionRegistry.Register(&Session{Code: "session", ProjectPath: "/project", OverlayPath: "/overlay.sock"})
	stream := d.eventHub.AddStreamSink(streamFilter{projectPath: "/project"})
	defer d.eventHub.RemoveStreamSink(stream)
	ptyCalls := 0
	d.incidentPTYInject = func(path, line string) error {
		ptyCalls++
		if path != "/overlay.sock" || line == "" {
			t.Fatalf("PTY delivery path=%q line=%q", path, line)
		}
		return nil
	}
	mcp, channel, pty := d.incidentSinkCallbacks("session")
	if channel != nil {
		t.Fatal("incident pinger channel callback must stay nil until a production channel transport exists")
	}
	payload := incident.PingPayload{Summary: incident.PingStats{Error: 1}}

	d.ApplyAlertsConfig(&config.AlertsConfig{Preset: "claude-code"})
	if err := mcp("error", payload); err != nil {
		t.Fatal(err)
	}
	if err := pty("claude digest"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.Ch:
	default:
		t.Fatal("claude-code preset must enable MCP/stream digest")
	}
	if ptyCalls != 0 {
		t.Fatalf("claude-code PTY calls=%d, want 0", ptyCalls)
	}

	d.ApplyAlertsConfig(&config.AlertsConfig{Preset: "universal"})
	if err := mcp("error", payload); err != nil {
		t.Fatal(err)
	}
	if err := pty("universal digest"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.Ch:
	default:
		t.Fatal("universal preset must enable MCP/stream digest")
	}
	if ptyCalls != 1 {
		t.Fatalf("universal PTY calls=%d, want exactly 1", ptyCalls)
	}
}
