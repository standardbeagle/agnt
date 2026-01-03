//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestActivityBroadcast_EndToEnd tests the complete activity broadcast pipeline:
// ActivityMonitor -> Client.BroadcastActivity -> Daemon -> Proxy -> WebSocket -> Browser
func TestActivityBroadcast_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start daemon
	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Create a test HTTP server that we'll proxy to
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello from target"))
	}))
	defer targetServer.Close()

	// Connect client
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Create a proxy
	proxyResult, err := client.ProxyStart("test-proxy", targetServer.URL, 0, 0, "")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	listenAddr, ok := proxyResult["listen_addr"].(string)
	if !ok || listenAddr == "" {
		t.Fatalf("No listen_addr in proxy result: %v", proxyResult)
	}
	t.Logf("Proxy listening on: %s", listenAddr)

	// Give proxy a moment to start
	time.Sleep(100 * time.Millisecond)

	// Connect WebSocket client to the proxy
	wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr)
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect WebSocket: %v", err)
	}
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

	// Give WebSocket a moment to be fully registered
	time.Sleep(50 * time.Millisecond)

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Start daemon
	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Create a test HTTP server
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()

	// Connect client
	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Create a proxy
	proxyResult, err := client.ProxyStart("test-proxy", targetServer.URL, 0, 0, "")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	listenAddr := proxyResult["listen_addr"].(string)
	time.Sleep(100 * time.Millisecond)

	// Connect WebSocket
	wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr)
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect WebSocket: %v", err)
	}
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

	time.Sleep(50 * time.Millisecond)

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
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for output preview")
	}

	client.ProxyStop("test-proxy")
}

// TestActivityBroadcast_NoProxies verifies that broadcasting with no proxies doesn't error.
func TestActivityBroadcast_NoProxies(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Create target servers
	targetServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer1.Close()

	targetServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer2.Close()

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Create two proxies
	proxy1Result, err := client.ProxyStart("proxy1", targetServer1.URL, 0, 0, "")
	if err != nil {
		t.Fatalf("Failed to start proxy1: %v", err)
	}
	listenAddr1 := proxy1Result["listen_addr"].(string)

	proxy2Result, err := client.ProxyStart("proxy2", targetServer2.URL, 0, 0, "")
	if err != nil {
		t.Fatalf("Failed to start proxy2: %v", err)
	}
	listenAddr2 := proxy2Result["listen_addr"].(string)

	time.Sleep(100 * time.Millisecond)

	// Connect WebSockets to both proxies
	var receivedCount atomic.Int32
	var wg sync.WaitGroup

	connectAndListen := func(addr string) {
		wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", addr)
		wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Errorf("Failed to connect WebSocket to %s: %v", addr, err)
			return
		}
		defer wsConn.Close()

		wg.Add(1)
		go func() {
			defer wg.Done()
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

		// Keep connection alive until test ends
		time.Sleep(500 * time.Millisecond)
	}

	go connectAndListen(listenAddr1)
	go connectAndListen(listenAddr2)

	time.Sleep(100 * time.Millisecond)

	// Broadcast to all proxies
	if err := client.BroadcastActivity(true); err != nil {
		t.Fatalf("BroadcastActivity failed: %v", err)
	}

	// Wait for messages
	time.Sleep(200 * time.Millisecond)

	count := receivedCount.Load()
	if count < 2 {
		t.Errorf("Expected at least 2 activity messages (one per proxy), got %d", count)
	} else {
		t.Logf("Received %d activity messages across proxies", count)
	}

	client.ProxyStop("proxy1")
	client.ProxyStop("proxy2")
}

// TestActivityBroadcast_SpecificProxy tests broadcasting to a specific proxy only.
func TestActivityBroadcast_SpecificProxy(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	targetServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer1.Close()

	targetServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer2.Close()

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Create two proxies
	proxy1Result, _ := client.ProxyStart("target-proxy", targetServer1.URL, 0, 0, "")
	listenAddr1 := proxy1Result["listen_addr"].(string)

	proxy2Result, _ := client.ProxyStart("other-proxy", targetServer2.URL, 0, 0, "")
	listenAddr2 := proxy2Result["listen_addr"].(string)

	time.Sleep(100 * time.Millisecond)

	var proxy1Received, proxy2Received atomic.Int32

	// Connect to proxy 1
	ws1URL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr1)
	ws1, _, _ := websocket.DefaultDialer.Dial(ws1URL, nil)
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

	// Connect to proxy 2
	ws2URL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr2)
	ws2, _, _ := websocket.DefaultDialer.Dial(ws2URL, nil)
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

	time.Sleep(100 * time.Millisecond)

	// Broadcast to specific proxy only
	if err := client.BroadcastActivity(true, "target-proxy"); err != nil {
		t.Fatalf("BroadcastActivity failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

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
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()

	client := NewClient(WithSocketPath(sockPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	proxyResult, _ := client.ProxyStart("test-proxy", targetServer.URL, 0, 0, "")
	listenAddr := proxyResult["listen_addr"].(string)

	time.Sleep(100 * time.Millisecond)

	// Connect WebSocket
	wsURL := fmt.Sprintf("ws://%s/__devtool_metrics", listenAddr)
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect WebSocket: %v", err)
	}
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

	time.Sleep(50 * time.Millisecond)

	// Send rapid activity updates
	for i := 0; i < 100; i++ {
		if err := client.BroadcastActivity(i%2 == 0); err != nil {
			t.Fatalf("BroadcastActivity failed at iteration %d: %v", i, err)
		}
	}

	// Wait for messages
	time.Sleep(500 * time.Millisecond)

	count := receivedCount.Load()
	if count < 50 {
		t.Errorf("Expected at least 50 activity messages, got %d", count)
	} else {
		t.Logf("Received %d activity messages from rapid fire", count)
	}

	client.ProxyStop("test-proxy")
}
