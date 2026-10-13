//go:build windows

package platform

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrForeignJobHandle is returned by KillSessionJobObject for a handle value
// this process did not get from CreateSessionJobObject.
var ErrForeignJobHandle = errors.New("job handle was not created by this process")

// ownedSessionJobs records the job handles this process created and still
// holds. A Windows handle value is an index into ONE process's handle table:
// the same number in another process names an unrelated object (a pipe, a
// file, another job). KillSessionJobObject therefore acts only on values found
// here, so a number that arrived over IPC can never terminate or close
// something this process owns under the same index.
var ownedSessionJobs sync.Map // windows.Handle -> struct{}

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
// The returned handle is valid in THIS process only. Never send its value
// to another process: there the same number names an unrelated object.
// The kill happens here, either through KillSessionJobObject or through
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE when the handle is released
// (ReleaseSessionJobObject, or process exit).
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

	ownedSessionJobs.Store(job, struct{}{})
	return job, nil
}

// ReleaseSessionJobObject closes a job handle from CreateSessionJobObject
// without an explicit terminate; JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE still
// ends the assigned processes once the last handle is gone. A handle this
// process did not create (or already released) is left alone.
func ReleaseSessionJobObject(job windows.Handle) error {
	if _, owned := ownedSessionJobs.LoadAndDelete(job); !owned {
		return nil
	}
	return windows.CloseHandle(job)
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
// code 1 via TerminateJobObject, then closes the handle. Passing 0 is a
// no-op. A second call for the same handle returns ErrForeignJobHandle,
// as does any value this process did not get from CreateSessionJobObject:
// such a value is never terminated and never closed (see ownedSessionJobs).
//
// The handle argument is typed as uintptr so cross-platform callers can
// invoke this without importing golang.org/x/sys/windows. The stub in
// sessionjob_other.go has the same signature and does nothing on
// non-Windows.
func KillSessionJobObject(handle uintptr) error {
	if handle == 0 {
		return nil
	}
	h := windows.Handle(handle)
	if _, owned := ownedSessionJobs.LoadAndDelete(h); !owned {
		return fmt.Errorf("job 0x%x: %w", handle, ErrForeignJobHandle)
	}

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
