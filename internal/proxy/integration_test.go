package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxy_PageTracking_Integration(t *testing.T) {
	// Create a backend server that serves HTML
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html><head></head><body>Hello World</body></html>"))
		case "/script.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("console.log('hello');"))
		case "/api/data":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status": "ok"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()

	// Create proxy pointing to backend
	config := ProxyConfig{
		ID:         "test-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0, // Auto-assign port
		MaxLogSize: 100,
	}

	ps, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer ps.Stop(ctx)

	// Wait for proxy to be ready
	select {
	case <-ps.Ready():
		// Server is ready
	case <-ctx.Done():
		t.Fatal("Context cancelled while waiting for proxy to be ready")
	}

	// Use ListenAddr directly since it now includes the bind address
	proxyURL := fmt.Sprintf("http://%s", ps.ListenAddr)

	// Make a request for the HTML page
	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatalf("Failed to request HTML page: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	t.Logf("HTML response status: %d", resp.StatusCode)
	t.Logf("HTML response Content-Type: %s", resp.Header.Get("Content-Type"))
	t.Logf("HTML response body length: %d", len(body))

	// Check that page session was created (synchronous - happens in request handler)
	sessions := ps.PageTracker().GetActiveSessions()
	t.Logf("Page sessions after HTML request: %d", len(sessions))

	if len(sessions) != 1 {
		// Debug: check what logs we have
		stats := ps.Logger().Stats()
		t.Logf("Logger stats: total=%d, available=%d", stats.TotalEntries, stats.AvailableEntries)

		entries := ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}, Limit: 10})
		t.Logf("HTTP log entries: %d", len(entries))
		for i, entry := range entries {
			if entry.HTTP != nil {
				t.Logf("  Entry %d: %s %s -> %d, Content-Type: %s",
					i, entry.HTTP.Method, entry.HTTP.URL, entry.HTTP.StatusCode,
					entry.HTTP.ResponseHeaders["Content-Type"])
			}
		}

		t.Errorf("Expected 1 page session after HTML request, got %d", len(sessions))
	}

	// Make a request for a JavaScript file with Referer
	req, _ := http.NewRequest("GET", proxyURL+"/script.js", nil)
	req.Header.Set("Referer", proxyURL+"/")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to request JS file: %v", err)
	}
	resp.Body.Close()

	t.Logf("JS response status: %d", resp.StatusCode)
	t.Logf("JS response Content-Type: %s", resp.Header.Get("Content-Type"))

	// Check that the JS request was added to the session
	sessions = ps.PageTracker().GetActiveSessions()
	if len(sessions) > 0 {
		t.Logf("Page session resources: %d", len(sessions[0].Resources))
		if len(sessions[0].Resources) != 1 {
			t.Errorf("Expected 1 resource in session, got %d", len(sessions[0].Resources))
		}
	}

	// Make a request for an API endpoint (should NOT create a new session)
	resp, err = http.Get(proxyURL + "/api/data")
	if err != nil {
		t.Fatalf("Failed to request API: %v", err)
	}
	resp.Body.Close()

	sessions = ps.PageTracker().GetActiveSessions()
	t.Logf("Page sessions after API request: %d", len(sessions))
}

func TestProxy_PageTracking_URLFormat(t *testing.T) {
	// This test verifies what URL format is stored in page sessions
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Backend received request: URL=%s, Path=%s, Host=%s", r.URL.String(), r.URL.Path, r.Host)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html></html>"))
	}))
	defer backend.Close()

	config := ProxyConfig{
		ID:         "test-url",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	}

	ps, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer ps.Stop(ctx)

	// Wait for proxy to be ready
	select {
	case <-ps.Ready():
		// Server is ready
	case <-ctx.Done():
		t.Fatal("Context cancelled while waiting for proxy to be ready")
	}

	// Use ListenAddr directly since it now includes the bind address
	proxyURL := fmt.Sprintf("http://%s", ps.ListenAddr)

	// Make the test request
	resp, err := http.Get(proxyURL + "/some/path?query=1")
	if err != nil {
		t.Fatalf("Failed to request: %v", err)
	}
	// Read full body to ensure complete response
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	// Check what URL was stored in the session (synchronous - happens in request handler)
	sessions := ps.PageTracker().GetActiveSessions()
	if len(sessions) == 0 {
		entries := ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}, Limit: 10})
		t.Fatalf("No sessions created (found %d HTTP log entries)", len(entries))
	}

	// Find the session for our test URL
	var testSession *PageSession
	for _, s := range sessions {
		t.Logf("Session found: %q", s.URL)
		if strings.Contains(s.URL, "some/path") {
			testSession = s
		}
	}

	if testSession == nil {
		t.Fatal("Session for /some/path?query=1 not found")
	}
	t.Logf("Test session URL: %q", testSession.URL)

	// Check that the URL contains the expected path and query
	if !strings.Contains(testSession.URL, "/some/path") {
		t.Errorf("Expected URL to contain '/some/path', got %q", testSession.URL)
	}
	if !strings.Contains(testSession.URL, "query=1") {
		t.Errorf("Expected URL to contain 'query=1', got %q", testSession.URL)
	}
}

func TestProxy_PageTracking_ResponseHeaders(t *testing.T) {
	// This test verifies that response headers are correctly captured and passed to PageTracker
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Custom-Header", "test-value")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Test</body></html>"))
	}))
	defer backend.Close()

	config := ProxyConfig{
		ID:         "test-headers",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	}

	ps, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer ps.Stop(ctx)

	// Wait for proxy to be ready
	select {
	case <-ps.Ready():
		// Server is ready
	case <-ctx.Done():
		t.Fatal("Context cancelled while waiting for proxy to be ready")
	}

	// Use ListenAddr directly since it now includes the bind address
	proxyURL := fmt.Sprintf("http://%s", ps.ListenAddr)

	// Make the test request
	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatalf("Failed to request: %v", err)
	}
	// Read full body to ensure complete response
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	// Check HTTP log entry has correct headers (synchronous - happens in request handler)
	entries := ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}, Limit: 1})
	if len(entries) < 1 {
		t.Fatalf("Expected at least 1 HTTP log entry, got %d", len(entries))
	}

	entry := entries[0].HTTP
	if entry == nil {
		t.Fatal("HTTP entry is nil")
	}

	t.Logf("Logged response headers: %+v", entry.ResponseHeaders)

	contentType := entry.ResponseHeaders["Content-Type"]
	if contentType == "" {
		// Check lowercase
		contentType = entry.ResponseHeaders["content-type"]
	}

	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Expected Content-Type to contain 'text/html', got '%s'", contentType)
		t.Logf("All response headers: %+v", entry.ResponseHeaders)
	}
}
