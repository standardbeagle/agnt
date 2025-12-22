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
	// jobRegistry maps process IDs to their job object handles.
	// This allows us to terminate entire process trees.
	jobRegistry sync.Map // map[int]windows.Handle

	// kernel32 for GenerateConsoleCtrlEvent
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

const (
	// CTRL_C_EVENT is sent to processes attached to a console
	CTRL_C_EVENT = 0
	// CTRL_BREAK_EVENT is sent to processes in a process group
	CTRL_BREAK_EVENT = 1
)

// setProcAttr sets platform-specific process attributes for Windows.
// Creates process in a new process group for clean signal handling.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// CREATE_NEW_PROCESS_GROUP allows GenerateConsoleCtrlEvent to target this process
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// createJobObject creates a Windows Job Object configured to kill all
// child processes when the job is closed or terminated.
func createJobObject() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}

	// Configure job to kill all processes when closed
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

// assignProcessToJob assigns a process to a job object.
// This must be called after the process starts but before it creates children.
func assignProcessToJob(job windows.Handle, pid int, processHandle uintptr) error {
	return windows.AssignProcessToJobObject(job, windows.Handle(processHandle))
}

// getProcessHandle extracts the Windows process handle from exec.Cmd.
// This uses unsafe to access the unexported handle field.
func getProcessHandle(cmd *exec.Cmd) (uintptr, error) {
	if cmd.Process == nil {
		return 0, errors.New("process not started")
	}

	// The os.Process struct has an unexported handle field
	// We access it through the Pid which gives us enough to open the process
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
// Call this after cmd.Start() succeeds.
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
	// Close the handle we opened - the job object will keep the process alive
	defer windows.CloseHandle(windows.Handle(handle))

	if err := assignProcessToJob(job, cmd.Process.Pid, handle); err != nil {
		windows.CloseHandle(job)
		return err
	}

	// Store job handle for later termination
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

// signalProcessGroup sends a termination signal to the process group.
// On Windows, this uses the job object if available, otherwise direct termination.
func (pm *ProcessManager) signalProcessGroup(pid int, sig syscall.Signal) error {
	// First try to terminate via job object (terminates all children)
	if val, ok := jobRegistry.Load(pid); ok {
		job := val.(windows.Handle)
		// TerminateJobObject kills all processes in the job
		err := windows.TerminateJobObject(job, 1)
		if err == nil {
			return nil
		}
		// Fall through to direct termination if job termination fails
	}

	// Fall back to direct process termination
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// signalTerm attempts graceful termination on Windows.
// Uses GenerateConsoleCtrlEvent to send CTRL_BREAK_EVENT to the process group.
func signalTerm(pid int) error {
	// Try sending CTRL_BREAK_EVENT to the process group
	// This is the Windows equivalent of SIGTERM
	ret, _, err := procGenerateConsoleCtrlEvent.Call(
		uintptr(CTRL_BREAK_EVENT),
		uintptr(pid), // Process group ID (same as PID when CREATE_NEW_PROCESS_GROUP is used)
	)
	if ret == 0 {
		// GenerateConsoleCtrlEvent failed - this can happen if the process
		// doesn't have a console. Fall back to nothing (let caller retry with Kill)
		return err
	}
	return nil
}

// signalKill forcefully terminates the process on Windows.
// Uses job object if available to ensure all children are killed.
func signalKill(pid int) error {
	// First try job object termination (kills all children)
	if val, ok := jobRegistry.Load(pid); ok {
		job := val.(windows.Handle)
		if err := windows.TerminateJobObject(job, 1); err == nil {
			return nil
		}
	}

	// Fall back to direct process kill
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

	// STILL_ACTIVE (259) means the process is still running
	return exitCode == 259
}

// isNoSuchProcess returns true if the error indicates the process doesn't exist.
func isNoSuchProcess(err error) bool {
	if err == nil {
		return false
	}
	// Check for common Windows "process not found" errors
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true
	}
	if errors.Is(err, syscall.EINVAL) {
		return true
	}
	return os.IsNotExist(err) || err == os.ErrProcessDone
}

// getProcessGroupID returns the process group ID for a given PID.
// On Windows, there's no direct equivalent of Unix process groups.
// We return the PID itself since we use Job Objects for process tree management.
func getProcessGroupID(pid int) int {
	return pid
}
