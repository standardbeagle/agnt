//go:build !windows

package platform

import (
	"context"
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
	wslResult bool
	wslMu     sync.Once
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
	})
	return wslResult
}

// ShouldUseWindowsShell reports whether path should be executed via the
// Windows shell (cmd.exe) rather than the Unix shell (sh). Returns true
// only on WSL when path looks Windows-shaped: a /mnt/<drive>/... DrvFs
// mount or a string containing a backslash (e.g. C:\foo or relative
// scripts\build.cmd that the user copied in from a Windows context).
//
// On native Windows this returns true unconditionally — see the stub in
// process_windows.go. On non-WSL Unix it always returns false: there is
// no cmd.exe to dispatch to.
//
// The empty-path case returns false: callers (config.ScriptConfig.ResolveShell)
// pass either Cwd or a run-command path, and an empty Cwd must fall through
// to the platform default rather than forcing cmd.exe.
func ShouldUseWindowsShell(path string) bool {
	return shouldUseWindowsShellPath(path, IsWSL())
}

func shouldUseWindowsShellPath(path string, isWSL bool) bool {
	if !isWSL {
		return false
	}
	if path == "" {
		return false
	}
	// Backslash never appears in legitimate Linux paths and is the most
	// reliable Windows-path tell. Catches `C:\Users\foo`, relative
	// `scripts\build.cmd`, and UNC paths like `\\wsl$\...`.
	if strings.ContainsRune(path, '\\') {
		return true
	}
	// /mnt/<drive>/... is the WSL DrvFs mountpoint pattern. Drive
	// letters are always lowercase ASCII in the canonical form.
	if strings.HasPrefix(path, "/mnt/") && len(path) >= 7 {
		c := path[5]
		if c >= 'a' && c <= 'z' && path[6] == '/' {
			return true
		}
	}
	return false
}

// ShouldUseWindowsCommand classifies a full shell command by its executable
// token only. Backslashes in later Linux-shell arguments are escapes, not a
// signal that the whole command belongs to cmd.exe.
func ShouldUseWindowsCommand(command string) bool {
	return shouldUseWindowsCommand(command, IsWSL())
}

func shouldUseWindowsCommand(command string, isWSL bool) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	executable := command
	if command[0] == '"' || command[0] == '\'' {
		quote := command[0]
		if end := strings.IndexByte(command[1:], quote); end >= 0 {
			executable = command[1 : end+1]
		}
	} else if fields := strings.Fields(command); len(fields) > 0 {
		executable = fields[0]
	}
	return shouldUseWindowsShellPath(executable, isWSL)
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

// killPID sends SIGTERM, waits, then SIGKILL.
func killPID(pid int, gracefulTimeout int) error {
	return killPIDWith(pid, gracefulTimeout, ProcessBirthID, syscall.Getpgid, syscall.Kill)
}

func killPIDWith(pid int, gracefulTimeout int, birthFn func(int) (string, bool),
	getpgidFn func(int) (int, error), killFn func(int, syscall.Signal) error,
) error {
	if pid <= 1 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	birth, haveBirth := birthFn(pid)
	if !haveBirth || birth == "" {
		return fmt.Errorf("cannot verify pid %d identity before signaling", pid)
	}
	groupLeader, err := verifiedPIDLeadership(pid, birth, birthFn, getpgidFn)
	if err != nil {
		return err
	}
	if groupLeader {
		_ = killFn(-pid, syscall.SIGTERM)
	}
	_ = killFn(pid, syscall.SIGTERM)

	deadline := time.Now().Add(time.Duration(gracefulTimeout) * time.Second)
	for time.Now().Before(deadline) {
		if err := killFn(pid, 0); err != nil {
			return nil // process gone
		}
		time.Sleep(100 * time.Millisecond)
	}

	// PID reuse can occur during the grace delay. Fail closed unless the same
	// kernel birth identity is still present immediately before escalation.
	groupLeader, err = verifiedPIDLeadership(pid, birth, birthFn, getpgidFn)
	if err != nil {
		return fmt.Errorf("pid %d changed before SIGKILL escalation: %w", pid, err)
	}
	if groupLeader {
		_ = killFn(-pid, syscall.SIGKILL)
	}
	_ = killFn(pid, syscall.SIGKILL)
	return nil
}

// verifiedPIDLeadership closes the Getpgid PID-reuse window by requiring the
// same non-empty kernel birth identity both before and after leadership lookup.
func verifiedPIDLeadership(pid int, expectedBirth string, birthFn func(int) (string, bool),
	getpgidFn func(int) (int, error),
) (bool, error) {
	if expectedBirth == "" {
		return false, fmt.Errorf("empty process birth identity")
	}
	pgid, err := getpgidFn(pid)
	if err != nil {
		return false, fmt.Errorf("getpgid(%d): %w", pid, err)
	}
	currentBirth, ok := birthFn(pid)
	if !ok || currentBirth == "" || currentBirth != expectedBirth {
		return false, fmt.Errorf("process birth identity no longer matches")
	}
	return pgid == pid, nil
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

	// Hard timeout: WSL→Windows interop (tasklist.exe) can wedge on a stalled
	// VM/9P mount. Without a deadline this hangs the duplicate-scan goroutine
	// (runs at startup + every 30s) forever. 3s matches the netstat.exe budget.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "/fo", "CSV", "/nh")
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
		fields := SplitTasklistCSVLine(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		pid, err := strconv.Atoi(fields[1])
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

// SplitTasklistCSVLine splits one tasklist.exe CSV row, respecting quoted
// fields (the memory column embeds commas like "45,000 K"). Surrounding
// double quotes are stripped from each field.
func SplitTasklistCSVLine(line string) []string {
	var fields []string
	var buf strings.Builder
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			fields = append(fields, buf.String())
			buf.Reset()
		default:
			buf.WriteByte(c)
		}
	}
	fields = append(fields, buf.String())
	return fields
}
