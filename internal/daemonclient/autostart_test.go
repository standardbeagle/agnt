package daemonclient

import (
	"testing"
	"time"
)

func TestDefaultAutoStartConfig(t *testing.T) {
	config := DefaultAutoStartConfig()

	if config.SocketPath == "" {
		t.Error("Expected non-empty socket path")
	}
	if config.StartTimeout == 0 {
		t.Error("Expected non-zero start timeout")
	}
	if config.RetryInterval == 0 {
		t.Error("Expected non-zero retry interval")
	}
	if config.MaxRetries == 0 {
		t.Error("Expected non-zero max retries")
	}
}

func TestAutoStartConfig_toLibraryConfig(t *testing.T) {
	config := AutoStartConfig{
		SocketPath:    "/tmp/test.sock",
		DaemonPath:    "/usr/bin/daemon",
		StartTimeout:  10 * time.Second,
		RetryInterval: 200 * time.Millisecond,
		MaxRetries:    20,
	}

	libConfig := config.toLibraryConfig()

	if libConfig.SocketPath != config.SocketPath {
		t.Errorf("Expected SocketPath %s, got %s", config.SocketPath, libConfig.SocketPath)
	}
	if libConfig.HubPath != config.DaemonPath {
		t.Errorf("Expected HubPath %s, got %s", config.DaemonPath, libConfig.HubPath)
	}
	if libConfig.StartTimeout != config.StartTimeout {
		t.Errorf("Expected StartTimeout %v, got %v", config.StartTimeout, libConfig.StartTimeout)
	}
	if libConfig.RetryInterval != config.RetryInterval {
		t.Errorf("Expected RetryInterval %v, got %v", config.RetryInterval, libConfig.RetryInterval)
	}
	if libConfig.MaxRetries != config.MaxRetries {
		t.Errorf("Expected MaxRetries %d, got %d", config.MaxRetries, libConfig.MaxRetries)
	}
	if libConfig.ProcessMatcher == nil {
		t.Error("Expected ProcessMatcher to be set")
	}
}

func TestNewAutoStartClient(t *testing.T) {
	config := AutoStartConfig{
		SocketPath:    "/tmp/test-newclient.sock",
		DaemonPath:    "/usr/bin/daemon",
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    10,
	}

	client := NewAutoStartClient(config)
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
	if client.Client == nil {
		t.Error("Expected non-nil embedded Client")
	}
}
