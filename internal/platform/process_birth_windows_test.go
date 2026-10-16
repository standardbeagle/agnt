//go:build windows

package platform

import (
	"io"
	"os"
	"os/exec"
	"testing"
)

// TestProcessBirthIDHelperProcess is the child the Windows birth-id test
// spawns: it blocks until its stdin closes. It does nothing in a normal run.
func TestProcessBirthIDHelperProcess(t *testing.T) {
	if os.Getenv("AGNT_BIRTH_ID_HELPER") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// Windows reuses PIDs quickly, so a recorded PID (the shim watcher's) is only
// safe to trust alongside a birth identity. Without one, a recycled PID made
// an unrelated process pass for the watcher and the watcher was never
// respawned.
//
// No t.Parallel(): starts a real OS process.
func TestProcessBirthID_WindowsIdentifiesOneLiveProcess(t *testing.T) {
	self, ok := ProcessBirthID(os.Getpid())
	if !ok || self == "" {
		t.Fatal("no birth id for the test process itself")
	}
	if again, ok := ProcessBirthID(os.Getpid()); !ok || again != self {
		t.Fatalf("birth id not stable: %q then %q", self, again)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessBirthIDHelperProcess$")
	cmd.Env = append(os.Environ(), "AGNT_BIRTH_ID_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	child, ok := ProcessBirthID(pid)
	if !ok || child == "" {
		_ = stdin.Close()
		_ = cmd.Wait()
		t.Fatalf("no birth id for live child %d", pid)
	}
	if child == self {
		t.Errorf("child and parent share birth id %q", child)
	}

	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	// The PID may already belong to a new process; it must not carry the
	// exited child's identity.
	if after, ok := ProcessBirthID(pid); ok && after == child {
		t.Errorf("exited child %d still reports birth id %q", pid, child)
	}
}
