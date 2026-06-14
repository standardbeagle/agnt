//go:build unix

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// attachProjectSession registers and attaches a session for projectDir so the
// connection is session-scoped. OVERLAY ACTIVITY / OUTPUT-PREVIEW with no
// explicit proxy IDs scope to the connection's session project; without an
// attached session they fail loud (never fan out across every project), so
// these end-to-end tests must establish one and create their proxies under the
// same project path.
func attachProjectSession(t *testing.T, c *Client, projectDir string) {
	t.Helper()
	if _, err := c.SessionRegister("sess-"+sanitizeSessionCode(projectDir), projectDir, projectDir, "test-cmd", nil); err != nil {
		t.Fatalf("SessionRegister failed: %v", err)
	}
	if _, err := c.SessionAttach(projectDir); err != nil {
		t.Fatalf("SessionAttach failed: %v", err)
	}
}

func sanitizeSessionCode(dir string) string {
	out := make([]rune, 0, len(dir))
	for _, r := range dir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		}
	}
	return string(out)
}

// TestActivityBroadcast_EndToEnd tests the complete activity broadcast pipeline:
// ActivityMonitor -> Client.BroadcastActivity -> Daemon -> Proxy -> WebSocket -> Browser
func TestActivityBroadcast_EndToEnd(t *testing.T) {
	t.Parallel()
	_, client, projectDir := newBootedDaemonWithClient(t)
	attachProjectSession(t, client, projectDir)

	// Create a test HTTP server that we'll proxy to
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello from target"))
	}))
	defer targetServer.Close()

	// Create a proxy under this session's project
	proxyResult, err := client.ProxyStart("test-proxy", targetServer.URL, 0, 0, projectDir)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	listenAddr, ok := proxyResult["listen_addr"].(string)
	if !ok || listenAddr == "" {
		t.Fatalf("No listen_addr in proxy result: %v", proxyResult)
	}
	t.Logf("Proxy listening on: %s", listenAddr)

	// Retry WS dial until proxy is accepting connections.
	wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr)
	var wsConn *websocket.Conn
	require.Eventually(t, func() bool {
		conn, _, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
		if dialErr != nil {
			return false
		}
		wsConn = conn
		return true
	}, 5*time.Second, 10*time.Millisecond, "failed to connect WebSocket to proxy")
	defer wsConn.Close()

	// Channel to receive activity messages
	activityReceived := make(chan bool, 10)
	wsErrors := make(chan error, 1)

	// Read WebSocket messages in goroutine
	go func() {
		for {
			_, message, err := wsConn.ReadMessage()
			if err != nil {
				wsErrors <- err
				return
			}

			var msg struct {
				Type    string `json:"type"`
				Payload struct {
					Active bool `json:"active"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(message, &msg); err != nil {
				continue
			}

			if msg.Type == "activity" {
				activityReceived <- msg.Payload.Active
			}
		}
	}()

	// Broadcast activity state (active)
	if err := client.BroadcastActivity(true); err != nil {
		t.Fatalf("BroadcastActivity(true) failed: %v", err)
	}

	// Wait for activity message
	select {
	case active := <-activityReceived:
		if !active {
			t.Errorf("Expected active=true, got active=false")
		}
		t.Log("Received activity=true message")
	case err := <-wsErrors:
		t.Fatalf("WebSocket error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for activity message")
	}

	// Broadcast activity state (idle)
	if err := client.BroadcastActivity(false); err != nil {
		t.Fatalf("BroadcastActivity(false) failed: %v", err)
	}

	// Wait for idle message
	select {
	case active := <-activityReceived:
		if active {
			t.Errorf("Expected active=false, got active=true")
		}
		t.Log("Received activity=false message")
	case err := <-wsErrors:
		t.Fatalf("WebSocket error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for idle message")
	}

	// Cleanup
	if err := client.ProxyStop("test-proxy"); err != nil {
		t.Logf("Warning: ProxyStop failed: %v", err)
	}
}

// TestOutputPreviewBroadcast_EndToEnd tests the output preview broadcast pipeline.
func TestOutputPreviewBroadcast_EndToEnd(t *testing.T) {
	t.Parallel()
	_, client, projectDir := newBootedDaemonWithClient(t)
	attachProjectSession(t, client, projectDir)

	// Create a test HTTP server
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()

	// Create a proxy under this session's project
	proxyResult, err := client.ProxyStart("test-proxy", targetServer.URL, 0, 0, projectDir)
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	listenAddr := proxyResult["listen_addr"].(string)

	wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr)
	var wsConn *websocket.Conn
	require.Eventually(t, func() bool {
		conn, _, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
		if dialErr != nil {
			return false
		}
		wsConn = conn
		return true
	}, 5*time.Second, 10*time.Millisecond, "failed to connect WebSocket to proxy")
	defer wsConn.Close()

	// Channel to receive output preview
	previewReceived := make(chan []string, 10)

	go func() {
		for {
			_, message, err := wsConn.ReadMessage()
			if err != nil {
				return
			}

			var msg struct {
				Type    string `json:"type"`
				Payload struct {
					Lines []string `json:"lines"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(message, &msg); err != nil {
				continue
			}

			if msg.Type == "output_preview" {
				previewReceived <- msg.Payload.Lines
			}
		}
	}()

	// Broadcast output preview
	testLines := []string{"Building project...", "Compiling main.go", "Done!"}
	if err := client.BroadcastOutputPreview(testLines); err != nil {
		t.Fatalf("BroadcastOutputPreview failed: %v", err)
	}

	// Wait for preview message
	select {
	case lines := <-previewReceived:
		if len(lines) != len(testLines) {
			t.Errorf("Expected %d lines, got %d", len(testLines), len(lines))
		}
		for i, line := range lines {
			if line != testLines[i] {
				t.Errorf("Line %d: expected %q, got %q", i, testLines[i], line)
			}
		}
		t.Logf("Received output preview: %v", lines)
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for output preview")
	}

	client.ProxyStop("test-proxy")
}

// TestActivityBroadcast_NoProxies verifies that broadcasting with no proxies doesn't error.
func TestActivityBroadcast_NoProxies(t *testing.T) {
	t.Parallel()
	_, client, projectDir := newBootedDaemonWithClient(t)
	attachProjectSession(t, client, projectDir)

	// Should not error even with no proxies
	if err := client.BroadcastActivity(true); err != nil {
		t.Errorf("BroadcastActivity should not error with no proxies: %v", err)
	}

	if err := client.BroadcastOutputPreview([]string{"test"}); err != nil {
		t.Errorf("BroadcastOutputPreview should not error with no proxies: %v", err)
	}
}

// TestActivityBroadcast_MultipleProxies tests broadcasting to multiple proxies.
func TestActivityBroadcast_MultipleProxies(t *testing.T) {
	t.Parallel()
	_, client, projectDir := newBootedDaemonWithClient(t)
	attachProjectSession(t, client, projectDir)

	// Create target servers
	targetServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer1.Close()

	targetServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer2.Close()

	// Create two proxies under this session's project
	proxy1Result, err := client.ProxyStart("proxy1", targetServer1.URL, 0, 0, projectDir)
	if err != nil {
		t.Fatalf("Failed to start proxy1: %v", err)
	}
	listenAddr1 := proxy1Result["listen_addr"].(string)

	proxy2Result, err := client.ProxyStart("proxy2", targetServer2.URL, 0, 0, projectDir)
	if err != nil {
		t.Fatalf("Failed to start proxy2: %v", err)
	}
	listenAddr2 := proxy2Result["listen_addr"].(string)

	// Connect WebSockets to both proxies (retry until proxy is accepting).
	dialWS := func(addr string) *websocket.Conn {
		wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", addr)
		var conn *websocket.Conn
		require.Eventually(t, func() bool {
			c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return false
			}
			conn = c
			return true
		}, 5*time.Second, 10*time.Millisecond, "WebSocket dial to %s failed", addr)
		return conn
	}

	ws1 := dialWS(listenAddr1)
	defer ws1.Close()
	ws2 := dialWS(listenAddr2)
	defer ws2.Close()

	received1 := make(chan struct{}, 10)
	received2 := make(chan struct{}, 10)

	listenWS := func(conn *websocket.Conn, ch chan struct{}) {
		go func() {
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var msg struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(message, &msg) == nil && msg.Type == "activity" {
					ch <- struct{}{}
				}
			}
		}()
	}
	listenWS(ws1, received1)
	listenWS(ws2, received2)

	// Broadcast to all proxies
	if err := client.BroadcastActivity(true); err != nil {
		t.Fatalf("BroadcastActivity failed: %v", err)
	}

	// Each proxy's WS must receive the message. Budget is 5s because the
	// race detector adds significant scheduling latency under parallel load.
	select {
	case <-received1:
	case <-time.After(5 * time.Second):
		t.Error("proxy1 did not receive activity message")
	}
	select {
	case <-received2:
	case <-time.After(5 * time.Second):
		t.Error("proxy2 did not receive activity message")
	}

	client.ProxyStop("proxy1")
	client.ProxyStop("proxy2")
}

