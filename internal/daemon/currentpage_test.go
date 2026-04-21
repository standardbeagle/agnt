//go:build unix

package daemon

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

func TestClient_CurrentPage_EndToEnd(t *testing.T) {
	_, client, _ := newBootedDaemonWithClient(t)

	// Create a backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><head></head><body>Test Page</body></html>"))
	}))
	defer backend.Close()

	// Start a proxy
	result, err := client.ProxyStart("test-proxy", backend.URL, 0, 100, ".")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	listenAddr := result["listen_addr"].(string)
	t.Logf("Proxy listening on: %s", listenAddr)

	// Use ListenAddr directly since it now includes the bind address
	proxyURL := fmt.Sprintf("http://%s", listenAddr)

	// Make a request through the proxy
	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatalf("Failed to request through proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	t.Logf("Response status: %d, body length: %d", resp.StatusCode, len(body))

	// Small delay to ensure tracking is complete
	time.Sleep(100 * time.Millisecond)

	// Check current page sessions
	sessionsResult, err := client.CurrentPageList("test-proxy")
	if err != nil {
		t.Fatalf("Failed to get current page list: %v", err)
	}

	t.Logf("CurrentPageList result: %+v", sessionsResult)

	count, ok := sessionsResult["count"].(float64)
	if !ok {
		t.Fatalf("Expected count field, got: %+v", sessionsResult)
	}

	if count < 1 {
		t.Errorf("Expected at least 1 page session, got %v", count)

		// Debug: check proxy logs
		logResult, err := client.ProxyLogQuery("test-proxy", protocol.LogQueryFilter{
			Types: []string{"http"},
			Limit: 10,
		})
		if err != nil {
			t.Logf("Failed to query logs: %v", err)
		} else {
			t.Logf("Proxy logs: %+v", logResult)
		}
	}

	sessions, ok := sessionsResult["sessions"].([]interface{})
	if ok && len(sessions) > 0 {
		firstSession := sessions[0].(map[string]interface{})
		t.Logf("First session URL: %s", firstSession["url"])
		t.Logf("First session active: %v", firstSession["active"])
	}

	// Clean up
	if err := client.ProxyStop("test-proxy"); err != nil {
		t.Logf("Failed to stop proxy: %v", err)
	}
}
