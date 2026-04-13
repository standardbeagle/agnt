//go:build windows

package platform

import (
	"os/exec"
	"testing"
	"time"
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
	cmd := exec.Command("cmd.exe", "/c", "timeout /t 30 /nobreak > NUL")
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
