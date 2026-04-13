//go:build windows

package platform

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CreateSessionJobObject creates a Windows Job Object configured with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. All processes subsequently assigned
// to this job (via AssignProcessToSessionJob) — and their descendants —
// will be terminated when the returned handle is closed, either by
// CloseHandle on process exit or by an explicit KillSessionJobObject
// call.
//
// This is the Windows equivalent of the POSIX session pgid path used on
// Unix (see sessionpgid_unix.go): both mechanisms exist so the daemon
// can reap every process the PTY child session leader spawned,
// including grandchild dev servers the coding agent backgrounded via a
// non-interactive shell.
//
// The returned handle is an opaque kernel handle. Callers that want to
// ship the handle across processes (e.g. from `agnt run` to the daemon
// via the SESSION REGISTER verb) MUST convert it with
// uint64(handle) and pass it as SessionJobHandle in
// SessionRegisterConfig. Handle values are per-process on Windows —
// shipping it to a different process is a best-effort hint, not a
// guarantee. The authoritative kill still happens from within the
// original process via the JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE flag when
// the handle is finally closed.
func CreateSessionJobObject() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}

	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		// Best-effort cleanup: if SetInformationJobObject failed we do
		// NOT want to leave a dangling job object that will nonetheless
		// kill processes on close — close it here and surface the error.
		windows.CloseHandle(job)
		return 0, fmt.Errorf("SetInformationJobObject: %w", err)
	}

	return job, nil
}

// AssignProcessToSessionJob assigns the given process handle to a job
// object created by CreateSessionJobObject. The process handle must
// have been opened with at least PROCESS_SET_QUOTA and
// PROCESS_TERMINATE access; the raw PID helper AssignPIDToSessionJob
// opens a handle with those rights automatically.
//
// All descendants spawned after assignment inherit the job unless they
// explicitly break out (which requires JOB_OBJECT_LIMIT_BREAKAWAY_OK
// on the parent job, which we do NOT set — break-out is denied).
func AssignProcessToSessionJob(job windows.Handle, process windows.Handle) error {
	return windows.AssignProcessToJobObject(job, process)
}

// AssignPIDToSessionJob is a convenience helper around
// AssignProcessToSessionJob for callers that have a raw PID (from
// cmd.Process.Pid) rather than a process handle. It opens the process
// with PROCESS_SET_QUOTA|PROCESS_TERMINATE, assigns it to the job, and
// closes the process handle before returning.
func AssignPIDToSessionJob(job windows.Handle, pid int) error {
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(proc)
	return windows.AssignProcessToJobObject(job, proc)
}

// KillSessionJobObject terminates every process in the job with exit
// code 1 via TerminateJobObject, then closes the handle. Idempotent:
// passing 0 is a no-op; a handle that is already closed returns an
// error from TerminateJobObject which we still surface to the caller
// so logs record it, but CloseHandle is still attempted.
//
// The handle argument is typed as uintptr so cross-platform callers
// (e.g. internal/daemon) can invoke this without importing
// golang.org/x/sys/windows. The stub in sessionjob_other.go has the
// same signature and does nothing on non-Windows.
func KillSessionJobObject(handle uintptr) error {
	if handle == 0 {
		return nil
	}
	h := windows.Handle(handle)

	// TerminateJobObject with exit code 1 kills every assigned process
	// (and their descendants) immediately. This is the equivalent of
	// SIGKILL to the pgid on Unix and exists so cleanup is synchronous
	// — we do not want to rely on handle-close semantics when the
	// daemon is explicitly tearing the session down.
	termErr := windows.TerminateJobObject(h, 1)

	// Always close the handle even if TerminateJobObject failed. A
	// dangling job handle would leak kernel memory; closing it also
	// triggers JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE as a belt-and-braces
	// kill in case TerminateJobObject races another closer.
	closeErr := windows.CloseHandle(h)

	if termErr != nil {
		return fmt.Errorf("TerminateJobObject: %w", termErr)
	}
	if closeErr != nil {
		return fmt.Errorf("CloseHandle: %w", closeErr)
	}
	return nil
}
