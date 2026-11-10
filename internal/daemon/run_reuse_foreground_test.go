package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// TestRun_ReusedProcessHonoursForegroundMode: RUN of an id that is already
// running reused it and replied at once, before reaching the foreground wait,
// so a caller sequencing work behind a build ("start it in the background,
// then wait on it in foreground mode") went on before the build finished. A
// reused process is awaited like a fresh one.
func TestRun_ReusedProcessHonoursForegroundMode(t *testing.T) {
	// No t.Parallel(): starts real processes.
	_, client, tmpDir := newHubProcTestDaemon(t)
	command, args := shellExec("sleep 1; echo built; exit 3")

	if _, err := client.Run(protocol.RunConfig{ID: "build", Path: tmpDir, Raw: true, Command: command, Args: args}); err != nil {
		t.Fatalf("background RUN: %v", err)
	}
	result, err := client.Run(protocol.RunConfig{
		ID: "build", Path: tmpDir, Raw: true, Command: command, Args: args, Mode: "foreground-raw",
	})
	if err != nil {
		t.Fatalf("foreground RUN of the running id: %v", err)
	}
	if reused, _ := result["reused"].(bool); !reused {
		t.Fatalf("second RUN started a new process instead of reusing the live one: %v", result)
	}
	if code := resultInt(t, result, "exit_code"); code != 3 {
		t.Errorf("exit_code = %d, want 3", code)
	}
	if state := result["state"]; state == "running" || state == "starting" {
		t.Errorf("state = %v: the foreground RUN returned before the reused process exited", state)
	}
}
