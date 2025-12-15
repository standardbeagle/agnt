package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestChaosEngine_BasicOperations(t *testing.T) {
	engine := NewChaosEngine(nil)

	// Initially disabled
	if engine.IsEnabled() {
		t.Error("expected chaos to be initially disabled")
	}

	// Enable
	engine.Enable()
	if !engine.IsEnabled() {
		t.Error("expected chaos to be enabled")
	}

	// Disable
	engine.Disable()
	if engine.IsEnabled() {
		t.Error("expected chaos to be disabled")
	}
}

func TestChaosEngine_SetConfig(t *testing.T) {
	engine := NewChaosEngine(nil)

	config := &ChaosConfig{
		Enabled:    true,
		GlobalOdds: 0.5,
		Rules: []*ChaosRule{
			{
				ID:           "test-latency",
				Name:         "Test Latency",
				Type:         ChaosLatency,
				Enabled:      true,
				MinLatencyMs: 100,
				MaxLatencyMs: 200,
			},
		},
	}

	err := engine.SetConfig(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !engine.IsEnabled() {
		t.Error("expected chaos to be enabled from config")
	}

	retrievedConfig := engine.GetConfig()
	if retrievedConfig == nil {
		t.Fatal("expected config to be set")
	}

	if len(retrievedConfig.Rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(retrievedConfig.Rules))
	}
}

