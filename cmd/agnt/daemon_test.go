package main

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemon"
)

// TestRunDaemonSocketPath_PrintsDefaultSocketPathOneLine pins the contract
// consumed by 'agnt ssh': exec'd over SSH, its stdout must be exactly the
// socket path, one line, no decoration, so the caller can use it verbatim
// as the remote endpoint for a direct-streamlocal channel.
func TestRunDaemonSocketPath_PrintsDefaultSocketPathOneLine(t *testing.T) {
	want := daemon.DefaultSocketPath()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	runDaemonSocketPath(daemonSocketPathCmd, nil)

	w.Close()
	os.Stdout = origStdout

	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line of output, got %d: %v", len(lines), lines)
	}
	if strings.TrimSpace(lines[0]) != want {
		t.Errorf("output = %q, want %q", lines[0], want)
	}
}
