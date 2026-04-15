package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServerSessionNotify_CustomMethod verifies that the forked SDK's
// ServerSession.Notify sends a custom notification method through an
// in-memory transport and the client receives the exact method + params.
func TestServerSessionNotify_CustomMethod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	// Server
	server := mcp.NewServer(&mcp.Implementation{Name: "testServer", Version: "1.0.0"}, nil)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	// Raw client connection (bypasses MCP client to read jsonrpc directly).
	cConn, err := ct.Connect(ctx)
	if err != nil {
		t.Fatalf("client transport connect: %v", err)
	}

	// Perform MCP initialize handshake so the server session is ready.
	initReq := buildInitializeRequest(t)
	if err := cConn.Write(ctx, initReq); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	// Read the initialize response.
	if _, err := cConn.Read(ctx); err != nil {
		t.Fatalf("read initialize response: %v", err)
	}
	// Send initialized notification to complete handshake.
	notif := &jsonrpc.Request{Method: "notifications/initialized"}
	if err := cConn.Write(ctx, notif); err != nil {
		t.Fatalf("write initialized notification: %v", err)
	}

	// Send custom notification from server.
	params := map[string]any{
		"content": "hello from channel",
		"meta":    map[string]string{"src": "test"},
	}
	if err := ss.Notify(ctx, "notifications/claude/channel", params); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	// Read the notification on the client side.
	msg, err := cConn.Read(ctx)
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}

	got, ok := msg.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("expected *jsonrpc.Request, got %T", msg)
	}

	if got.Method != "notifications/claude/channel" {
		t.Errorf("method = %q, want %q", got.Method, "notifications/claude/channel")
	}

	var result struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	}
	if err := json.Unmarshal(got.Params, &result); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if result.Content != "hello from channel" {
		t.Errorf("content = %q, want %q", result.Content, "hello from channel")
	}
	if result.Meta["src"] != "test" {
		t.Errorf("meta.src = %q, want %q", result.Meta["src"], "test")
	}
}

// TestServerSessionNotify_RejectsNonNotification verifies that Notify
// rejects method names that don't start with "notifications/".
func TestServerSessionNotify_RejectsNonNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{Name: "testServer", Version: "1.0.0"}, nil)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	cConn, err := ct.Connect(ctx)
	if err != nil {
		t.Fatalf("client transport connect: %v", err)
	}
	_ = cConn // not needed for this test

	err = ss.Notify(ctx, "tools/call", nil)
	if err == nil {
		t.Fatal("expected error for non-notifications/ method")
	}
	wantErr := `Notify: method must start with notifications/, got "tools/call"`
	if err.Error() != wantErr {
		t.Errorf("error = %q, want %q", err.Error(), wantErr)
	}
}

func buildInitializeRequest(t *testing.T) *jsonrpc.Request {
	t.Helper()
	id, err := jsonrpc.MakeID(float64(1))
	if err != nil {
		t.Fatalf("make ID: %v", err)
	}
	params := json.RawMessage(`{
		"protocolVersion": "2025-11-25",
		"capabilities": {},
		"clientInfo": {"name": "testClient", "version": "1.0.0"}
	}`)
	return &jsonrpc.Request{ID: id, Method: "initialize", Params: params}
}
