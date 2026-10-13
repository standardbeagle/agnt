//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Process enumeration for Linux (and WSL, which is GOOS=linux). macOS has no
// /proc and enumerates through sysctl in process_scan_darwin.go; other Unixes
// get the stub in process_scan_other.go.

// Scan returns all running processes by reading /proc on Linux.
func Scan() ([]ProcInfo, error) {
	return scanProc()
}

// scanProc reads /proc entries to enumerate processes.
func scanProc() ([]ProcInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("reading /proc: %w", err)
	}

	var procs []ProcInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		info := readProcInfo(pid)
		if info == nil {
			continue
		}
		procs = append(procs, *info)
	}
	return procs, nil
}

// readProcInfo reads /proc/<pid> fields for a single process.
// Returns nil if the process has disappeared or cannot be read.
func readProcInfo(pid int) *ProcInfo {
	cmdlineData, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	if len(cmdlineData) == 0 {
		return nil // kernel thread
	}

	// cmdline is null-delimited
	parts := strings.SplitN(string(cmdlineData), "\x00", 2)
	if len(parts) == 0 || parts[0] == "" {
		return nil
	}

	info := &ProcInfo{
		PID:     pid,
		Command: filepath.Base(parts[0]),
		Cmdline: strings.ReplaceAll(string(cmdlineData), "\x00", " "),
	}

	// Read PPID from /proc/<pid>/stat. After comm: state(0) ppid(1) ...
	if statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		fields := parseStatFieldsAfterComm(statData)
		if len(fields) >= 2 {
			if ppid, err := strconv.Atoi(fields[1]); err == nil {
				info.PPID = ppid
			}
		}
	}

	// Read cwd symlink
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err == nil {
		info.Cwd = cwd
	}

	return info
}
