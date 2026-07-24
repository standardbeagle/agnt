package tools

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
)

func TestBuildWatchCommand_AllTarget(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "all"}
	cmd, desc, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(cmd, "monitor") {
		t.Errorf("command should contain 'monitor', got: %s", cmd)
	}
	if !strings.Contains(cmd, "--socket /run/user/1000/agnt.sock") {
		t.Errorf("command should contain socket path, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--format compact") {
		t.Errorf("command should contain --format compact, got: %s", cmd)
	}
	if strings.Contains(cmd, "--types") {
		t.Errorf("'all' target should not have --types filter, got: %s", cmd)
	}
	if desc == "" {
		t.Error("description should not be empty")
	}
}

func TestBuildWatchCommand_ErrorsTarget(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "errors", ProxyID: "dev"}
	cmd, desc, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(cmd, "--types error,diagnostic") {
		t.Errorf("errors target should have error,diagnostic types, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--proxy dev") {
		t.Errorf("command should contain --proxy dev, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--socket /run/user/1000/agnt.sock") {
		t.Errorf("command should contain socket path, got: %s", cmd)
	}
	if !strings.Contains(strings.ToLower(desc), "error") {
		t.Errorf("description should mention errors, got: %s", desc)
	}
}

func TestBuildWatchCommand_InteractionsTarget(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "interactions", ProxyID: "dev"}
	cmd, desc, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(cmd, "--types panel_message,interaction,sketch") {
		t.Errorf("interactions target should have panel_message,interaction,sketch types, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--proxy dev") {
		t.Errorf("command should contain --proxy dev, got: %s", cmd)
	}
	if !strings.Contains(desc, "interaction") {
		t.Errorf("description should mention interactions, got: %s", desc)
	}
}

func TestBuildWatchCommand_ProcessTarget(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "process", ProcessID: "app"}
	cmd, desc, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(cmd, "--process app") {
		t.Errorf("command should contain --process app, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--types process") {
		t.Errorf("process target should have process type, got: %s", cmd)
	}
	if !strings.Contains(strings.ToLower(desc), "process") {
		t.Errorf("description should mention process, got: %s", desc)
	}
}

func TestBuildWatchCommand_InvalidTarget(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "invalid"}
	_, _, err := buildWatchCommand(dt, input)
	if err == nil {
		t.Fatal("expected error for invalid target")
	}
}

func TestBuildWatchCommand_ErrorsWithoutProxyID(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "errors"}
	cmd, _, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(cmd, "--proxy") {
		t.Errorf("command without proxy_id should not have --proxy, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--types error,diagnostic") {
		t.Errorf("command should still have types, got: %s", cmd)
	}
}

func TestBuildWatchCommand_InteractionsWithoutProxyID(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "interactions"}
	cmd, _, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(cmd, "--proxy") {
		t.Errorf("command without proxy_id should not have --proxy, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--types panel_message,interaction,sketch") {
		t.Errorf("command should still have types, got: %s", cmd)
	}
}

func TestBuildWatchCommand_ProcessWithoutProcessID(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{Target: "process"}
	_, _, err := buildWatchCommand(dt, input)
	if err == nil {
		t.Fatal("expected error for process target without process_id")
	}
}

func TestBuildWatchCommand_DefaultTarget(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.12.35")

	input := WatchInput{}
	cmd, _, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(cmd, "--format compact") {
		t.Errorf("default target (empty) should behave like 'all', got: %s", cmd)
	}
}

func TestBuildWatchCommand_BinaryPathResolution(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/tmp/test.sock",
	}, "0.12.35")

	input := WatchInput{Target: "all"}
	cmd, _, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The command should start with the resolved binary path
	// We just verify it contains "monitor" after the binary
	parts := strings.SplitN(cmd, " ", 2)
	if len(parts) < 2 {
		t.Fatalf("command should have at least binary and subcommand: %s", cmd)
	}
	if !strings.Contains(parts[1], "monitor") {
		t.Errorf("second part should contain 'monitor', got: %s", parts[1])
	}
}
