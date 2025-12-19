package daemon

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

func TestNewConn(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	if conn == nil {
		t.Fatal("NewConn returned nil")
	}
	if conn.SocketPath() != "/tmp/test.sock" {
		t.Errorf("SocketPath = %q, want %q", conn.SocketPath(), "/tmp/test.sock")
	}
}

func TestNewConn_DefaultSocketPath(t *testing.T) {
	conn := NewConn("")
	if conn == nil {
		t.Fatal("NewConn returned nil")
	}
	if conn.SocketPath() == "" {
		t.Error("SocketPath should use default when empty string provided")
	}
}

func TestConn_SetTimeout(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	conn.SetTimeout(5 * time.Second)
	// Timeout is internal, but we can verify no panic/error
	if conn.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want %v", conn.timeout, 5*time.Second)
	}
}

func TestConn_IsConnected_NotConnected(t *testing.T) {
	conn := NewConn("/tmp/nonexistent.sock")
	if conn.IsConnected() {
		t.Error("IsConnected should return false for new connection")
	}
}

func TestConn_Close_NotConnected(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	err := conn.Close()
	if err != nil {
		t.Errorf("Close on unconnected Conn should not error: %v", err)
	}
}

func TestConn_Close_Twice(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	conn.Close()
	err := conn.Close()
	if err != nil {
		t.Errorf("Double Close should not error: %v", err)
	}
}

func TestConn_Disconnect_NotConnected(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	err := conn.Disconnect()
	if err != nil {
		t.Errorf("Disconnect on unconnected Conn should not error: %v", err)
	}
}

func TestConn_EnsureConnected_Closed(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	conn.Close()
	err := conn.EnsureConnected()
	if err != ErrConnectionClosed {
		t.Errorf("EnsureConnected after Close should return ErrConnectionClosed, got %v", err)
	}
}

func TestConn_Request_BuildsCorrectly(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	rb := conn.Request("PROC", "LIST")
	if rb == nil {
		t.Fatal("Request returned nil")
	}
	if rb.verb != "PROC" {
		t.Errorf("verb = %q, want %q", rb.verb, "PROC")
	}
	if len(rb.args) != 1 || rb.args[0] != "LIST" {
		t.Errorf("args = %v, want [LIST]", rb.args)
	}
}

func TestRequestBuilder_WithArgs(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	rb := conn.Request("PROC", "OUTPUT", "test-id").WithArgs("tail=50", "stream=stderr")
	expected := []string{"OUTPUT", "test-id", "tail=50", "stream=stderr"}
	if len(rb.args) != len(expected) {
		t.Fatalf("args length = %d, want %d", len(rb.args), len(expected))
	}
	for i, arg := range expected {
		if rb.args[i] != arg {
			t.Errorf("args[%d] = %q, want %q", i, rb.args[i], arg)
		}
	}
}

func TestRequestBuilder_WithData(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	data := []byte("test data")
	rb := conn.Request("INFO").WithData(data)
	if string(rb.data) != "test data" {
		t.Errorf("data = %q, want %q", string(rb.data), "test data")
	}
}

func TestRequestBuilder_WithJSON(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	filter := map[string]bool{"global": true}
	rb := conn.Request("PROC", "LIST").WithJSON(filter)
	if rb.data == nil {
		t.Fatal("WithJSON should set data")
	}
	var decoded map[string]bool
	if err := json.Unmarshal(rb.data, &decoded); err != nil {
		t.Fatalf("WithJSON data is not valid JSON: %v", err)
	}
	if !decoded["global"] {
		t.Error("WithJSON should preserve data")
	}
}

func TestRequestBuilder_WithJSON_InvalidType(t *testing.T) {
	conn := NewConn("/tmp/test.sock")
	// channels cannot be marshaled to JSON
	ch := make(chan int)
	rb := conn.Request("INFO").WithJSON(ch)
	// Should handle gracefully (data will be nil)
	if rb.data != nil {
		t.Error("WithJSON with unmarshalable type should set data to nil")
	}
}

// mockDaemonServer creates a mock daemon server for testing
type mockDaemonServer struct {
	listener net.Listener
	handler  func(net.Conn)
	done     chan struct{}
	ready    chan struct{}
}

