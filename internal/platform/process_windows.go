//go:build windows

package platform

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Scan returns all running processes on Windows.
func Scan() ([]ProcInfo, error) {
	return scanWindows()
}

// scanWindows enumerates processes from a Toolhelp snapshot: one kernel call,
// no child process.
//
// It used to run `tasklist /fo CSV /nh` and parse the output. That had two
// defects. tasklist takes from half a second to several seconds and ran with
// no deadline and no way to cancel, so the daemon's autostart worker (which
// scans on every autostart, collectManagedPIDs) could still be parked in it
// after Daemon.Stop had returned. And tasklist reports no parent PID, so
// every ProcInfo.PPID was 0: the parent-chain walks that protect a managed
// process's children from port cleanup found no children on Windows.
//
// Command and Cmdline are both the image name ("node.exe"), as before; the
// full command line and the working directory need a per-process handle and
// are not read here.
func scanWindows() ([]ProcInfo, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	var procs []ProcInfo
	for {
		// PID 0 is the System Idle Process, a placeholder with no image.
		if entry.ProcessID != 0 {
			name := windows.UTF16ToString(entry.ExeFile[:])
			procs = append(procs, ProcInfo{
				PID:     int(entry.ProcessID),
				PPID:    int(entry.ParentProcessID),
				Command: name,
				Cmdline: name,
			})
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return procs, nil
			}
			return nil, fmt.Errorf("process snapshot: %w", err)
		}
	}
}

// killPID terminates a process on Windows.
func killPID(pid int, _ int) error {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	defer syscall.CloseHandle(handle)

	// syscall.TerminateProcess reports failure from the call's own return
	// value. The previous LazyProc.Call form judged it by GetLastError, which
	// a successful call does not reset, so a kill that worked could be
	// reported as failed.
	if err := syscall.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("terminate: %w", err)
	}
	return nil
}

// ScanWindows is a no-op on native Windows (already covered by Scan).
func ScanWindows() ([]ProcInfo, error) {
	return nil, nil
}

// IsWSL returns false on native Windows. WSL processes run as Linux binaries
// and use the unix build of this package.
func IsWSL() bool {
	return false
}

// ShouldUseWindowsShell always returns true on native Windows. Path is
// ignored — the platform shell is cmd.exe regardless of whether the
// script lives on the C: drive, a UNC share, or anywhere else. The
// signature matches the unix variant so callers can stay
// platform-agnostic.
func ShouldUseWindowsShell(_ string) bool {
	return true
}

// ShouldUseWindowsCommand always selects cmd.exe on native Windows.
func ShouldUseWindowsCommand(_ string) bool { return true }
