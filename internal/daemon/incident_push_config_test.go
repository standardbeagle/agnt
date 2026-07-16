package daemon

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
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

	d.ApplyAlertsConfig("/project", &config.AlertsConfig{Preset: "claude-code"})
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

	d.ApplyAlertsConfig("/project", &config.AlertsConfig{Preset: "universal"})
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

func TestIncidentPushPresetIsIsolatedAcrossConcurrentProjects(t *testing.T) {
	d := &Daemon{eventHub: NewEventHub(), sessionRegistry: NewSessionRegistry(0)}
	defer func() {
		if hold := d.holdBuffer.Swap(nil); hold != nil {
			hold.Stop()
		}
	}()
	d.sessionRegistry.Register(&Session{Code: "claude", ProjectPath: "/projects/claude", OverlayPath: "/claude.sock"})
	d.sessionRegistry.Register(&Session{Code: "universal", ProjectPath: "/projects/universal", OverlayPath: "/universal.sock"})
	claudeStream := d.eventHub.AddStreamSink(streamFilter{projectPath: "/projects/claude"})
	universalStream := d.eventHub.AddStreamSink(streamFilter{projectPath: "/projects/universal"})
	defer d.eventHub.RemoveStreamSink(claudeStream)
	defer d.eventHub.RemoveStreamSink(universalStream)

	var claudePTY, universalPTY atomic.Int64
	d.incidentPTYInject = func(path, _ string) error {
		switch path {
		case "/claude.sock":
			claudePTY.Add(1)
		case "/universal.sock":
			universalPTY.Add(1)
		default:
			t.Fatalf("unexpected PTY path %q", path)
		}
		return nil
	}

	var applyWG sync.WaitGroup
	for i := 0; i < 50; i++ {
		applyWG.Add(2)
		go func() {
			defer applyWG.Done()
			d.ApplyAlertsConfig("/projects/claude/", &config.AlertsConfig{Preset: "claude-code"})
		}()
		go func() {
			defer applyWG.Done()
			d.ApplyAlertsConfig("/projects/universal", &config.AlertsConfig{Preset: "universal"})
		}()
	}
	applyWG.Wait()

	claudeMCP, claudeChannel, claudeInject := d.incidentSinkCallbacks("claude")
	universalMCP, universalChannel, universalInject := d.incidentSinkCallbacks("universal")
	if claudeChannel != nil || universalChannel != nil {
		t.Fatal("incident channel callbacks must remain nil")
	}
	payload := incident.PingPayload{Summary: incident.PingStats{Error: 1}}
	if err := claudeMCP("error", payload); err != nil {
		t.Fatal(err)
	}
	if err := claudeInject("claude"); err != nil {
		t.Fatal(err)
	}
	if err := universalMCP("error", payload); err != nil {
		t.Fatal(err)
	}
	if err := universalInject("universal"); err != nil {
		t.Fatal(err)
	}

	assertExactlyOneStreamEntry := func(name string, ch <-chan proxy.LogEntry) {
		t.Helper()
		select {
		case <-ch:
		default:
			t.Fatalf("%s missing stream digest", name)
		}
		select {
		case <-ch:
			t.Fatalf("%s received duplicate stream digest", name)
		default:
		}
	}
	assertExactlyOneStreamEntry("claude", claudeStream.Ch)
	assertExactlyOneStreamEntry("universal", universalStream.Ch)
	if got := claudePTY.Load(); got != 0 {
		t.Fatalf("claude PTY=%d, want 0", got)
	}
	if got := universalPTY.Load(); got != 1 {
		t.Fatalf("universal PTY=%d, want 1", got)
	}
}
