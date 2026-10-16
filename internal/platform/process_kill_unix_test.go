//go:build linux || darwin

package platform

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// unixRunning reports whether the process id names is still running (a
// zombie counts as gone).
func unixRunning(id ProcessIdentity) bool {
	cur, ok := ProcessBirthID(id.PID)
	return ok && cur == id.Birth && !pidIsZombie(id.PID)
}

func unixWaitGone(id ProcessIdentity) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !unixRunning(id) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// unixIdentify identifies a process the test started and registers an
// identity-checked cleanup, which cannot reach a later holder of the PID.
func unixIdentify(t *testing.T, pid int) ProcessIdentity {
	t.Helper()
	id, ok := IdentifyProcess(pid)
	if !ok {
		t.Fatalf("cannot identify %d", pid)
	}
	t.Cleanup(func() { _ = KillProcess(id, 0) })
	return id
}

// The Unix twin of TestKillProcess_WindowsRefusesStaleIdentity: an identity
// taken earlier is honoured, a stale one is refused.
//
// No t.Parallel(): starts real OS processes.
func TestKillProcess_UnixRefusesStaleIdentity(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()
	id := unixIdentify(t, cmd.Process.Pid)

	stale := ProcessIdentity{PID: id.PID, Birth: "1"}
	if err := KillProcess(stale, 0); !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("KillProcess(stale) = %v, want ErrProcessIdentityChanged", err)
	}
	if !unixRunning(id) {
		t.Fatal("a stale identity killed the process holding the PID")
	}
	if err := KillProcess(id, 1); err != nil {
		t.Fatalf("KillProcess: %v", err)
	}
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatal("process survived a kill by its own identity")
	}
}

// A tree kill reaches a child that is not in the root's process group: the
// shell here is not a group leader, so only the parent-link walk finds the
// sleep.
//
// No t.Parallel(): starts real OS processes.
func TestKillProcessTree_UnixKillsDescendants(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	line, err := bufio.NewReader(out).ReadString('\n')
	go func() { _ = cmd.Wait(); close(reaped) }()
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	root := unixIdentify(t, cmd.Process.Pid)
	child := unixIdentify(t, childPID)

	if err := KillProcessTree(ProcessIdentity{PID: root.PID, Birth: "1"}, 0); !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("KillProcessTree(stale root) = %v, want ErrProcessIdentityChanged", err)
	}
	if !unixRunning(root) || !unixRunning(child) {
		t.Fatal("a stale root identity killed part of the tree")
	}

	if err := KillProcessTree(root, 1); err != nil {
		t.Fatalf("KillProcessTree: %v", err)
	}
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatal("root survived the tree kill")
	}
	if !unixWaitGone(child) {
		t.Fatalf("child %d survived the tree kill", childPID)
	}
}
