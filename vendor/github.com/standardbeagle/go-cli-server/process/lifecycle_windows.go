//go:build windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	jobRegistry                  sync.Map
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

const (
	CTRL_C_EVENT     = 0
	CTRL_BREAK_EVENT = 1
)

// setProcAttr sets platform-specific process attributes for Windows.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func createJobObject() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}

	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}

	return job, nil
}

func assignProcessToJob(job windows.Handle, pid int, processHandle uintptr) error {
	return windows.AssignProcessToJobObject(job, windows.Handle(processHandle))
}

func getProcessHandle(cmd *exec.Cmd) (uintptr, error) {
	if cmd.Process == nil {
		return 0, errors.New("process not started")
	}

	handle, err := windows.OpenProcess(
		windows.PROCESS_ALL_ACCESS,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return 0, err
	}

	return uintptr(handle), nil
}

// SetupJobObject creates and assigns a job object for the given process.
func SetupJobObject(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return errors.New("process not started")
	}

	job, err := createJobObject()
	if err != nil {
		return err
	}

	handle, err := getProcessHandle(cmd)
	if err != nil {
		windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(windows.Handle(handle))

	if err := assignProcessToJob(job, cmd.Process.Pid, handle); err != nil {
		windows.CloseHandle(job)
		return err
	}

	jobRegistry.Store(cmd.Process.Pid, job)

	return nil
}

// CleanupJobObject removes and closes the job object for a process.
func CleanupJobObject(pid int) {
	if val, ok := jobRegistry.LoadAndDelete(pid); ok {
		job := val.(windows.Handle)
		windows.CloseHandle(job)
	}
}

// cleanupProcessGroup terminates any surviving members of a process group.
// On Windows, this uses the Job Object if available.
func cleanupProcessGroup(pgid int) {
	if val, ok := jobRegistry.Load(pgid); ok {
		job := val.(windows.Handle)
		_ = windows.TerminateJobObject(job, 1)
	}
}

// signalProcessGroup sends a termination signal to the process group, honoring
// the requested signal so Windows gets the same graceful-then-forceful contract
// as Unix. Previously it ignored sig and always called TerminateJobObject, so a
// SIGTERM (the graceful phase of StopProcess) was an instant hard kill and
// GracefulTimeout meant nothing.
func (pm *ProcessManager) signalProcessGroup(pid int, sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		// Force: terminate the whole job-object tree (children cascade), else kill
		// the bare process.
		if val, ok := jobRegistry.Load(pid); ok {
			job := val.(windows.Handle)
			if err := windows.TerminateJobObject(job, 1); err == nil {
				return nil
			}
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return proc.Kill()
	}

	// Graceful (SIGTERM): deliver CTRL_BREAK to the child's process group so it can
	// run its shutdown handler. Children are started with CREATE_NEW_PROCESS_GROUP,
	// so their group id equals the PID. If no console is attached (a detached
	// daemon) this fails — return the error WITHOUT hard-killing, so the caller's
	// GracefulTimeout elapses and forceKill (SIGKILL) does the escalation.
	return signalTerm(pid)
}

// signalTerm attempts graceful termination on Windows.
func signalTerm(pid int) error {
	ret, _, err := procGenerateConsoleCtrlEvent.Call(
		uintptr(CTRL_BREAK_EVENT),
		uintptr(pid),
	)
	if ret == 0 {
		return err
	}
	return nil
}

// signalKill forcefully terminates the process on Windows.
func signalKill(pid int) error {
	if val, ok := jobRegistry.Load(pid); ok {
		job := val.(windows.Handle)
		if err := windows.TerminateJobObject(job, 1); err == nil {
			return nil
		}
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// isProcessAlive checks if a process is still running on Windows.
func isProcessAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		return false
	}

	return exitCode == 259 // STILL_ACTIVE
}

// isNoSuchProcess returns true if the error indicates the process doesn't exist.
func isNoSuchProcess(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if errors.Is(err, syscall.EINVAL) {
		return true
	}
	return os.IsNotExist(err) || err == os.ErrProcessDone
}

// getProcessGroupID returns the process group ID for a given PID.
func getProcessGroupID(pid int) int {
	return pid
}

// findAllDescendants returns descendant PIDs. On Windows, Job Objects own
// the process tree and cascade-kill automatically via TerminateJobObject,
// so descendant enumeration is unnecessary — returns nil. Callers that
// snapshot descendants for the SIGKILL escalation path still work: the
// Job Object handles the escalation implicitly.
func findAllDescendants(pid int) []int {
	return nil
}

// cleanupProcessTree forcefully terminates a process and its job-object tree.
// On Windows, signalProcessGroup already terminates the full job object, so this
// is the SIGKILL escalation fallback for stragglers. Uses the job registry when
// available; otherwise falls back to a direct process kill.
func cleanupProcessTree(pid int) {
	if val, ok := jobRegistry.Load(pid); ok {
		job := val.(windows.Handle)
		if err := windows.TerminateJobObject(job, 1); err == nil {
			return
		}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
}
