package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/tools"
	"github.com/standardbeagle/go-sdk/jsonrpc"
	"github.com/standardbeagle/go-sdk/mcp"
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

// TestChannelAutostartHandler_DisabledReturnsNil verifies that
// channelAutostartHandler returns nil when channel is disabled.
func TestChannelAutostartHandler_DisabledReturnsNil(t *testing.T) {
	var called bool
	noop := func(dir string) (map[string]interface{}, error) {
		called = true
		return nil, nil
	}

	// nil config
	handler := channelAutostartHandler(nil, noop)
	if handler != nil {
		t.Error("expected nil handler for nil config")
	}

	// explicitly disabled
	disabled := false
	cfg := &config.ChannelConfig{Enabled: &disabled}
	handler = channelAutostartHandler(cfg, noop)
	if handler != nil {
		t.Error("expected nil handler for disabled config")
	}

	if called {
		t.Error("runAutostart should not have been called")
	}
}

// TestChannelAutostartHandler_EnabledInvokesRunAutostart verifies that when
// channel mode is enabled, the returned handler calls runAutostart with the
// working directory. This test uses an InMemoryTransport to perform a real MCP
// initialize/initialized handshake so the SDK fires the InitializedHandler.
func TestChannelAutostartHandler_EnabledInvokesRunAutostart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	var mu sync.Mutex
	var capturedDir string
	autostartCalled := make(chan struct{}, 1)

	mockRunAutostart := func(dir string) (map[string]interface{}, error) {
		mu.Lock()
		capturedDir = dir
		mu.Unlock()
		select {
		case autostartCalled <- struct{}{}:
		default:
		}
		return map[string]interface{}{
			"scripts": []string{"dev"},
		}, nil
	}

	enabled := true
	cfg := &config.ChannelConfig{Enabled: &enabled}

	opts := defaultDaemonServerOptions()
	opts = tools.ChannelServerOptions(opts, cfg)
	opts.InitializedHandler = channelAutostartHandler(cfg, mockRunAutostart)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.0"},
		opts,
	)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	// Perform the initialize + initialized handshake to trigger the handler.
	performInitHandshake(t, ctx, ct)

	// Wait for the autostart to be called (handler runs in a goroutine).
	select {
	case <-autostartCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("runAutostart was not called within timeout")
	}

	mu.Lock()
	dir := capturedDir
	mu.Unlock()

	if dir == "" {
		t.Error("expected runAutostart to be called with a non-empty directory")
	}
}

// TestChannelAutostartHandler_HandlerIsNilOnDisabled verifies that
// channelAutostartHandler returns nil when channel mode is disabled.
// Note: serve.go now uses mcpAutostartHandler (always non-nil); this helper
// is kept for channel-specific callers that want nil-on-disabled semantics.
func TestChannelAutostartHandler_HandlerIsNilOnDisabled(t *testing.T) {
	var called bool
	noop := func(dir string) (map[string]interface{}, error) {
		called = true
		return nil, nil
	}

	// With disabled config, channelAutostartHandler returns nil.
	disabled := false
	cfg := &config.ChannelConfig{Enabled: &disabled}
	handler := channelAutostartHandler(cfg, noop)

	if handler != nil {
		t.Error("expected nil handler for disabled config")
	}
	if called {
		t.Error("runAutostart should not have been called")
	}
}

// TestMCPAutostartHandler_AlwaysReturnsHandler verifies that mcpAutostartHandler
// always returns a non-nil handler regardless of channel config.
func TestMCPAutostartHandler_AlwaysReturnsHandler(t *testing.T) {
	noop := func(dir string) (map[string]interface{}, error) { return nil, nil }
	handler := mcpAutostartHandler(noop)
	if handler == nil {
		t.Error("expected non-nil handler from mcpAutostartHandler")
	}
}

