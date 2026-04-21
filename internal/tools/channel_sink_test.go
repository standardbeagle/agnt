package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/go-sdk/jsonrpc"
	"github.com/standardbeagle/go-sdk/mcp"
)

// capturedNotification records a single Notify call for test assertions.
type capturedNotification struct {
	method string
	params channelNotification
}

// fakeNotify returns a NotifyFunc that captures calls into the given slice.
// The slice is protected by the returned mutex.
func fakeNotify(calls *[]capturedNotification) (NotifyFunc, *sync.Mutex) {
	var mu sync.Mutex
	return func(ctx context.Context, method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		notif, ok := params.(channelNotification)
		if !ok {
			return nil
		}
		*calls = append(*calls, capturedNotification{method: method, params: notif})
		return nil
	}, &mu
}

func enabledChannelConfig() *config.ChannelConfig {
	enabled := true
	return &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "warning",
		DedupeWindow: 0, // Disable dedupe for basic tests.
	}
}

// TestChannelSink_EmitsNotification verifies that feeding a LogEntry into the sink
// produces the expected Notify call with correct method + content + meta.
func TestChannelSink_EmitsNotification(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	sink := NewChannelSink(enabledChannelConfig(), notify)

	entry := proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "Cannot read property 'map' of undefined",
			Source:  "src/components/List.tsx",
			LineNo:  42,
			ColNo:   15,
			URL:     "http://localhost:3000/dashboard",
		},
	}

	sink.HandleEntry(context.Background(), entry)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(calls))
	}

	got := calls[0]
	if got.method != ChannelNotificationMethod {
		t.Errorf("method = %q, want %q", got.method, ChannelNotificationMethod)
	}
	if got.params.Content != "Cannot read property 'map' of undefined" {
		t.Errorf("content = %q, want %q", got.params.Content, "Cannot read property 'map' of undefined")
	}
	if got.params.Meta["type"] != "error" {
		t.Errorf("meta[type] = %q, want %q", got.params.Meta["type"], "error")
	}
	if got.params.Meta["severity"] != "error" {
		t.Errorf("meta[severity] = %q, want %q", got.params.Meta["severity"], "error")
	}
	wantLoc := "src/components/List.tsx:42:15"
	if got.params.Meta["location"] != wantLoc {
		t.Errorf("meta[location] = %q, want %q", got.params.Meta["location"], wantLoc)
	}
}

// TestChannelSink_SanitizesMetaKeys verifies that meta keys are sanitized:
// hyphens become underscores, invalid characters are dropped, keys are lowercased.
func TestChannelSink_SanitizesMetaKeys(t *testing.T) {
	// Use a diagnostic entry which has a "category" field to test sanitization.
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	enabled := true
	cfg := &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "info", // low threshold to pass diagnostic info level
		DedupeWindow: 0,
	}
	sink := NewChannelSink(cfg, notify)

	entry := proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:    proxy.DiagnosticInfo,
			Message:  "connection established",
			Category: "proxy-health",
		},
	}

	sink.HandleEntry(context.Background(), entry)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(calls))
	}

	got := calls[0].params
	// "category" stays as "category" (all lowercase already)
	if _, ok := got.Meta["category"]; !ok {
		t.Error("expected 'category' key in meta")
	}
	// The value should preserve hyphens since sanitization is on keys, not values.
	if got.Meta["category"] != "proxy-health" {
		t.Errorf("meta[category] = %q, want %q", got.Meta["category"], "proxy-health")
	}
}

// TestSanitizeMetaKey verifies the sanitizer directly.
func TestSanitizeMetaKey(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"proxy-id", "proxy_id"},
		{"$weird.key!", "weirdkey"},
		{"already_clean", "already_clean"},
		{"ABC-def_GHI", "abc_def_ghi"},
		{"123", "123"},
		{"$@#!", ""},
		{"Content-Type", "content_type"},
	}
	for _, tt := range tests {
		got := sanitizeMetaKey(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeMetaKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestChannelSink_DedupeWindow verifies that two identical entries within
// the dedupe window produce only one notification, while two different
// entries produce two notifications.
func TestChannelSink_DedupeWindow(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	enabled := true
	cfg := &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "warning",
		DedupeWindow: 2000, // 2-second window.
	}
	sink := NewChannelSink(cfg, notify)

	now := time.Now()
	sink.SetNowFunc(func() time.Time { return now })

	entry := proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "TypeError: x is not a function",
			Source:  "app.js",
			LineNo:  10,
		},
	}

	// First entry: should emit.
	sink.HandleEntry(context.Background(), entry)

	// Second identical entry within window: should be deduped.
	sink.HandleEntry(context.Background(), entry)

	// Third entry with different content: should emit.
	entry2 := proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "TypeError: y is not a function",
			Source:  "app.js",
			LineNo:  20,
		},
	}
	sink.HandleEntry(context.Background(), entry2)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 notifications (deduped 1), got %d", len(calls))
	}
	if calls[0].params.Content != "TypeError: x is not a function" {
		t.Errorf("first content = %q, want %q", calls[0].params.Content, "TypeError: x is not a function")
	}
	if calls[1].params.Content != "TypeError: y is not a function" {
		t.Errorf("second content = %q, want %q", calls[1].params.Content, "TypeError: y is not a function")
	}
}