func TestChaosEngine_SetConfigWithInvalidRegex(t *testing.T) {
	engine := NewChaosEngine(nil)

	config := &ChaosConfig{
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:         "test-invalid",
				Type:       ChaosLatency,
				Enabled:    true,
				URLPattern: "[invalid(regex",
			},
		},
	}

	err := engine.SetConfig(config)
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestChaosEngine_AddRemoveRule(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	rule := &ChaosRule{
		ID:          "test-rule",
		Name:        "Test Rule",
		Type:        ChaosHTTPError,
		Enabled:     true,
		ErrorCodes:  []int{500},
		Probability: 1.0,
	}

	err := engine.AddRule(rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check rule exists
	config := engine.GetConfig()
	if config != nil && len(config.Rules) != 0 {
		t.Error("config should be nil for individually added rules")
	}

	// Remove the rule
	removed := engine.RemoveRule("test-rule")
	if !removed {
		t.Error("expected rule to be removed")
	}

	// Try to remove again
	removed = engine.RemoveRule("test-rule")
	if removed {
		t.Error("expected rule to not be found")
	}
}

func TestChaosEngine_MatchingRules(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	// Add rules
	engine.AddRule(&ChaosRule{
		ID:           "get-only",
		Type:         ChaosLatency,
		Enabled:      true,
		Methods:      []string{"GET"},
		MinLatencyMs: 100,
		MaxLatencyMs: 200,
	})

	engine.AddRule(&ChaosRule{
		ID:         "api-pattern",
		Type:       ChaosHTTPError,
		Enabled:    true,
		URLPattern: "/api/.*",
		ErrorCodes: []int{500},
	})

	// Test GET request
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	rules := engine.MatchingRules(req)
	if len(rules) != 1 {
		t.Errorf("expected 1 matching rule for GET, got %d", len(rules))
	}

	// Test POST to /api/
	req = httptest.NewRequest("POST", "http://example.com/api/users", nil)
	rules = engine.MatchingRules(req)
	if len(rules) != 1 {
		t.Errorf("expected 1 matching rule for POST /api, got %d", len(rules))
	}

	// Test GET to /api/ (should match both)
	req = httptest.NewRequest("GET", "http://example.com/api/users", nil)
	rules = engine.MatchingRules(req)
	if len(rules) != 2 {
		t.Errorf("expected 2 matching rules for GET /api, got %d", len(rules))
	}
}

func TestChaosEngine_DisabledReturnsNoRules(t *testing.T) {
	engine := NewChaosEngine(nil)

	// Add a rule but don't enable chaos
	engine.AddRule(&ChaosRule{
		ID:      "test",
		Type:    ChaosLatency,
		Enabled: true,
	})

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	rules := engine.MatchingRules(req)

	if len(rules) != 0 {
		t.Errorf("expected no matching rules when chaos is disabled, got %d", len(rules))
	}
}

func TestChaosEngine_GetLatencyDelay(t *testing.T) {
	engine := NewChaosEngine(nil)

	rules := []*ChaosRule{
		{
			Type:         ChaosLatency,
			MinLatencyMs: 100,
			MaxLatencyMs: 200,
		},
	}

	delay := engine.GetLatencyDelay(rules)
	if delay < 100*time.Millisecond || delay > 200*time.Millisecond {
		t.Errorf("expected delay between 100-200ms, got %v", delay)
	}

	// Test with jitter
	rules[0].JitterMs = 50
	delay = engine.GetLatencyDelay(rules)
	if delay < 50*time.Millisecond || delay > 250*time.Millisecond {
		t.Errorf("expected delay between 50-250ms with jitter, got %v", delay)
	}
}

func TestChaosEngine_GetHTTPError(t *testing.T) {
	engine := NewChaosEngine(nil)

	rules := []*ChaosRule{
		{
			Type:         ChaosHTTPError,
			ErrorCodes:   []int{500, 502, 503},
			ErrorMessage: "Server Error",
		},
	}

	code, msg := engine.GetHTTPError(rules)
	if code < 500 || code > 503 {
		t.Errorf("expected error code 500-503, got %d", code)
	}
	if msg != "Server Error" {
		t.Errorf("expected 'Server Error', got %s", msg)
	}

	// Test with no HTTP error rules
	rules = []*ChaosRule{
		{Type: ChaosLatency},
	}
	code, _ = engine.GetHTTPError(rules)
	if code != 0 {
		t.Errorf("expected code 0 for non-error rules, got %d", code)
	}
}

func TestChaosEngine_GetDropConfig(t *testing.T) {
	engine := NewChaosEngine(nil)

	// Test with DropAfterPercent
	rules := []*ChaosRule{
		{
			Type:             ChaosDisconnect,
			DropAfterPercent: 0.3,
		},
	}

	percent, bytes := engine.GetDropConfig(rules)
	if percent != 0.3 {
		t.Errorf("expected percent 0.3, got %f", percent)
	}
	if bytes != 0 {
		t.Errorf("expected bytes 0, got %d", bytes)
	}

	// Test with DropAfterBytes
	rules = []*ChaosRule{
		{
			Type:           ChaosDisconnect,
			DropAfterBytes: 1024,
		},
	}

	percent, bytes = engine.GetDropConfig(rules)
	if percent != 0 {
		t.Errorf("expected percent 0, got %f", percent)
	}
	if bytes != 1024 {
		t.Errorf("expected bytes 1024, got %d", bytes)
	}
}

func TestChaosEngine_Stats(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	engine.AddRule(&ChaosRule{
		ID:      "test",
		Type:    ChaosLatency,
		Enabled: true,
	})

	// Make some requests
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "http://example.com/test", nil)
		engine.MatchingRules(req)
	}

	stats := engine.GetStats()
	if stats.TotalRequests != 10 {
		t.Errorf("expected 10 total requests, got %d", stats.TotalRequests)
	}
	if stats.AffectedCount != 10 {
		t.Errorf("expected 10 affected, got %d", stats.AffectedCount)
	}

	// Reset stats
	engine.ResetStats()
	stats = engine.GetStats()
	if stats.TotalRequests != 0 {
		t.Errorf("expected 0 requests after reset, got %d", stats.TotalRequests)
	}
}

func TestChaosEngine_Clear(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	engine.SetConfig(&ChaosConfig{
		Enabled: true,
		Rules: []*ChaosRule{
			{ID: "test", Type: ChaosLatency, Enabled: true},
		},
	})

	engine.Clear()

	if engine.IsEnabled() {
		t.Error("expected chaos to be disabled after clear")
	}

	config := engine.GetConfig()
	if config != nil {
		t.Error("expected config to be nil after clear")
	}
}

