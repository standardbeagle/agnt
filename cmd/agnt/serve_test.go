package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/tools"
)

// TestChannelDisabled_DefaultCapabilities verifies that when channel is
// disabled (the default), the initialize response has no experimental
// capabilities -- byte-for-byte identical to pre-change behavior.
func TestChannelDisabled_DefaultCapabilities(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	opts := defaultDaemonServerOptions()
	opts = tools.ChannelServerOptions(opts, nil) // nil config = disabled

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.0"},
		opts,
	)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	initResult := performInitHandshake(t, ctx, ct)

	if initResult.Capabilities == nil {
		t.Fatal("capabilities should not be nil")
	}
	if len(initResult.Capabilities.Experimental) != 0 {
		t.Errorf("expected no experimental capabilities, got %v", initResult.Capabilities.Experimental)
	}
}

// TestChannelDisabled_ExplicitDisabled verifies that an explicitly-disabled
// channel config (enabled = false) also produces no experimental capabilities.
func TestChannelDisabled_ExplicitDisabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	disabled := false
	cfg := &config.ChannelConfig{Enabled: &disabled}

	opts := defaultDaemonServerOptions()
	opts = tools.ChannelServerOptions(opts, cfg)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.0"},
		opts,
	)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	initResult := performInitHandshake(t, ctx, ct)

	if initResult.Capabilities == nil {
		t.Fatal("capabilities should not be nil")
	}
	if len(initResult.Capabilities.Experimental) != 0 {
		t.Errorf("expected no experimental capabilities, got %v", initResult.Capabilities.Experimental)
	}
}

// TestChannelEnabled_CapabilityPresent verifies that when channel is enabled,
// the initialize response includes capabilities.experimental["claude/channel"].
func TestChannelEnabled_CapabilityPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	enabled := true
	cfg := &config.ChannelConfig{Enabled: &enabled}

	opts := defaultDaemonServerOptions()
	opts = tools.ChannelServerOptions(opts, cfg)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.0"},
		opts,
	)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	initResult := performInitHandshake(t, ctx, ct)

	if initResult.Capabilities == nil {
		t.Fatal("capabilities should not be nil")
	}
	if _, ok := initResult.Capabilities.Experimental["claude/channel"]; !ok {
		t.Fatal(`expected experimental["claude/channel"] to be present`)
	}
}

// TestChannelEnabled_InstructionsContainChannelTag verifies that when channel
// is enabled, the instructions string contains the channel tag shape.
func TestChannelEnabled_InstructionsContainChannelTag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	enabled := true
	cfg := &config.ChannelConfig{Enabled: &enabled}

	opts := defaultDaemonServerOptions()
	opts = tools.ChannelServerOptions(opts, cfg)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.0"},
		opts,
	)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	initResult := performInitHandshake(t, ctx, ct)

	if initResult.Instructions == "" {
		t.Fatal("instructions should not be empty")
	}
	if !strings.Contains(initResult.Instructions, `<channel source="agnt"`) {
		t.Errorf("instructions should contain channel tag, got:\n%s", initResult.Instructions)
	}
}

// TestChannelDisabled_InstructionsUnchanged verifies that the instructions
// string is unchanged when channel is disabled.
func TestChannelDisabled_InstructionsUnchanged(t *testing.T) {
	opts := defaultDaemonServerOptions()
	baseInstructions := opts.Instructions

	result := tools.ChannelServerOptions(opts, nil)
	if result.Instructions != baseInstructions {
		t.Errorf("instructions changed when channel disabled:\ngot:  %q\nwant: %q", result.Instructions, baseInstructions)
	}

	disabled := false
	cfg := &config.ChannelConfig{Enabled: &disabled}
	result = tools.ChannelServerOptions(opts, cfg)
	if result.Instructions != baseInstructions {
		t.Errorf("instructions changed when channel explicitly disabled:\ngot:  %q\nwant: %q", result.Instructions, baseInstructions)
	}
}

// TestChannelServerOptions_DoNotMutate verifies that ChannelServerOptions
// does not mutate the original ServerOptions.
func TestChannelServerOptions_DoNotMutate(t *testing.T) {
	opts := defaultDaemonServerOptions()
	originalInstructions := opts.Instructions

	enabled := true
	cfg := &config.ChannelConfig{Enabled: &enabled}
	result := tools.ChannelServerOptions(opts, cfg)

	// Original should be unchanged.
	if opts.Instructions != originalInstructions {
		t.Errorf("ChannelServerOptions mutated the original Instructions")
	}
	// Result should differ.
	if result.Instructions == originalInstructions {
		t.Errorf("result Instructions should differ from original when channel enabled")
	}
}

// defaultDaemonServerOptions returns the same ServerOptions that runDaemonClient
// uses, extracted into a helper so tests can construct matching state.
func defaultDaemonServerOptions() *mcp.ServerOptions {
	return &mcp.ServerOptions{
		HasTools: true,
		Instructions: `Development tool server for project detection, process management, and reverse proxy with traffic logging.

Uses a background daemon for persistent state across connections:
- Processes and proxies survive client disconnections
- State is shared across multiple MCP clients
- Auto-starts daemon if not running

Available tools:
- detect: Detect project type and available scripts
- run: Run scripts or raw commands (background/foreground modes)
- proc: Manage processes (status, output, stop, list, cleanup_port)
- proxy: Reverse proxy with traffic logging and JS instrumentation
- proxylog: Query proxy traffic logs
- currentpage: View active page sessions
- get_errors: Unified error view across processes and proxies
- responsive_audit: Responsive design audits across multiple viewport sizes
- snapshot: Visual regression testing (baseline/compare screenshots)
- daemon: Manage the background daemon service`,
	}
}

// performInitHandshake performs the MCP initialize handshake using a raw
// jsonrpc connection on the client transport. Returns the InitializeResult.
func performInitHandshake(t *testing.T, ctx context.Context, ct *mcp.InMemoryTransport) *mcp.InitializeResult {
	t.Helper()

	cConn, err := ct.Connect(ctx)
	if err != nil {
		t.Fatalf("client transport connect: %v", err)
	}

	id, err := jsonrpc.MakeID(float64(1))
	if err != nil {
		t.Fatalf("make ID: %v", err)
	}
	params := json.RawMessage(`{
		"protocolVersion": "2025-11-25",
		"capabilities": {},
		"clientInfo": {"name": "testClient", "version": "1.0.0"}
	}`)
	initReq := &jsonrpc.Request{ID: id, Method: "initialize", Params: params}
	if err := cConn.Write(ctx, initReq); err != nil {
		t.Fatalf("write initialize: %v", err)
	}

	resp, err := cConn.Read(ctx)
	if err != nil {
		t.Fatalf("read initialize response: %v", err)
	}

	jsonResp, ok := resp.(*jsonrpc.Response)
	if !ok {
		t.Fatalf("expected *jsonrpc.Response, got %T", resp)
	}

	var initResult mcp.InitializeResult
	if err := json.Unmarshal(jsonResp.Result, &initResult); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}

	// Send initialized notification to complete handshake.
	notif := &jsonrpc.Request{Method: "notifications/initialized"}
	if err := cConn.Write(ctx, notif); err != nil {
		t.Fatalf("write initialized notification: %v", err)
	}

	return &initResult
}