// TestChannelSink_DedupeExpired tests that entries outside the window are not deduped.
func TestChannelSink_DedupeExpired(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	enabled := true
	cfg := &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "warning",
		DedupeWindow: 100, // 100ms window.
	}
	sink := NewChannelSink(cfg, notify)

	now := time.Now()
	sink.SetNowFunc(func() time.Time { return now })

	entry := proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "TypeError: x is not a function",
		},
	}

	// First entry.
	sink.HandleEntry(context.Background(), entry)

	// Advance time past the window.
	sink.SetNowFunc(func() time.Time { return now.Add(200 * time.Millisecond) })

	// Same entry after window expired: should emit again.
	sink.HandleEntry(context.Background(), entry)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 notifications (window expired), got %d", len(calls))
	}
}

// TestChannelSink_SeverityFilter verifies that events below the configured
// severity threshold are dropped.
func TestChannelSink_SeverityFilter(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	enabled := true
	cfg := &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "error", // Only errors, drop warnings and below.
		DedupeWindow: 0,
	}
	sink := NewChannelSink(cfg, notify)

	// Info-level diagnostic: should be dropped.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:   proxy.DiagnosticInfo,
			Message: "server started",
		},
	})

	// Warning-level diagnostic: should be dropped.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:   proxy.DiagnosticWarning,
			Message: "slow response",
		},
	})

	// Error-level: should pass.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:   proxy.DiagnosticError,
			Message: "connection refused",
		},
	})

	// JS error: always severity "error", should pass.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "Uncaught TypeError",
		},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 notifications (error severity only), got %d", len(calls))
	}
	if calls[0].params.Content != "connection refused" {
		t.Errorf("first content = %q, want %q", calls[0].params.Content, "connection refused")
	}
	if calls[1].params.Content != "Uncaught TypeError" {
		t.Errorf("second content = %q, want %q", calls[1].params.Content, "Uncaught TypeError")
	}
}

// TestChannelSink_EventTypeFilter verifies that the events allowlist works.
func TestChannelSink_EventTypeFilter(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	enabled := true
	cfg := &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "info",
		DedupeWindow: 0,
		Events:       []string{"error", "diagnostic"},
	}
	sink := NewChannelSink(cfg, notify)

	// Error type: should pass.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "JS error"},
	})

	// Diagnostic type: should pass.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type:       proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{Level: proxy.DiagnosticInfo, Message: "diag"},
	})

	// Panel message type: should be filtered out.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type:         proxy.LogTypePanelMessage,
		PanelMessage: &proxy.PanelMessage{Message: "hello"},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 notifications (event type filter), got %d", len(calls))
	}
}

// TestChannelSink_HTTPError verifies HTTP entry extraction.
func TestChannelSink_HTTPError(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	sink := NewChannelSink(enabledChannelConfig(), notify)

	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:     "POST",
			URL:        "/api/users",
			StatusCode: 500,
		},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(calls))
	}
	got := calls[0].params
	if got.Meta["severity"] != "error" {
		t.Errorf("severity = %q, want %q", got.Meta["severity"], "error")
	}
	if got.Meta["type"] != "http" {
		t.Errorf("type = %q, want %q", got.Meta["type"], "http")
	}
	want := "POST /api/users → 500"
	if got.Content != want {
		t.Errorf("content = %q, want %q", got.Content, want)
	}
}

// TestChannelSink_SkipsNonErrors verifies that non-error HTTP entries are skipped.
func TestChannelSink_SkipsNonErrors(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	sink := NewChannelSink(enabledChannelConfig(), notify)

	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:     "GET",
			URL:        "/api/health",
			StatusCode: 200,
		},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("expected 0 notifications for non-error HTTP, got %d", len(calls))
	}
}

