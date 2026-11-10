package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemon"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// TestCheckDaemonStandalone_ReadsTheSocketsOwnPIDTracker: a daemon on a
// non-default socket records its PID beside that socket (daemon.PIDTrackerPath),
// but doctor's standalone check always read the default tracker. `agnt --socket
// X doctor` then reported the default daemon's PID, or "no PID recorded", for X.
func TestCheckDaemonStandalone_ReadsTheSocketsOwnPIDTracker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // empty default tracker
	t.Setenv("AGNT_SOCKET", "")
	t.Setenv("AGNT_DAEMON_SOCKET", "")
	sock := filepath.Join(t.TempDir(), "d.sock")

	tracker := goprocess.NewFilePIDTracker(goprocess.FilePIDTrackerConfig{
		AppName: "devtool-mcp",
		Path:    daemon.PIDTrackerPath(sock),
	})
	if err := tracker.SetDaemonPID(os.Getpid()); err != nil {
		t.Fatal(err)
	}

	got := checkDaemonStandalone(sock)
	if !strings.Contains(got.Message, fmt.Sprint(os.Getpid())) {
		t.Fatalf("doctor read another tracker for socket %s: %q", sock, got.Message)
	}
}
