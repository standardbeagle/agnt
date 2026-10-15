package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project path containing a space cannot travel as a whitespace-delimited
// protocol argument. The path-taking verbs must still reach the daemon with
// the whole path (carried in the JSON payload).
func TestPathTakingVerbs_AcceptProjectPathWithSpace(t *testing.T) {
	_, c, tmpDir := newBootedDaemonWithClient(t)
	projectPath := filepath.Join(tmpDir, "my project", "src")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SessionRegister("space-session", "/tmp/overlay", projectPath, "test", nil); err != nil {
		t.Fatalf("SessionRegister: %v", err)
	}

	found, err := c.SessionFind(projectPath)
	if err != nil {
		t.Fatalf("SessionFind(%q): %v", projectPath, err)
	}
	if found["code"] != "space-session" {
		t.Fatalf("SessionFind found %v, want space-session", found["code"])
	}

	attached, err := c.SessionAttach(projectPath)
	if err != nil {
		t.Fatalf("SessionAttach(%q): %v", projectPath, err)
	}
	if attached["session_code"] != "space-session" {
		t.Fatalf("SessionAttach attached %v, want space-session", attached["session_code"])
	}

	if _, err := c.AutostartReconcile(projectPath); err != nil {
		t.Fatalf("AutostartReconcile(%q): %v", projectPath, err)
	}

	// No autostart is pending, so the daemon answers with that; the point is
	// that it was the daemon answering about this project, not the client
	// refusing the path.
	_, err = c.AutostartClearPorts(projectPath)
	if err == nil || !strings.Contains(err.Error(), "no pending autostart") {
		t.Fatalf("AutostartClearPorts(%q) = %v, want the daemon's no-pending-autostart answer", projectPath, err)
	}
}