// TestChannelSink_EmptyMessageSkipped verifies entries with empty messages are skipped.
func TestChannelSink_EmptyMessageSkipped(t *testing.T) {
	var calls []capturedNotification
	notify, _ := fakeNotify(&calls)

	sink := NewChannelSink(enabledChannelConfig(), notify)

	// Error with empty message.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: ""},
	})

	// Nil error.
	sink.HandleEntry(context.Background(), proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: nil,
	})

	if len(calls) != 0 {
		t.Fatalf("expected 0 notifications for empty entries, got %d", len(calls))
	}
}

// TestChannelSink_DedupeDisabled verifies that dedupe=0 disables deduplication.
func TestChannelSink_DedupeDisabled(t *testing.T) {
	var calls []capturedNotification
	notify, mu := fakeNotify(&calls)

	enabled := true
	cfg := &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "warning",
		DedupeWindow: 0, // Disabled.
	}
	sink := NewChannelSink(cfg, notify)

	entry := proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "same error"},
	}

	sink.HandleEntry(context.Background(), entry)
	sink.HandleEntry(context.Background(), entry)
	sink.HandleEntry(context.Background(), entry)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("expected 3 notifications (dedupe disabled), got %d", len(calls))
	}
}

// TestSeverityRank tests the severity ranking helper.
func TestSeverityRank(t *testing.T) {
	tests := []struct {
		level string
		rank  int
	}{
		{"trace", 0},
		{"debug", 1},
		{"info", 2},
		{"warning", 3},
		{"error", 4},
		{"unknown", 0},
	}
	for _, tt := range tests {
		got := severityRank(tt.level)
		if got != tt.rank {
			t.Errorf("severityRank(%q) = %d, want %d", tt.level, got, tt.rank)
		}
	}
}

// TestChannelSink_Integration_InMemory verifies the end-to-end path:
// feed a LogEntry into the ChannelSink and assert that a correctly
// shaped notifications/claude/channel notification arrives on an
// in-memory MCP client connection.
func TestChannelSink_Integration_InMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ct, st := mcp.NewInMemoryTransports()

	// Server with channel capability enabled.
	enabled := true
	agntCfg := &config.ChannelConfig{
		Enabled:      &enabled,
		Severity:     "warning",
		DedupeWindow: 0,
	}

	opts := ChannelServerOptions(&mcp.ServerOptions{HasTools: true}, agntCfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, opts)
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	// NotifyFunc that sends to all MCP sessions (same pattern as production wiring).
	notify := func(ctx context.Context, method string, params any) error {
		for session := range server.Sessions() {
			_ = session.Notify(ctx, method, params)
		}
		return nil
	}

	sink := NewChannelSink(agntCfg, notify)

	// Connect raw client to read notifications.
	cConn, err := ct.Connect(ctx)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	// Perform initialize handshake so session is ready.
	id, _ := jsonrpc.MakeID(float64(1))
	initReq := &jsonrpc.Request{
		ID:     id,
		Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1.0.0"}}`),
	}
	if err := cConn.Write(ctx, initReq); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if _, err := cConn.Read(ctx); err != nil {
		t.Fatalf("read init response: %v", err)
	}
	if err := cConn.Write(ctx, &jsonrpc.Request{Method: "notifications/initialized"}); err != nil {
		t.Fatalf("write initialized: %v", err)
	}

	// Feed a LogEntry through the channel sink.
	sink.HandleEntry(ctx, proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "integration test error",
			Source:  "test.js",
			LineNo:  1,
			ColNo:   1,
		},
	})

	// Read the notification from the client.
	msg, err := cConn.Read(ctx)
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}

	got, ok := msg.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("expected *jsonrpc.Request, got %T", msg)
	}

	if got.Method != ChannelNotificationMethod {
		t.Errorf("method = %q, want %q", got.Method, ChannelNotificationMethod)
	}

	var result struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	}
	if err := json.Unmarshal(got.Params, &result); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if result.Content != "integration test error" {
		t.Errorf("content = %q, want %q", result.Content, "integration test error")
	}
	if result.Meta["type"] != "error" {
		t.Errorf("meta[type] = %q, want %q", result.Meta["type"], "error")
	}
	if result.Meta["severity"] != "error" {
		t.Errorf("meta[severity] = %q, want %q", result.Meta["severity"], "error")
	}
}
