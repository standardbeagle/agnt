package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"
)

func TestDefaultUpgradeConfig(t *testing.T) {
	t.Parallel()
	config := daemonclient.DefaultUpgradeConfig()

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
	t.Parallel()
	config := daemonclient.UpgradeConfig{
		SocketPath:      "/tmp/test-upgrade.sock",
		Timeout:         10 * time.Second,
		GracefulTimeout: 2 * time.Second,
	}

	upgrader := daemonclient.NewDaemonUpgrader(config)
	if upgrader == nil {
		t.Fatal("Expected non-nil upgrader")
	}
}

func TestNewDaemonUpgrader_Defaults(t *testing.T) {
	t.Parallel()
	// Test that empty config gets defaults filled in
	config := daemonclient.UpgradeConfig{}

	upgrader := daemonclient.NewDaemonUpgrader(config)
	if upgrader == nil {
		t.Fatal("Expected non-nil upgrader")
	}

	// The upgrader should have been created with defaults applied internally
}
