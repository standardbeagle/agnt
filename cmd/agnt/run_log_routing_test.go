package main

import (
	"os"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/debug"
)

// TestRouteRunLogsToFile_WithoutDebug: debug.Error/Warn/Info print whether or
// not debug is enabled, so `agnt run` must move the logger off the terminal
// even when debug is off.
func TestRouteRunLogsToFile_WithoutDebug(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("LocalAppData", cache)   // os.UserCacheDir on Windows
	t.Setenv("XDG_CACHE_HOME", cache) // on Linux
	t.Setenv("HOME", cache)           // on macOS
	wasEnabled := debug.IsEnabled()
	debug.Disable()
	t.Cleanup(func() {
		_ = debug.SetLogFile("")
		if wasEnabled {
			debug.Enable()
		}
	})
	if path := debug.GetLogFilePath(); path != "" {
		t.Fatalf("precondition: log file already set to %s", path)
	}

	if err := routeRunLogsToFile(); err != nil {
		t.Fatalf("routeRunLogsToFile: %v", err)
	}
	path := debug.GetLogFilePath()
	if path == "" {
		t.Fatal("with debug off, the logger still writes to stderr (the agent's terminal)")
	}
	debug.Warn("test", "run-log-routing-marker")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), "run-log-routing-marker") {
		t.Fatalf("warning did not reach %s: %q", path, data)
	}
}
