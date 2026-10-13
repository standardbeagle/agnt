//go:build windows

package platform

import (
	"errors"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestCreateSessionJobObject_Smoke is the minimum compile-time guarantee
// that the Windows job-object API surface is wired correctly. It creates
// a job object, verifies it is a non-zero handle, and closes it. On
// hosts without Job Object support CreateJobObject would fail and we
// would surface that as a test failure — the test is NOT skipped.
func TestCreateSessionJobObject_Smoke(t *testing.T) {
	job, err := CreateSessionJobObject()
	if err != nil {
		t.Fatalf("CreateSessionJobObject: %v", err)
	}
	if job == 0 {
		t.Fatal("CreateSessionJobObject returned zero handle without error")
	}
	if err := KillSessionJobObject(uintptr(job)); err != nil {
		t.Fatalf("KillSessionJobObject: %v", err)
	}
}

// TestKillSessionJobObject_ZeroHandleIsNoop guarantees that the zero
// handle (the value daemon-side code sees when the client didn't
// report a SessionJobHandle) is safe and idempotent. This is the path
// that fires on every non-Windows client connecting to a Windows
// daemon (unlikely in practice but cheap to defend).
func TestKillSessionJobObject_ZeroHandleIsNoop(t *testing.T) {
	if err := KillSessionJobObject(0); err != nil {
		t.Fatalf("KillSessionJobObject(0) should be a no-op, got: %v", err)
	}
}

// TestKillSessionJobObject_RefusesForeignHandle pins the cross-process
// handle bug: a handle value this process did not get from
// CreateSessionJobObject (here a real job object opened behind its back,
// standing in for a number that arrived over IPC) must be neither
// terminated nor closed. The child assigned to it has to survive, and the
// handle has to still be open afterwards.
func TestKillSessionJobObject_RefusesForeignHandle(t *testing.T) {
	foreign, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatalf("CreateJobObject: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = windows.CloseHandle(foreign)
		}
	}()

	// ping, not `timeout`: timeout exits at once when stdin is not a console,
	// which would make "the child survived" unobservable.
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn child (%v); skipping", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { _ = cmd.Process.Kill(); <-exited }()

	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("OpenProcess: %v", err)
	}
	err = windows.AssignProcessToJobObject(foreign, proc)
	_ = windows.CloseHandle(proc)
	if err != nil {
		t.Fatalf("AssignProcessToJobObject: %v", err)
	}

	err = KillSessionJobObject(uintptr(foreign))
	if !errors.Is(err, ErrForeignJobHandle) {
		t.Fatalf("KillSessionJobObject(foreign) = %v, want ErrForeignJobHandle", err)
	}

	select {
	case <-exited:
		t.Fatal("child in a foreign job was terminated")
	case <-time.After(500 * time.Millisecond):
	}
	// Still open: a handle that had been closed would fail here.
	if err := windows.CloseHandle(foreign); err != nil {
		t.Fatalf("foreign handle was closed by KillSessionJobObject: %v", err)
	}
	closed = true
}

// TestReleaseSessionJobObject_ForgetsHandle checks a released handle is no
// longer owned, so a later kill on the same (recyclable) number is refused.
func TestReleaseSessionJobObject_ForgetsHandle(t *testing.T) {
	job, err := CreateSessionJobObject()
	if err != nil {
		t.Fatalf("CreateSessionJobObject: %v", err)
	}
	if err := ReleaseSessionJobObject(job); err != nil {
		t.Fatalf("ReleaseSessionJobObject: %v", err)
	}
	if err := KillSessionJobObject(uintptr(job)); !errors.Is(err, ErrForeignJobHandle) {
		t.Fatalf("KillSessionJobObject after release = %v, want ErrForeignJobHandle", err)
	}
}

// TestSessionJobObject_KillsAssignedChild is the full end-to-end
// containment test. It creates a job object, spawns a long-lived
// child, assigns the child to the job, then calls
// KillSessionJobObject and verifies the child is dead. This test is
// the Windows analogue of the Unix sessionpgid integration test and
// guarantees JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE actually fires.
//
// The test will NOT run on the Linux/WSL2 dev host (build tag
// //go:build windows) but will be exercised by any Windows CI job
// that runs `go test ./internal/platform/...`.
func TestSessionJobObject_KillsAssignedChild(t *testing.T) {
	job, err := CreateSessionJobObject()
	if err != nil {
		t.Fatalf("CreateSessionJobObject: %v", err)
	}

	// Spawn a child that would otherwise run for a while. `timeout
	// /t 30 /nobreak` is a stock Windows command shipped with
	// every recent Windows SKU. If it isn't available the test
	// skips cleanly after closing the job handle so nothing leaks.
	// (ping rather than `timeout`, which exits immediately without a
	// console stdin and so proved nothing about the kill.)
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		_ = KillSessionJobObject(uintptr(job))
		t.Skipf("cannot spawn child (%v); skipping containment assertion", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	if err := AssignPIDToSessionJob(job, cmd.Process.Pid); err != nil {
		t.Fatalf("AssignPIDToSessionJob: %v", err)
	}

	if err := KillSessionJobObject(uintptr(job)); err != nil {
		t.Fatalf("KillSessionJobObject: %v", err)
	}

	// Wait for the child to actually exit. TerminateJobObject is
	// synchronous from the kernel's perspective but the exec.Cmd
	// wait loop still needs to reap the process. 5 seconds is
	// generous — termination is normally sub-100ms.
	errCh := make(chan error, 1)
	go func() { errCh <- cmd.Wait() }()

	select {
	case <-errCh:
		// Any exit is success. TerminateJobObject forces exit code
		// 1, which Go surfaces as an *exec.ExitError — we do not
		// assert on the exit code because Windows job termination
		// can race with process shutdown.
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after KillSessionJobObject")
	}
}