func newMockDaemonServer(t *testing.T, handler func(net.Conn)) *mockDaemonServer {
	t.Helper()
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create mock server: %v", err)
	}

	server := &mockDaemonServer{
		listener: listener,
		handler:  handler,
		done:     make(chan struct{}),
		ready:    make(chan struct{}),
	}

	go func() {
		close(server.ready) // Signal that we're ready to accept
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-server.done:
					return
				default:
					continue
				}
			}
			go handler(conn)
		}
	}()

	// Wait for server to be ready
	<-server.ready
	// Small additional delay to ensure Accept is blocking
	time.Sleep(5 * time.Millisecond)

	return server
}

func (s *mockDaemonServer) SocketPath() string {
	return s.listener.Addr().String()
}

func (s *mockDaemonServer) Close() {
	close(s.done)
	s.listener.Close()
}

func TestConn_Ping(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		cmd, err := parser.ParseCommand()
		if err != nil {
			return
		}
		if cmd.Verb != protocol.VerbPing {
			writer.WriteErr(protocol.ErrInvalidCommand, "expected PING")
			return
		}
		writer.WritePong()
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	err := conn.Ping()
	if err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}

func TestConn_Request_OK(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		_, err := parser.ParseCommand()
		if err != nil {
			return
		}
		writer.WriteOK("success")
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	err := conn.Request("PROXY", "STOP", "test-id").OK()
	if err != nil {
		t.Errorf("OK request failed: %v", err)
	}
}

func TestConn_Request_OK_Error(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		_, err := parser.ParseCommand()
		if err != nil {
			return
		}
		writer.WriteErr(protocol.ErrNotFound, "proxy not found")
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	err := conn.Request("PROXY", "STOP", "nonexistent").OK()
	if err == nil {
		t.Error("OK should return error when server returns ERR")
	}
}

func TestConn_Request_JSON(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		_, err := parser.ParseCommand()
		if err != nil {
			return
		}
		response := map[string]interface{}{
			"processes": []interface{}{
				map[string]interface{}{"id": "test-1", "state": "running"},
			},
		}
		data, _ := json.Marshal(response)
		writer.WriteJSON(data)
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	result, err := conn.Request("PROC", "LIST").JSON()
	if err != nil {
		t.Fatalf("JSON request failed: %v", err)
	}

	processes, ok := result["processes"].([]interface{})
	if !ok || len(processes) != 1 {
		t.Errorf("Unexpected result: %v", result)
	}
}

func TestConn_Request_JSONInto(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		_, err := parser.ParseCommand()
		if err != nil {
			return
		}
		response := map[string]string{"status": "ok", "version": "1.0.0"}
		data, _ := json.Marshal(response)
		writer.WriteJSON(data)
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	var result struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	err := conn.Request("INFO").JSONInto(&result)
	if err != nil {
		t.Fatalf("JSONInto request failed: %v", err)
	}
	if result.Status != "ok" || result.Version != "1.0.0" {
		t.Errorf("Unexpected result: %+v", result)
	}
}

func TestConn_Request_Bytes(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		_, err := parser.ParseCommand()
		if err != nil {
			return
		}
		response := map[string]string{"raw": "data"}
		data, _ := json.Marshal(response)
		writer.WriteJSON(data)
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	// Use valid verb INFO instead of invalid TEST
	data, err := conn.Request("INFO").Bytes()
	if err != nil {
		t.Fatalf("Bytes request failed: %v", err)
	}
	if string(data) != `{"raw":"data"}` {
		t.Errorf("Unexpected data: %s", string(data))
	}
}

func TestConn_Request_Chunked(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		_, err := parser.ParseCommand()
		if err != nil {
			return
		}
		// Send chunked response
		writer.WriteChunk([]byte("line 1\n"))
		writer.WriteChunk([]byte("line 2\n"))
		writer.WriteChunk([]byte("line 3\n"))
		writer.WriteEnd()
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	data, err := conn.Request("PROC", "OUTPUT", "test-id").Chunked()
	if err != nil {
		t.Fatalf("Chunked request failed: %v", err)
	}
	expected := "line 1\nline 2\nline 3\n"
	if string(data) != expected {
		t.Errorf("Chunked data = %q, want %q", string(data), expected)
	}
}

func TestConn_Request_String(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		_, err := parser.ParseCommand()
		if err != nil {
			return
		}
		writer.WriteChunk([]byte("output text"))
		writer.WriteEnd()
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	output, err := conn.Request("PROC", "OUTPUT", "test-id").String()
	if err != nil {
		t.Fatalf("String request failed: %v", err)
	}
	if output != "output text" {
		t.Errorf("String output = %q, want %q", output, "output text")
	}
}

