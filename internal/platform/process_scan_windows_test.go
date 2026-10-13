//go:build windows

package platform

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Scan must report this process, with its real parent. The tasklist-based scan
// it replaced reported PPID 0 for everything, which made every parent-chain
// walk on Windows (the one that protects a managed process's children from
// port cleanup) find nothing.
func TestScan_FindsThisProcessWithItsParent(t *testing.T) {
	procs, err := Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	self := os.Getpid()
	for _, p := range procs {
		if p.PID != self {
			continue
		}
		if p.PPID != os.Getppid() {
			t.Errorf("PPID = %d, want %d", p.PPID, os.Getppid())
		}
		if !strings.HasSuffix(strings.ToLower(p.Command), ".exe") {
			t.Errorf("Command = %q, want the image name", p.Command)
		}
		return
	}
	t.Fatalf("Scan returned %d processes, none of them this one (pid %d)", len(procs), self)
}

// A child this test starts shows up with this process as its parent.
func TestScan_ReportsChildParentLink(t *testing.T) {
	// No t.Parallel(): starts a real process.
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn child: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	procs, err := Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, p := range procs {
		if p.PID == cmd.Process.Pid {
			if p.PPID != os.Getpid() {
				t.Fatalf("child PPID = %d, want %d", p.PPID, os.Getpid())
			}
			return
		}
	}
	t.Fatalf("child pid %d not in Scan output", cmd.Process.Pid)
}

// The scan is a kernel snapshot, not a child process. This is a liveness
// ceiling, not a latency claim: `tasklist` could not finish 20 scans in it.
func TestScan_DoesNotShellOut(t *testing.T) {
	start := time.Now()
	for i := 0; i < 20; i++ {
		if _, err := Scan(); err != nil {
			t.Fatalf("Scan: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("20 scans took %v; a snapshot takes milliseconds, a tasklist exec does not", elapsed)
	}
}
