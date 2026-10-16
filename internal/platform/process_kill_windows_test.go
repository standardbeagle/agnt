//go:build windows

package platform

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestKillHelperProcess is the child the Windows kill tests spawn. It does
// nothing in a normal run. "sleep" waits two minutes and exits, so a helper
// a failed test leaves behind does not live long. "tree" starts a "sleep"
// child, prints its PID, and then sleeps the same way.
func TestKillHelperProcess(t *testing.T) {
	switch os.Getenv("AGNT_KILL_HELPER") {
	case "sleep":
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	case "tree":
		child := exec.Command(os.Args[0], "-test.run=^TestKillHelperProcess$")
		child.Env = append(os.Environ(), "AGNT_KILL_HELPER=sleep")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString(strconv.Itoa(child.Process.Pid) + "\n")
		_ = child.Process.Release()
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
}

// startKillHelper starts a helper in mode and, for "tree", returns its child's
// PID as well.
func startKillHelper(t *testing.T, mode string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestKillHelperProcess$")
	cmd.Env = append(os.Environ(), "AGNT_KILL_HELPER="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if mode != "tree" {
		return cmd, 0
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("helper did not report its child: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("helper child pid %q: %v", line, err)
	}
	return cmd, child
}

// waitGone reports whether the process id names has stopped running within a
// generous ceiling.
func waitGone(id ProcessIdentity) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !running(id) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// running reports whether the process id names is still running.
func running(id ProcessIdentity) bool {
	cur, ok := ProcessBirthID(id.PID)
	return ok && cur == id.Birth
}

// mustIdentify identifies a helper the test started and registers its
// cleanup. The cleanup kills by identity, so it can never reach a process
// that inherited the PID after the helper died.
func mustIdentify(t *testing.T, pid int) ProcessIdentity {
	t.Helper()
	id, ok := IdentifyProcess(pid)
	if !ok {
		t.Fatalf("cannot identify helper %d", pid)
	}
	t.Cleanup(func() { _ = KillProcess(id, 0) })
	return id
}

// staleIdentity is id with a birth identity one tick earlier: the identity
// an earlier holder of the same PID would have had.
func staleIdentity(t *testing.T, id ProcessIdentity) ProcessIdentity {
	t.Helper()
	ticks, err := strconv.ParseUint(id.Birth, 10, 64)
	if err != nil {
		t.Fatalf("birth %q is not a tick count: %v", id.Birth, err)
	}
	return ProcessIdentity{PID: id.PID, Birth: strconv.FormatUint(ticks-1, 10)}
}

// Windows kills used to go by PID alone: a PID taken from an earlier scan was
// terminated whatever process held it by then. A kill now opens the process,
// compares its creation time and terminates through the same handle.
//
// No t.Parallel(): starts real OS processes.
func TestKillProcess_WindowsRefusesStaleIdentity(t *testing.T) {
	helper, _ := startKillHelper(t, "sleep")
	t.Cleanup(func() { _ = helper.Wait() })
	id := mustIdentify(t, helper.Process.Pid)

	err := KillProcess(staleIdentity(t, id), 0)
	if !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("KillProcess(stale) = %v, want ErrProcessIdentityChanged", err)
	}
	if !running(id) {
		t.Fatal("a stale identity killed the process now holding the PID")
	}

	if err := KillProcess(id, 0); err != nil {
		t.Fatalf("KillProcess(current): %v", err)
	}
	if !waitGone(id) {
		t.Fatal("process survived a kill by its own identity")
	}
	// Gone is success, not an error.
	if err := KillProcess(id, 0); err != nil {
		t.Fatalf("KillProcess on an exited process: %v", err)
	}
}

// A tree kill takes the descendants too, and a stale root identity refuses
// the whole tree.
//
// No t.Parallel(): starts real OS processes.
func TestKillProcessTree_WindowsKillsDescendantsByIdentity(t *testing.T) {
	parent, childPID := startKillHelper(t, "tree")
	t.Cleanup(func() { _ = parent.Wait() })
	root := mustIdentify(t, parent.Process.Pid)
	child := mustIdentify(t, childPID)

	err := KillProcessTree(staleIdentity(t, root), 0)
	if !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("KillProcessTree(stale root) = %v, want ErrProcessIdentityChanged", err)
	}
	if !running(root) || !running(child) {
		t.Fatal("a stale root identity killed part of the tree")
	}

	if err := KillProcessTree(root, 0); err != nil {
		t.Fatalf("KillProcessTree: %v", err)
	}
	if !waitGone(root) {
		t.Fatal("root survived the tree kill")
	}
	if !waitGone(child) {
		t.Fatalf("child %d survived the tree kill", childPID)
	}
}

// A root that has exited but whose PID is still held (here by the
// exec.Cmd's handle) still leads its tree: its children are found and killed.
// This is the hook-timeout case where the shell exits before its children.
//
// No t.Parallel(): starts real OS processes.
func TestKillProcessTree_WindowsExitedRootStillReachesChildren(t *testing.T) {
	parent, childPID := startKillHelper(t, "tree")
	root := mustIdentify(t, parent.Process.Pid)
	child := mustIdentify(t, childPID)
	if err := KillProcess(root, 0); err != nil { // root only
		t.Fatalf("KillProcess(root): %v", err)
	}
	if !running(child) {
		t.Fatal("killing the root alone took the child: the test proves nothing")
	}
	if _, ok := IdentifyProcess(root.PID); !ok {
		t.Fatal("an exited root whose handle is held must still be identifiable")
	}

	if err := KillProcessTree(root, 0); err != nil {
		t.Fatalf("KillProcessTree(exited root): %v", err)
	}
	if !waitGone(child) {
		t.Fatalf("child %d of an exited root survived the tree kill", childPID)
	}
	_ = parent.Wait()
}

// Native KillWindowsPID used to terminate only the named process, while the
// WSL path runs taskkill /T and takes the tree. A port holder's children
// (a dev server's workers) survived the kill on native Windows.
//
// No t.Parallel(): starts real OS processes.
func TestKillWindowsPID_NativeKillsTree(t *testing.T) {
	parent, childPID := startKillHelper(t, "tree")
	t.Cleanup(func() { _ = parent.Wait() })
	root := mustIdentify(t, parent.Process.Pid)
	child := mustIdentify(t, childPID)

	if err := KillWindowsPID(parent.Process.Pid); err != nil {
		t.Fatalf("KillWindowsPID: %v", err)
	}
	if !waitGone(root) {
		t.Fatal("KillWindowsPID left its target running")
	}
	if !waitGone(child) {
		t.Fatalf("child %d of the killed process survived", childPID)
	}
}