// TestActivityBroadcast_SpecificProxy tests broadcasting to a specific proxy only.
func TestActivityBroadcast_SpecificProxy(t *testing.T) {
	t.Parallel()
	_, client, _ := newBootedDaemonWithClient(t)

	targetServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer1.Close()

	targetServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer2.Close()

	// Create two proxies
	proxy1Result, _ := client.ProxyStart("target-proxy", targetServer1.URL, 0, 0, "")
	listenAddr1 := proxy1Result["listen_addr"].(string)

	proxy2Result, _ := client.ProxyStart("other-proxy", targetServer2.URL, 0, 0, "")
	listenAddr2 := proxy2Result["listen_addr"].(string)

	dialWS := func(addr string) *websocket.Conn {
		wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", addr)
		var conn *websocket.Conn
		require.Eventually(t, func() bool {
			c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				return false
			}
			conn = c
			return true
		}, 5*time.Second, 10*time.Millisecond, "WebSocket dial to %s failed", addr)
		return conn
	}

	var proxy1Received, proxy2Received atomic.Int32

	ws1 := dialWS(listenAddr1)
	defer ws1.Close()

	go func() {
		for {
			_, message, err := ws1.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message, &msg) == nil && msg.Type == "activity" {
				proxy1Received.Add(1)
			}
		}
	}()

	ws2 := dialWS(listenAddr2)
	defer ws2.Close()

	go func() {
		for {
			_, message, err := ws2.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message, &msg) == nil && msg.Type == "activity" {
				proxy2Received.Add(1)
			}
		}
	}()

	// Broadcast to specific proxy only
	if err := client.BroadcastActivity(true, "target-proxy"); err != nil {
		t.Fatalf("BroadcastActivity failed: %v", err)
	}

	require.Eventually(t, func() bool {
		return proxy1Received.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond, "target-proxy should have received activity message")

	if proxy1Received.Load() != 1 {
		t.Errorf("target-proxy should have received 1 message, got %d", proxy1Received.Load())
	}
	if proxy2Received.Load() != 0 {
		t.Errorf("other-proxy should have received 0 messages, got %d", proxy2Received.Load())
	}

	client.ProxyStop("target-proxy")
	client.ProxyStop("other-proxy")
}

