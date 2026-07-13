//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/term"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
)

// consoleRestore captures the console state mutated by runWithConPTY so
// the deferred restore path stays declarative. Returned by
// enterConsoleRawMode and consumed by its Restore() method.
type consoleRestore struct {
	stdoutHandle    windows.Handle
	savedStdoutMode uint32
	hasStdoutMode   bool
	oldState        *term.State
}

// Restore puts the terminal back the way enterConsoleRawMode found it.
// Always disables win32-input-mode FIRST while VT processing is still
// enabled, then restores raw-mode and stdout console mode. Idempotent —
// safe to call from a deferred function.
func (c *consoleRestore) Restore() {
	// Disable win32-input-mode BEFORE restoring console modes — must
	// happen while VT processing is still enabled.
	fmt.Fprint(os.Stdout, "\x1b[?9001l")
	if c.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), c.oldState)
	}
	if c.hasStdoutMode {
		_ = windows.SetConsoleMode(c.stdoutHandle, c.savedStdoutMode)
	}
}

// enterConsoleRawMode saves the current stdout console mode, puts stdin
// into raw mode, and enables ENABLE_VIRTUAL_TERMINAL_PROCESSING on
// stdout so ConPTY-emitted escape sequences render correctly. Returns a
// consoleRestore that the caller MUST defer Restore() on. If enabling VT
// output fails after stdin entered raw mode, stdin is restored before the
// error is returned.
func enterConsoleRawMode() (*consoleRestore, error) {
	cr := &consoleRestore{}

	// Save stdout console mode BEFORE any changes so we can restore on
	// exit. Must happen before MakeRaw since ConPTY creation can alter modes.
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil {
		cr.stdoutHandle = h
		if err := windows.GetConsoleMode(h, &cr.savedStdoutMode); err == nil {
			cr.hasStdoutMode = true
		}
	}

	// Set stdin in raw mode BEFORE creating ConPTY so ConPTY doesn't
	// inherit/interfere with the console mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, fmt.Errorf("failed to set raw mode: %w", err)
	}
	cr.oldState = oldState

	// Enable Virtual Terminal Processing on stdout.
	if cr.hasStdoutMode {
		newMode := cr.savedStdoutMode | 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING
		if err := finishConsoleSetup(
			func() error { return windows.SetConsoleMode(cr.stdoutHandle, newMode) },
			func() error { return term.Restore(int(os.Stdin.Fd()), oldState) },
		); err != nil {
			return nil, fmt.Errorf("failed to enable virtual terminal processing: %w", err)
		}
	}

	return cr, nil
}

// setupSessionJob creates a Windows Job Object and assigns the PTY child
// PID to it so all descendant processes (dev servers, npm, etc.) are
// killed when agnt exits. Returns the handle (zero on any failure) and
// a closer the caller MUST defer if non-nil. The handle is also intended
// to be published to the daemon as SessionJobHandle so explicit
// `SESSION UNREGISTER` cleanup can call TerminateJobObject immediately
// rather than waiting for agnt's process-exit to fire
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
//
// Surfaces failure to BOTH the debug log AND stderr — silent-failure
// prohibition (.claude/rules/daemon-architecture.md). Without the job
// object, grandchild dev servers can survive `agnt run` termination —
// the exact bug the job object exists to prevent.
func setupSessionJob(pid int) (handle uint64, closer func()) {
	jobHandle, err := platform.CreateSessionJobObject()
	if err != nil {
		debug.Warn("job", "failed to create session job object: %v", err)
		fmt.Fprintf(os.Stderr, "warning: failed to create session job object: %v (child processes may survive agnt exit)\n", err)
		return 0, nil
	}
	closer = func() { _ = windows.CloseHandle(jobHandle) }
	if err := platform.AssignPIDToSessionJob(jobHandle, pid); err != nil {
		debug.Warn("job", "failed to assign PTY child to session job: %v", err)
		fmt.Fprintf(os.Stderr, "warning: failed to assign PTY child to session job: %v\n", err)
		// Closer still returned so the empty job's handle is released —
		// but don't publish it: an empty job has no containment value
		// and would make TerminateJobObject succeed misleadingly.
		return 0, closer
	}
	return uint64(jobHandle), closer
}

// getTerminalSize tries multiple methods to get terminal size.
// VS Code and other embedded terminals may not report size correctly on stdin.
func getTerminalSize() (width, height int) {
	// Method 1: Try stdin
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && w > 0 && h > 0 {
		return w, h
	}
	// Method 2: Try stdout
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		return w, h
	}
	// Method 3: Windows Console API
	if w, h, err := getConsoleSize(); err == nil && w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}

// getConsoleSize gets the current console size via the Windows Console API.
func getConsoleSize() (width, height int, err error) {
	handle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return 80, 24, err
	}

	var info windows.ConsoleScreenBufferInfo
	err = windows.GetConsoleScreenBufferInfo(handle, &info)
	if err != nil {
		return 80, 24, err
	}

	width = int(info.Window.Right - info.Window.Left + 1)
	height = int(info.Window.Bottom - info.Window.Top + 1)
	return width, height, nil
}

// resolveCommand resolves a command using multi-step fallback:
//  1. exec.LookPath — direct PATH lookup (fast path)
//  2. PowerShell Get-Command — finds commands PowerShell knows about
//  3. cmd.exe where — finds commands in PATH extensions (.cmd, .bat, etc.)
//  4. PowerShell shell wrap — runs the command inside PowerShell (like Unix bash -ic)
func resolveCommand(command string) (path string, shellWrap bool, err error) {
	if p, err := exec.LookPath(command); err == nil {
		return p, false, nil
	}
	if p := resolveViaPowerShell(command); p != "" {
		return p, false, nil
	}
	if p := resolveViaWhere(command); p != "" {
		return p, false, nil
	}
	psPath, err := exec.LookPath("powershell.exe")
	if err != nil {
		return "", false, fmt.Errorf("command not found: %s (powershell.exe also not available for fallback)", command)
	}
	return psPath, true, nil
}

// resolveViaPowerShell uses PowerShell's Get-Command to find a command's path.
func resolveViaPowerShell(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// -NoProfile to skip profile loading for speed.
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-Command %s -ErrorAction SilentlyContinue).Source", psQuote(command)))
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveViaWhere uses cmd.exe's where command to find executables.
func resolveViaWhere(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "where", command)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		result := strings.TrimSpace(lines[0])
		if result != "" {
			return result
		}
	}
	return ""
}

// buildPowerShellWrapArgs builds arguments for running a command inside PowerShell.
// PowerShell loads $PROFILE automatically, so commands available in the user's
// PowerShell session (aliases, functions, PATH additions) will be found.
func buildPowerShellWrapArgs(command string, args []string) []string {
	var parts []string
	parts = append(parts, psQuote(command))
	for _, arg := range args {
		parts = append(parts, psQuote(arg))
	}
	cmdStr := strings.Join(parts, " ")
	return []string{"-NoLogo", "-Command", "& { " + cmdStr + " }"}
}

// psQuote quotes a string for safe use in PowerShell commands.
// Uses single quotes and doubles embedded single quotes.
func psQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n'\"\\$`(){}[]<>&|;,@#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