func TestChaosEngine_GlobalOdds(t *testing.T) {
	engine := NewChaosEngine(nil)

	// Set config with global odds of 0 (never apply)
	engine.SetConfig(&ChaosConfig{
		Enabled:    true,
		GlobalOdds: 0.0001, // Very low odds
		Rules: []*ChaosRule{
			{ID: "test", Type: ChaosLatency, Enabled: true, Probability: 1.0},
		},
	})

	// With very low global odds, most requests should not match
	matches := 0
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest("GET", "http://example.com/test", nil)
		rules := engine.MatchingRules(req)
		if len(rules) > 0 {
			matches++
		}
	}

	// With 0.01% odds, we expect very few matches out of 100
	if matches > 10 {
		t.Errorf("expected very few matches with low global odds, got %d", matches)
	}
}

func TestChaosPresets(t *testing.T) {
	presets := ListPresets()
	if len(presets) == 0 {
		t.Error("expected at least one preset")
	}

	// Test that all listed presets exist
	for _, name := range presets {
		preset := GetPreset(name)
		if preset == nil {
			t.Errorf("preset %q not found", name)
		}
	}

	// Test that unknown preset returns nil
	if GetPreset("unknown-preset-xyz") != nil {
		t.Error("expected nil for unknown preset")
	}

	// Test mobile-3g preset
	mobile3g := GetPreset("mobile-3g")
	if mobile3g == nil {
		t.Fatal("mobile-3g preset not found")
	}
	if !mobile3g.Enabled {
		t.Error("mobile-3g should be enabled by default")
	}
	if len(mobile3g.Rules) == 0 {
		t.Error("mobile-3g should have rules")
	}

	// Test that modifying returned preset doesn't affect original
	mobile3g.Enabled = false
	mobile3g2 := GetPreset("mobile-3g")
	if !mobile3g2.Enabled {
		t.Error("preset copy should be independent")
	}
}

// TestSlowDripWriter tests the slow drip response writer
func TestSlowDripWriter(t *testing.T) {
	// Create a mock response writer
	rr := httptest.NewRecorder()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create writer with fast settings for testing (bytesPerMs, chunkSize, ctx)
	sdw := NewSlowDripWriter(rr, 10000, 10, ctx) // 10KB/ms (fast for test)

	data := []byte("Hello, World!")
	n, err := sdw.Write(data)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}
	if rr.Body.String() != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got '%s'", rr.Body.String())
	}
}

func TestSlowDripWriter_ContextCancellation(t *testing.T) {
	rr := httptest.NewRecorder()

	ctx, cancel := context.WithCancel(context.Background())

	// Create writer with slow settings (bytesPerMs, chunkSize, ctx)
	sdw := NewSlowDripWriter(rr, 1, 1, ctx) // Very slow: 1 byte/ms

	// Cancel context immediately
	cancel()

	data := make([]byte, 100)
	_, err := sdw.Write(data)

	if err == nil {
		t.Error("expected context cancelled error")
	}
}

