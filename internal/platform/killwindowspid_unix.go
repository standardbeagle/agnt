//go:build !windows

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// KillWindowsPID forcibly terminates a Windows-side process identified by pid
// from a WSL host by shelling to taskkill.exe /T /F /PID <n>. Returns nil on
// success, an error wrapping taskkill's stderr otherwise.
//
// taskkill.exe is the only mechanism that can reach a Windows-side PID from
// WSL; syscall.Kill returns ESRCH because the PIDs live in distinct kernel
// namespaces. Use this helper exclusively for PIDs you know are Windows-side
// (e.g. surfaced by FindPIDsByPort's netstat.exe fallback). Calling it on a
// Linux-side PID will silently no-op or fail because that PID does not exist
// in the Windows tasklist.
//
// Errors are intentionally not nil-returned silently: callers must surface
// them as visible warning events per the Silent Failure Prohibition rule
// (.claude/rules/daemon-architecture.md). The most common failure modes:
//   - taskkill.exe missing — WSL interop disabled or not on PATH
//   - exit 128 — invalid argument (PID <= 0)
//   - exit 1 with "ERROR: The process \"<n>\" not found" — PID already gone
//   - exit 1 with "ERROR: Access is denied" — Windows ACL blocks termination
//
// The 5s context deadline guards against a hung taskkill.exe holding up
// shutdown / preflight; the call is best-effort and a deadline-exceeded
// error is treated the same as any other failure by callers.
func KillWindowsPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("kill windows pid: invalid pid %d", pid)
	}
	if !IsWSL() {
		return fmt.Errorf("kill windows pid %d: not running under WSL", pid)
	}
	exe, err := exec.LookPath("taskkill.exe")
	if err != nil {
		return fmt.Errorf("kill windows pid %d: taskkill.exe not found: %w", pid, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, taskkillArgs(pid)...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := bytes.TrimSpace(stderr.Bytes())
		if len(msg) == 0 {
			return fmt.Errorf("kill windows pid %d: %w", pid, err)
		}
		return fmt.Errorf("kill windows pid %d: %s: %w", pid, string(msg), err)
	}
	return nil
}

func taskkillArgs(pid int) []string {
	return []string{"/T", "/F", "/PID", strconv.Itoa(pid)}
}
