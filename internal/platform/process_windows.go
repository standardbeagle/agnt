//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Scan returns all running processes using tasklist on Windows.
func Scan() ([]ProcInfo, error) {
	return scanWindows()
}

// scanWindows uses tasklist to enumerate processes on Windows.
func scanWindows() ([]ProcInfo, error) {
	cmd := exec.Command("tasklist", "/fo", "CSV", "/nh")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tasklist: %w", err)
	}

	var procs []ProcInfo
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// CSV format: "Image Name","PID","Session Name","Session#","Mem Usage"
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		name := strings.Trim(fields[0], `"`)
		pidStr := strings.Trim(fields[1], `"`)
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		procs = append(procs, ProcInfo{
			PID:     pid,
			Command: name,
			Cmdline: name,
		})
	}
	return procs, nil
}

// killPID terminates a process on Windows.
func killPID(pid int, _ int) error {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	defer syscall.CloseHandle(handle)

	_, _, err = syscall.NewLazyDLL("kernel32.dll").NewProc("TerminateProcess").Call(
		uintptr(handle), 1,
	)
	if err != nil && err != syscall.Errno(0) {
		return fmt.Errorf("terminate: %w", err)
	}
	return nil
}

// ScanWindows is a no-op on native Windows (already covered by Scan).
func ScanWindows() ([]ProcInfo, error) {
	return nil, nil
}