func TestConn_Sequential_Requests(t *testing.T) {
	// Test that multiple sequential requests work on same connection
	// (Conn serializes concurrent requests internally, so we test sequential behavior)
	requestCount := 0

	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		for {
			_, err := parser.ParseCommand()
			if err != nil {
				return
			}
			requestCount++
			response := map[string]int{"count": requestCount}
			data, _ := json.Marshal(response)
			writer.WriteJSON(data)
		}
	})
	defer server.Close()

	// Wait for server to be ready
	time.Sleep(10 * time.Millisecond)

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	// Make 5 sequential requests
	for i := 1; i <= 5; i++ {
		result, err := conn.Request("INFO").JSON()
		if err != nil {
			t.Fatalf("Request %d failed: %v", i, err)
		}
		count := int(result["count"].(float64))
		if count != i {
			t.Errorf("Request %d: count = %d, want %d", i, count, i)
		}
	}

	if requestCount != 5 {
		t.Errorf("Expected 5 requests, got %d", requestCount)
	}
}

func TestConn_AutoReconnect(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Start server
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer listener.Close()

	requestCount := 0
	serverReady := make(chan struct{})
	go func() {
		close(serverReady)
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				return
			}
			parser := protocol.NewParser(clientConn)
			writer := protocol.NewWriter(clientConn)

			_, err = parser.ParseCommand()
			if err != nil {
				clientConn.Close()
				continue
			}
			requestCount++
			writer.WriteOK("ok")
			// Give client time to read response before closing
			time.Sleep(10 * time.Millisecond)
			clientConn.Close() // Close after each request to test reconnect
		}
	}()

	<-serverReady
	time.Sleep(10 * time.Millisecond) // Ensure server is listening

	conn := NewConn(socketPath)
	defer conn.Close()

	// First request
	err = conn.Request("INFO").OK()
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}

	// Wait for server to close the connection
	time.Sleep(20 * time.Millisecond)

	// Second request - connection was closed by server, so client will get error
	// on first attempt, but handleErrorLocked clears connection
	err = conn.Request("INFO").OK()
	if err != nil {
		// First attempt after server close may fail - that's expected
		// The important thing is that next request works (auto-reconnect)
		err = conn.Request("INFO").OK()
		if err != nil {
			t.Fatalf("Request after reconnect failed: %v", err)
		}
	}

	if requestCount < 2 {
		t.Errorf("Expected at least 2 requests, got %d", requestCount)
	}
}

func TestConn_EnsureConnected_Success(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		// Just accept and hold connection
		buf := make([]byte, 1)
		conn.Read(buf)
	})
	defer server.Close()

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	err := conn.EnsureConnected()
	if err != nil {
		t.Errorf("EnsureConnected failed: %v", err)
	}

	if !conn.IsConnected() {
		t.Error("IsConnected should return true after EnsureConnected succeeds")
	}
}

func TestConn_EnsureConnected_Failure(t *testing.T) {
	conn := NewConn("/tmp/nonexistent-socket-12345.sock")
	err := conn.EnsureConnected()
	if err == nil {
		t.Error("EnsureConnected should fail for nonexistent socket")
	}
}

func TestConn_Disconnect_ThenReconnect(t *testing.T) {
	server := newMockDaemonServer(t, func(conn net.Conn) {
		defer conn.Close()
		parser := protocol.NewParser(conn)
		writer := protocol.NewWriter(conn)

		for {
			_, err := parser.ParseCommand()
			if err != nil {
				return
			}
			writer.WriteOK("ok")
		}
	})
	defer server.Close()

	// Wait for server to be ready
	time.Sleep(10 * time.Millisecond)

	conn := NewConn(server.SocketPath())
	defer conn.Close()

	// Connect
	err := conn.EnsureConnected()
	if err != nil {
		t.Fatalf("Initial connect failed: %v", err)
	}

	// Disconnect
	err = conn.Disconnect()
	if err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}

	if conn.IsConnected() {
		t.Error("IsConnected should be false after Disconnect")
	}

	// Wait for server to accept the closed connection and be ready for new one
	time.Sleep(10 * time.Millisecond)

	// Should be able to reconnect
	err = conn.Request("INFO").OK()
	if err != nil {
		t.Fatalf("Request after Disconnect failed: %v", err)
	}
}
