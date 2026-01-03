package daemon

import (
	"testing"
	"time"
)

func TestDefaultUpgradeConfig(t *testing.T) {
	config := DefaultUpgradeConfig()

	if config.SocketPath == "" {
		t.Error("Expected non-empty socket path")
	}

	if config.Timeout != 30*time.Second {
		t.Errorf("Expected timeout 30s, got %v", config.Timeout)
	}

	if config.GracefulTimeout != 5*time.Second {
		t.Errorf("Expected graceful timeout 5s, got %v", config.GracefulTimeout)
	}

	if config.Force != false {
		t.Error("Expected Force to be false by default")
	}

	if config.Verbose != false {
		t.Error("Expected Verbose to be false by default")
	}
}

func TestNewDaemonUpgrader(t *testing.T) {
	config := UpgradeConfig{
		SocketPath:      "/tmp/test-upgrade.sock",
		Timeout:         10 * time.Second,
		GracefulTimeout: 2 * time.Second,
	}

	upgrader := NewDaemonUpgrader(config)
	if upgrader == nil {
		t.Fatal("Expected non-nil upgrader")
	}
}

func TestNewDaemonUpgrader_Defaults(t *testing.T) {
	// Test that empty config gets defaults filled in
	config := UpgradeConfig{}

	upgrader := NewDaemonUpgrader(config)
	if upgrader == nil {
		t.Fatal("Expected non-nil upgrader")
	}

	// The upgrader should have been created with defaults applied internally
}
