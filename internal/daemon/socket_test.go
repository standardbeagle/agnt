//go:build unix

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDefaultSocketPath(t *testing.T) {
	path := DefaultSocketPath()
	if path == "" {
		t.Error("DefaultSocketPath() returned empty string")
	}

	// Should contain uid for uniqueness
	uid := os.Getuid()

	// If XDG_RUNTIME_DIR is set, path should be there
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		if !filepath.HasPrefix(path, xdg) {
			t.Errorf("Expected path to start with XDG_RUNTIME_DIR (%s), got %s", xdg, path)
		}
	} else {
		// Otherwise should be in /tmp with uid
		if !filepath.HasPrefix(path, "/tmp/") {
			t.Errorf("Expected path to start with /tmp/, got %s", path)
		}
		if !containsUID(path, uid) {
			t.Errorf("Expected path to contain UID %d, got %s", uid, path)
		}
	}

	t.Logf("DefaultSocketPath: %s", path)
}

func containsUID(path string, uid int) bool {
	return filepath.Base(path) == "devtool-mcp-"+strconv.Itoa(uid)+".sock" ||
		filepath.Base(path) == "devtool-mcp.sock"
}

func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	return err.Error() == "close unix: use of closed network connection" ||
		err.Error() == "use of closed network connection" ||
		// Also match partial error messages from wrapped errors
		contains(err.Error(), "use of closed network connection")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsString(s, substr))
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSocketManager_Listen(t *testing.T) {
	// Use temp directory for test socket
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	config := SocketConfig{
		Path: sockPath,
		Mode: 0600,
	}

	sm := NewSocketManager(config)

	// Listen should succeed
	listener, err := sm.Listen()
	if err != nil {
		t.Fatalf("Listen() failed: %v", err)
	}
	defer sm.Close()

	// Verify socket exists
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("Socket file not created: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Error("Created file is not a socket")
	}

	// Verify PID file exists
	pidFile := sockPath + ".pid"
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("PID file not created: %v", err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatalf("Invalid PID in file: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("PID file contains %d, expected %d", pid, os.Getpid())
	}

	// Verify we can connect
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Failed to connect to socket: %v", err)
	}
	conn.Close()

	// Second listen should fail
	sm2 := NewSocketManager(config)
	_, err = sm2.Listen()
	if err != ErrDaemonRunning {
		t.Errorf("Expected ErrDaemonRunning, got %v", err)
	}

	// Close and verify cleanup
	// Don't close listener separately - sm.Close() will do it
	err = sm.Close()
	// Ignore "use of closed network connection" since listener may be shared
	if err != nil && !isClosedConnErr(err) {
		t.Errorf("Close() failed: %v", err)
	}
	_ = listener // silence unused warning

	// Verify files are removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("Socket file not removed after Close()")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("PID file not removed after Close()")
	}
}

func TestSocketManager_CleanupStale(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "stale.sock")

	// Create a stale socket (no listener)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Failed to create test socket: %v", err)
	}
	listener.Close() // Close immediately to make it stale

	// Create a new socket manager
	config := SocketConfig{Path: sockPath}
	sm := NewSocketManager(config)

	// Listen should succeed (cleaning up stale socket)
	newListener, err := sm.Listen()
	if err != nil {
		t.Fatalf("Listen() failed after stale socket: %v", err)
	}
	defer sm.Close()

	// Verify new socket works
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Failed to connect to new socket: %v", err)
	}
	conn.Close()
	newListener.Close()
}

func TestConnect(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "connect.sock")

	// Connect to non-existent socket should fail
	_, err := Connect(sockPath)
	if err != ErrSocketNotFound {
		t.Errorf("Expected ErrSocketNotFound for non-existent socket, got %v", err)
	}

	// Create socket
	config := SocketConfig{Path: sockPath}
	sm := NewSocketManager(config)
	listener, err := sm.Listen()
	if err != nil {
		t.Fatalf("Listen() failed: %v", err)
	}
	defer sm.Close()

	// Accept connections in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Connect should succeed
	conn, err := Connect(sockPath)
	if err != nil {
		t.Fatalf("Connect() failed: %v", err)
	}
	conn.Close()
}

func TestIsRunning(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "running.sock")

	// Should return false for non-existent socket
	if IsRunning(sockPath) {
		t.Error("IsRunning() returned true for non-existent socket")
	}

	// Create socket
	config := SocketConfig{Path: sockPath}
	sm := NewSocketManager(config)
	listener, err := sm.Listen()
	if err != nil {
		t.Fatalf("Listen() failed: %v", err)
	}
	defer sm.Close()

	// Accept connections in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Should return true for running daemon
	if !IsRunning(sockPath) {
		t.Error("IsRunning() returned false for running socket")
	}

	// Close and wait
	listener.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
	}

	// Should return false after close
	if IsRunning(sockPath) {
		t.Error("IsRunning() returned true after socket closed")
	}
}

func TestSocketPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "perms.sock")

	// Test with custom permissions
	config := SocketConfig{
		Path: sockPath,
		Mode: 0660,
	}

	sm := NewSocketManager(config)
	_, err := sm.Listen()
	if err != nil {
		t.Fatalf("Listen() failed: %v", err)
	}
	defer sm.Close()

	// Verify permissions
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("Failed to stat socket: %v", err)
	}

	// Socket mode includes the socket type bit, so mask it out
	mode := info.Mode() & os.ModePerm
	if mode != 0660 {
		t.Errorf("Socket permissions = %o, want %o", mode, 0660)
	}
}

func TestSocketManager_Path(t *testing.T) {
	config := SocketConfig{Path: "/custom/path.sock"}
	sm := NewSocketManager(config)

	if sm.Path() != "/custom/path.sock" {
		t.Errorf("Path() = %s, want /custom/path.sock", sm.Path())
	}
}

func TestNewSocketManager_Defaults(t *testing.T) {
	// Test with empty config
	sm := NewSocketManager(SocketConfig{})

	if sm.config.Path == "" {
		t.Error("Default path not set")
	}
	if sm.config.Mode != 0600 {
		t.Errorf("Default mode = %o, want %o", sm.config.Mode, 0600)
	}
}