// TestConnectionDropWriter tests the connection drop response writer
func TestConnectionDropWriter_WriteHeader(t *testing.T) {
	rr := httptest.NewRecorder()

	cdw := NewConnectionDropWriter(rr, 0.5, 0, 100)

	cdw.WriteHeader(200)

	if rr.Code != 200 {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestConnectionDropWriter_Header(t *testing.T) {
	rr := httptest.NewRecorder()

	cdw := NewConnectionDropWriter(rr, 0.5, 0, 100)

	cdw.Header().Set("X-Test", "value")

	if rr.Header().Get("X-Test") != "value" {
		t.Error("expected header to be set")
	}
}

// TestTruncationWriter tests the truncation response writer
func TestTruncationWriter(t *testing.T) {
	rr := httptest.NewRecorder()

	// Truncate at 50% with expected size of 10 bytes
	tw := NewTruncationWriter(rr, 0.5, 10)

	data := []byte("0123456789") // 10 bytes
	n, err := tw.Write(data)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Should write only 5 bytes (50% of 10)
	if n != 5 {
		t.Errorf("expected to write 5 bytes, wrote %d", n)
	}

	body := rr.Body.String()
	if body != "01234" {
		t.Errorf("expected '01234', got '%s'", body)
	}

	// Additional writes should be silently discarded (returns len(p) but doesn't actually write)
	n, err = tw.Write([]byte("more data"))
	if n != 9 || err != nil { // Returns len("more data") = 9, but data is discarded
		t.Errorf("expected n=9 (silently discarded), got n=%d, err=%v", n, err)
	}

	// Body should still be only the original 5 bytes
	if rr.Body.String() != "01234" {
		t.Errorf("expected body to still be '01234', got '%s'", rr.Body.String())
	}
}

func TestTruncationWriter_TotalBytes(t *testing.T) {
	rr := httptest.NewRecorder()

	// Truncate at 80% with expected size of 100 bytes
	tw := NewTruncationWriter(rr, 0.8, 100)

	// Write 50 bytes
	data := make([]byte, 50)
	for i := range data {
		data[i] = byte('A' + i%26)
	}
	n, err := tw.Write(data)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != 50 {
		t.Errorf("expected 50, got %d", n)
	}

	// Write another 50 bytes - should truncate at 80 total
	n, err = tw.Write(data)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != 30 { // Only 30 more bytes to reach 80
		t.Errorf("expected 30, got %d", n)
	}

	// Verify total written
	if rr.Body.Len() != 80 {
		t.Errorf("expected 80 total bytes, got %d", rr.Body.Len())
	}
}

// TestReorderQueue tests the request reordering queue
func TestReorderQueue_BasicReorder(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()
	engine.AddRule(&ChaosRule{
		ID:                 "reorder",
		Type:               ChaosOutOfOrder,
		Enabled:            true,
		ReorderMinRequests: 2,
		ReorderMaxWaitMs:   500,
	})

	// The ReorderQueue is created automatically by the engine
	// Just verify the queue exists and can be used through the engine
	queue := NewReorderQueue(engine)
	if queue == nil {
		t.Fatal("expected reorder queue to be created")
	}

	// Stop the queue
	queue.Stop()
}

// TestChaosTransport tests the chaos transport wrapper
func TestChaosTransport_NoRulesMatch(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	engine := NewChaosEngine(nil)
	// Chaos disabled, so no rules should match

	transport := NewChaosTransport(http.DefaultTransport, engine)

	req := httptest.NewRequest("GET", server.URL, nil)
	req = req.WithContext(context.Background())

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "OK" {
		t.Errorf("expected 'OK', got '%s'", string(body))
	}
}

func TestChaosTransport_WithLatency(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	engine := NewChaosEngine(nil)
	engine.Enable()
	engine.AddRule(&ChaosRule{
		ID:           "test-latency",
		Type:         ChaosLatency,
		Enabled:      true,
		MinLatencyMs: 50,
		MaxLatencyMs: 100,
	})

	transport := NewChaosTransport(http.DefaultTransport, engine)

	req, _ := http.NewRequest("GET", server.URL, nil)
	req = req.WithContext(context.Background())

	start := time.Now()
	resp, err := transport.RoundTrip(req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Should have added at least 50ms latency
	if elapsed < 50*time.Millisecond {
		t.Errorf("expected at least 50ms latency, got %v", elapsed)
	}
}

// TestErrorInjectionWriter tests the error injection writer
func TestErrorInjectionWriter(t *testing.T) {
	rr := httptest.NewRecorder()

	eiw := NewErrorInjectionWriter(rr, 500, "Internal Server Error")

	eiw.WriteHeader(200) // This should be ignored
	eiw.Write([]byte("Should not appear"))

	if rr.Code != 500 {
		t.Errorf("expected status 500, got %d", rr.Code)
	}

	body := rr.Body.String()
	if body != "Internal Server Error" {
		t.Errorf("expected 'Internal Server Error', got '%s'", body)
	}
}

// Benchmark tests
func BenchmarkChaosEngine_MatchingRules(b *testing.B) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	// Add several rules
	for i := 0; i < 10; i++ {
		engine.AddRule(&ChaosRule{
			ID:         string(rune('a' + i)),
			Type:       ChaosLatency,
			Enabled:    true,
			URLPattern: "/api/.*",
		})
	}

	req := httptest.NewRequest("GET", "http://example.com/api/users", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.MatchingRules(req)
	}
}

func BenchmarkChaosEngine_GetStats(b *testing.B) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	for i := 0; i < 10; i++ {
		engine.AddRule(&ChaosRule{
			ID:      string(rune('a' + i)),
			Type:    ChaosLatency,
			Enabled: true,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.GetStats()
	}
}

// Integration test for the full chaos pipeline
func TestChaosIntegration_HTTPErrorInjection(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()
	engine.AddRule(&ChaosRule{
		ID:           "http-error",
		Type:         ChaosHTTPError,
		Enabled:      true,
		Probability:  1.0,
		ErrorCodes:   []int{503},
		ErrorMessage: `{"error": "service unavailable"}`,
	})

	req := httptest.NewRequest("GET", "http://example.com/api/test", nil)
	rules := engine.MatchingRules(req)

	code, msg := engine.GetHTTPError(rules)

	if code != 503 {
		t.Errorf("expected 503, got %d", code)
	}
	if msg != `{"error": "service unavailable"}` {
		t.Errorf("unexpected message: %s", msg)
	}
}

func TestChaosIntegration_SlowDripWithTruncation(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	engine.AddRule(&ChaosRule{
		ID:         "slow-drip",
		Type:       ChaosSlowDrip,
		Enabled:    true,
		BytesPerMs: 100,
		ChunkSize:  10,
	})

	engine.AddRule(&ChaosRule{
		ID:              "truncate",
		Type:            ChaosTruncate,
		Enabled:         true,
		TruncatePercent: 0.5,
	})

	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	rules := engine.MatchingRules(req)

	bytesPerMs, chunkSize := engine.GetSlowDripConfig(rules)
	if bytesPerMs != 100 || chunkSize != 10 {
		t.Errorf("unexpected slow drip config: %d, %d", bytesPerMs, chunkSize)
	}

	truncatePercent := engine.GetTruncateConfig(rules)
	if truncatePercent != 0.5 {
		t.Errorf("unexpected truncate percent: %f", truncatePercent)
	}
}

// Test concurrent access to the chaos engine
func TestChaosEngine_ConcurrentAccess(t *testing.T) {
	engine := NewChaosEngine(nil)
	engine.Enable()

	engine.AddRule(&ChaosRule{
		ID:      "test",
		Type:    ChaosLatency,
		Enabled: true,
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "http://example.com/test", nil)
			engine.MatchingRules(req)
			engine.GetStats()
		}()
	}

	wg.Wait()

	stats := engine.GetStats()
	if stats.TotalRequests != 100 {
		t.Errorf("expected 100 requests, got %d", stats.TotalRequests)
	}
}

// Test TruncationWriter with buffer
func TestTruncationWriter_WithBuffer(t *testing.T) {
	var buf bytes.Buffer

	// Truncate at 50% with expected size of 16 bytes
	tw := NewTruncationWriter(&testResponseWriter{buf: &buf}, 0.5, 16)

	data := []byte("0123456789ABCDEF") // 16 bytes
	n, err := tw.Write(data)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != 8 { // 50% of 16
		t.Errorf("expected 8, got %d", n)
	}
}

// testResponseWriter is a simple test implementation
type testResponseWriter struct {
	buf     *bytes.Buffer
	headers http.Header
}

func (w *testResponseWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}

func (w *testResponseWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}

func (w *testResponseWriter) WriteHeader(code int) {}
