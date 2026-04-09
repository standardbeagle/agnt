//go:build !windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	wslChecked bool
	wslResult  bool
	wslMu      sync.Once
)

// IsWSL returns true when running under Windows Subsystem for Linux.
func IsWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	wslMu.Do(func() {
		data, err := os.ReadFile("/proc/version")
		if err != nil {
			wslResult = false
			return
		}
		v := strings.ToLower(string(data))
		wslResult = strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
		wslChecked = true
	})
	return wslResult
}

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

	// Read cwd symlink
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err == nil {
		info.Cwd = cwd
	}

	return info
}

// killPID sends SIGTERM, waits, then SIGKILL.
func killPID(pid int, gracefulTimeout int) error {
	// Send SIGTERM to process group
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(pid, syscall.SIGTERM)

	deadline := time.Now().Add(time.Duration(gracefulTimeout) * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return nil // process gone
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Escalate to SIGKILL
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return nil
}

// ScanWindows returns Windows-side processes when running under WSL.
// On non-WSL Unix this returns nil.
func ScanWindows() ([]ProcInfo, error) {
	if !IsWSL() {
		return nil, nil
	}
	return scanWSLWindows()
}

// scanWSLWindows uses tasklist.exe to list Windows processes visible from WSL.
func scanWSLWindows() ([]ProcInfo, error) {
	exe, err := exec.LookPath("tasklist.exe")
	if err != nil {
		return nil, nil // tasklist.exe not available
	}

	cmd := exec.Command(exe, "/fo", "CSV", "/nh")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tasklist.exe: %w", err)
	}

	var procs []ProcInfo
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
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