// TestMCPAutostartHandler_InvokesRunAutostart verifies that the handler returned
// by mcpAutostartHandler calls runAutostart with the working directory when the
// MCP initialize/initialized handshake completes.
func TestMCPAutostartHandler_InvokesRunAutostart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	var mu sync.Mutex
	var capturedDir string
	autostartCalled := make(chan struct{}, 1)

	mockRunAutostart := func(dir string) (map[string]interface{}, error) {
		mu.Lock()
		capturedDir = dir
		mu.Unlock()
		select {
		case autostartCalled <- struct{}{}:
		default:
		}
		return map[string]interface{}{"scripts": []string{"dev"}}, nil
	}

	opts := defaultDaemonServerOptions()
	opts.InitializedHandler = mcpAutostartHandler(mockRunAutostart)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-server", Version: "0.0.0"},
		opts,
	)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	performInitHandshake(t, ctx, ct)

	select {
	case <-autostartCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("runAutostart was not called within timeout")
	}

	mu.Lock()
	dir := capturedDir
	mu.Unlock()

	if dir == "" {
		t.Error("expected runAutostart to be called with a non-empty directory")
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

// TestRegisterChannelSession_DisabledReturnsNil verifies that
// RegisterChannelSession returns nil when channel is disabled.
func TestRegisterChannelSession_DisabledReturnsNil(t *testing.T) {
	dt := tools.NewDaemonTools(daemon.AutoStartConfig{}, "test")
	ctx := context.Background()

	// nil config
	h := dt.RegisterChannelSession(ctx, nil, ".")
	if h != nil {
		t.Error("expected nil handle for nil config")
	}

	// explicitly disabled
	disabled := false
	cfg := &config.ChannelConfig{Enabled: &disabled}
	h = dt.RegisterChannelSession(ctx, cfg, ".")
	if h != nil {
		t.Error("expected nil handle for disabled config")
	}
}

// TestRegisterChannelSession_SetsSessionCode verifies that after a
// successful registration the session code is stored, and after Close it
// is cleared. This test starts a real daemon so the resilient client can
// connect.
func TestRegisterChannelSession_SetsSessionCode(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := daemon.New(daemon.DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
		// OrphanScanEnabled left zero (false) — test-safe default per iter 15.
	})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	dt := tools.NewDaemonTools(daemon.AutoStartConfig{
		SocketPath:    sockPath,
		StartTimeout:  2 * time.Second,
		RetryInterval: 50 * time.Millisecond,
		MaxRetries:    20,
	}, "test")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	enabled := true
	cfg := &config.ChannelConfig{Enabled: &enabled}

	h := dt.RegisterChannelSession(ctx, cfg, ".")
	if h == nil {
		t.Fatal("expected non-nil handle for enabled config")
	}

	code := dt.ChannelSessionCode()
	if code == "" {
		t.Error("expected session code to be set after registration")
	}
	if !strings.HasPrefix(code, "mcp-") {
		t.Errorf("expected session code to have mcp- prefix, got %q", code)
	}

	// Close should clear the session code.
	h.Close()
	if dt.ChannelSessionCode() != "" {
		t.Error("expected session code to be cleared after Close")
	}
}

// TestRegisterChannelSession_CloseIdempotent verifies that calling Close
// multiple times on the handle is safe.
func TestRegisterChannelSession_CloseIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := daemon.New(daemon.DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
		// OrphanScanEnabled left zero (false) — test-safe default per iter 15.
	})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	dt := tools.NewDaemonTools(daemon.AutoStartConfig{
		SocketPath:    sockPath,
		StartTimeout:  2 * time.Second,
		RetryInterval: 50 * time.Millisecond,
		MaxRetries:    20,
	}, "test")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	enabled := true
	cfg := &config.ChannelConfig{Enabled: &enabled}

	h := dt.RegisterChannelSession(ctx, cfg, ".")
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	// Close multiple times should not panic.
	h.Close()
	h.Close()
	h.Close()
}

// TestChannelSessionHandle_CloseNilIsSafe verifies that Close on a nil
// handle is a safe no-op.
func TestChannelSessionHandle_CloseNilIsSafe(t *testing.T) {
	var h *tools.ChannelSessionHandle
	h.Close() // should not panic
}

// TestChannelSession_ConcurrentCloseAndHeartbeat stresses the Close-vs-Init
// race fixed in iter 18 (Option B: single-owner ctx cancel in the handle).
// Spawns many Register/Close cycles with concurrent idempotent Close calls
// on each handle. Under -race this previously tripped the data race between
// closeChannelSession writing dt.channelHeartbeat and the heartbeat
// goroutine reading it in its select. With the fix, the heartbeat
// goroutine only reads its own hbCtx.Done(), so no shared-field access.
//
// This is the G1/G8-style stress harness for the [E] fix: tight-loop +
// concurrent Close + goroutine-leak check.
func TestChannelSession_ConcurrentCloseAndHeartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress in short mode")
	}

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := daemon.New(daemon.DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   64,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	dt := tools.NewDaemonTools(daemon.AutoStartConfig{
		SocketPath:    sockPath,
		StartTimeout:  2 * time.Second,
		RetryInterval: 50 * time.Millisecond,
		MaxRetries:    20,
	}, "test")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	enabled := true
	cfg := &config.ChannelConfig{Enabled: &enabled}

	const cycles = 40
	const closersPerHandle = 4

	for i := 0; i < cycles; i++ {
		h := dt.RegisterChannelSession(ctx, cfg, ".")
		if h == nil {
			t.Fatalf("cycle %d: expected non-nil handle", i)
		}
		if code := dt.ChannelSessionCode(); code == "" {
			t.Fatalf("cycle %d: expected session code after register", i)
		}

		// Fan out multiple concurrent Close calls; stopOnce must guarantee
		// the ctx cancel + daemon unregister pair fires exactly once.
		var wg sync.WaitGroup
		wg.Add(closersPerHandle)
		for j := 0; j < closersPerHandle; j++ {
			go func() {
				defer wg.Done()
				h.Close()
			}()
		}
		wg.Wait()

		if code := dt.ChannelSessionCode(); code != "" {
			t.Fatalf("cycle %d: expected session code cleared, got %q", i, code)
		}
	}

	// Let any stale heartbeat goroutines exit before test teardown so the
	// race detector has a quiescent point to inspect.
	time.Sleep(50 * time.Millisecond)
}
