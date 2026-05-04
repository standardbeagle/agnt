//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// runPlatformPTY is the Unix entry point dispatched from runCommand
// (defined in pty_common.go). It owns the PTY backend, signal wiring,
// and child-cleanup paths that genuinely differ from Windows. Everything
// reusable across both platforms lives in pty_common.go; everything
// signal-driven (SIGCHLD/SIGWINCH) lives in signals_unix.go; shell
// resolution lives in resolve_unix.go.
func runPlatformPTY(commandArgs []string, _socketPath string, _sessionCode string) error {
	// Ignore SIGPIPE to prevent unexpected shutdowns when writing to closed
	// connections. This is important when the accessibility audit or other
	// JS execution returns large responses and the connection is interrupted
	// (e.g., Claude Code disconnects).
	signal.Ignore(syscall.SIGPIPE)

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	return runWithPTY(ctx, commandArgs, overlaySocketPath, sessionCode)
}

// runWithPTY runs a command in a creack/pty PTY with overlay support.
func runWithPTY(ctx context.Context, args []string, socketPath string, sessionCode string) error {
	command := args[0]
	cmdArgs := args[1:]

	if sessionCode == "" {
		sessionCode = generateSessionCode(command)
	}
	projectPath, _ := os.Getwd()

	// Validate .agnt.kdl config early — before any PTY/terminal setup.
	// Parse errors are fatal: the user has a config they expect to work.
	if configPath := config.FindAgntConfigFile(projectPath); configPath != "" {
		if _, err := config.LoadAgntConfigFile(configPath); err != nil {
			return fmt.Errorf("%s: %w", configPath, err)
		}
	}

	adapter := resolveAgentAdapter(command, projectPath)
	var adapterPrompt string
	if adapter != nil {
		adapterPrompt = buildAgntSystemPrompt(socketPath)
		cmdArgs = adapter.BuildArgs(cmdArgs, adapterPrompt)
	}

	stopSpinner := spinner(fmt.Sprintf("Starting %s...", command))
	c := commandWithArgs(command, cmdArgs...)
	c.Env = append(os.Environ(), "AGNT_PROJECT_PATH="+projectPath)

	ptmx, err := pty.Start(c)
	stopSpinner()
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// Capture the PTY child's pgid. creack/pty sets Setsid=true on the
	// child, which makes the child both a new session leader and a new
	// pgid leader — so the child's pgid equals its PID and identifies
	// every process the coding agent spawns via non-interactive bash
	// (including backgrounded jobs like `npm run dev &`). On session
	// cleanup the daemon uses this pgid to reap orphans via killpg.
	sessionPGID := 0
	if c.Process != nil {
		if pgid, err := syscall.Getpgid(c.Process.Pid); err == nil {
			sessionPGID = pgid
		} else {
			debug.Warn("run", "getpgid(%d): %v (session pgid tracking disabled)", c.Process.Pid, err)
		}
	}

	// Clear screen before child outputs to prevent visual artifacts from
	// previous terminal content showing through.
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")

	width, height := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		width, height = w, h
	}

	// Reserve bottom row for indicator bar by sizing child terminal one
	// row shorter so the child cannot draw in the indicator area.
	childHeight := height
	if useTermOverlay && showIndicator && height > 1 {
		childHeight = height - 1
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(childHeight), Cols: uint16(width)}); err != nil {
		debug.Warn("run", "error setting pty size: %s", err)
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	handle := &ptyHandle{
		Backend:      ptmx,
		Width:        width,
		Height:       height,
		SessionPGID:  sessionPGID,
		EnableSplash: true,
		Resize: func(childCols, childRows int) error {
			return pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(childRows), Cols: uint16(childCols)})
		},
		Interrupt: func() {
			// Send SIGINT to the entire process group so all children
			// receive the signal and can clean up. PTY processes get
			// their own session via creack/pty's Setsid, so the negative
			// PID targets all processes in that session's process group.
			if c.Process != nil {
				if pgid, err := syscall.Getpgid(c.Process.Pid); err == nil && pgid > 0 {
					_ = syscall.Kill(-pgid, syscall.SIGINT)
				} else {
					_ = c.Process.Signal(syscall.SIGINT)
				}
			}
		},
		PreRedraw: func() {
			// SIGWINCH forces the child to redraw, clearing any overlay
			// artifacts left by the menu.
			if c.Process != nil {
				_ = c.Process.Signal(syscall.SIGWINCH)
			}
		},
	}

	rt := runOverlayPipeline(ctx, handle, command, cmdArgs, adapter, adapterPrompt, projectPath)

	// Ctrl+Z support — handleSIGCHLD lives in signals_unix.go because
	// SIGCHLD/SIGSTOP/SIGCONT are fundamentally Unix-only.
	go handleSIGCHLD(ctx, rt.Done(), c, ptmx, oldState, rt, &width, &height)

	<-rt.Done()
	rt.Stop()
	_ = c.Wait()
	cleanupTerminal(height)
	return nil
}

// Compile-time interface check — *os.File is the creack/pty backend, and
// must satisfy the interface the shared overlay pipeline expects.
var _ overlay.PtyReadWriter = (*os.File)(nil)
