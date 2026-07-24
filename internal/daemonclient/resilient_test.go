//go:build unix

package daemonclient

import (
	"testing"
	"time"
)

func TestDefaultResilientClientConfig(t *testing.T) {
	config := DefaultResilientClientConfig()

	if config.HeartbeatInterval == 0 {
		t.Error("Expected non-zero HeartbeatInterval")
	}
	if config.HeartbeatTimeout == 0 {
		t.Error("Expected non-zero HeartbeatTimeout")
	}
	if config.ReconnectBackoffMin == 0 {
		t.Error("Expected non-zero ReconnectBackoffMin")
	}
	if config.ReconnectBackoffMax == 0 {
		t.Error("Expected non-zero ReconnectBackoffMax")
	}
	// MaxReconnectAttempts should be 0 (unlimited)
	if config.MaxReconnectAttempts != 0 {
		t.Errorf("Expected MaxReconnectAttempts=0, got %d", config.MaxReconnectAttempts)
	}
}

func TestNewResilientClient(t *testing.T) {
	config := ResilientClientConfig{
		AutoStartConfig: AutoStartConfig{
			SocketPath:   "/tmp/test-resilient.sock",
			DaemonPath:   "test-daemon",
			StartTimeout: 5 * time.Second,
		},
		HeartbeatInterval:   5 * time.Second,
		HeartbeatTimeout:    3 * time.Second,
		ReconnectBackoffMin: 100 * time.Millisecond,
		ReconnectBackoffMax: 10 * time.Second,
	}

	rc := NewResilientClient(config)
	if rc == nil {
		t.Fatal("Expected non-nil ResilientClient")
	}

	// Not connected yet
	if rc.IsConnected() {
		t.Error("Expected not connected before Connect()")
	}
	if rc.IsReconnecting() {
		t.Error("Expected not reconnecting before Connect()")
	}

	// Client should be nil when not connected
	if rc.Client() != nil {
		t.Error("Expected nil Client when not connected")
	}
}

func TestNewResilientClient_WithVersionCheck(t *testing.T) {
	versionMismatchCalled := false

	config := ResilientClientConfig{
		AutoStartConfig: AutoStartConfig{
			SocketPath: "/tmp/test-resilient-version.sock",
		},
		ClientVersion: "1.0.0",
		OnVersionMismatch: func(clientVer, daemonVer string) error {
			versionMismatchCalled = true
			return nil
		},
	}

	rc := NewResilientClient(config)
	if rc == nil {
		t.Fatal("Expected non-nil ResilientClient")
	}

	// Version mismatch won't be called until Connect()
	if versionMismatchCalled {
		t.Error("Version mismatch should not be called before connect")
	}
}

func TestNewResilientClient_WithCallbacks(t *testing.T) {
	disconnectCalled := false
	reconnectFailedCalled := false
	reconnectCalled := false

	config := ResilientClientConfig{
		AutoStartConfig: AutoStartConfig{
			SocketPath: "/tmp/test-resilient-callbacks.sock",
		},
		OnDisconnect: func(err error) {
			disconnectCalled = true
		},
		OnReconnectFailed: func(err error) {
			reconnectFailedCalled = true
		},
		OnReconnect: func(client *Client) error {
			reconnectCalled = true
			return nil
		},
	}

	rc := NewResilientClient(config)
	if rc == nil {
		t.Fatal("Expected non-nil ResilientClient")
	}

	// Callbacks won't be called without actual connection
	if disconnectCalled || reconnectFailedCalled || reconnectCalled {
		t.Error("Callbacks should not be called before any connection activity")
	}
}

func TestResilientClient_Stats(t *testing.T) {
	config := ResilientClientConfig{
		AutoStartConfig: AutoStartConfig{
			SocketPath: "/tmp/test-resilient-stats.sock",
		},
	}

	rc := NewResilientClient(config)
	stats := rc.Stats()
	if stats == nil {
		t.Error("Expected non-nil stats")
	}

	t.Logf("Stats: %+v", stats)
}