// TestActivityBroadcast_RapidFire tests that rapid activity updates are handled correctly.
func TestActivityBroadcast_RapidFire(t *testing.T) {
	t.Parallel()
	_, client, projectDir := newBootedDaemonWithClient(t)
	attachProjectSession(t, client, projectDir)

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()

	proxyResult, _ := client.ProxyStart("test-proxy", targetServer.URL, 0, 0, projectDir)
	listenAddr := proxyResult["listen_addr"].(string)

	// Connect WebSocket (retry until proxy accepts connections)
	wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr)
	var wsConn *websocket.Conn
	require.Eventually(t, func() bool {
		conn, _, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
		if dialErr != nil {
			return false
		}
		wsConn = conn
		return true
	}, 5*time.Second, 10*time.Millisecond, "failed to connect WebSocket to proxy")
	defer wsConn.Close()

	var receivedCount atomic.Int32
	go func() {
		for {
			_, message, err := wsConn.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message, &msg) == nil && msg.Type == "activity" {
				receivedCount.Add(1)
			}
		}
	}()

	// Send rapid activity updates
	for i := 0; i < 100; i++ {
		if err := client.BroadcastActivity(i%2 == 0); err != nil {
			t.Fatalf("BroadcastActivity failed at iteration %d: %v", i, err)
		}
	}

	// Wait for at least 50 messages to arrive
	require.Eventually(t, func() bool {
		return receivedCount.Load() >= 50
	}, 5*time.Second, 10*time.Millisecond, "expected at least 50 activity messages")

	t.Logf("Received %d activity messages from rapid fire", receivedCount.Load())

	client.ProxyStop("test-proxy")
}
