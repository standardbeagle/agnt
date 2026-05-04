//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aymanbagabas/go-pty"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// runPlatformPTY is the Windows entry point dispatched from runCommand
// (defined in pty_common.go). It owns the ConPTY backend, Job Object
// lifecycle, console-mode save/restore, and the win32-input-mode quirks
// that genuinely differ from the Unix path. The polling resize loop
// lives in signals_windows.go; command resolution and BrowserHelper are
// in resolve_windows.go and browser_helper_windows.go.
func runPlatformPTY(commandArgs []string, _socketPath string, _sessionCode string) error {
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	return runWithConPTY(ctx, commandArgs, overlaySocketPath, sessionCode)
}

// runWithConPTY runs a command in a ConPTY with overlay support.
func runWithConPTY(ctx context.Context, args []string, socketPath string, sessionCode string) error {
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

	// Get initial terminal size BEFORE any mode changes — VS Code and
	// other embedded terminals may not report size correctly on stdin.
	width, height := getTerminalSize()
	childHeight := height
	if useTermOverlay && showIndicator && height > 1 {
		childHeight = height - 1
	}

	// Save+enter raw console mode (lives in resolve_windows.go because
	// it's pure Windows console plumbing). Restore() must run before
	// any other deferred restore that touches stdout.
	consoleState, err := enterConsoleRawMode()
	if err != nil {
		return err
	}
	defer consoleState.Restore()

	stopSpinner := spinner(fmt.Sprintf("Starting %s...", command))

	ptmx, err := pty.New()
	if err != nil {
		stopSpinner()
		return fmt.Errorf("failed to create PTY: %w", err)
	}
	defer ptmx.Close()
	if err := ptmx.Resize(width, childHeight); err != nil {
		log.Printf("warning: failed to set initial PTY size: %v", err)
	}

	resolvedPath, shellWrap, err := resolveCommand(command)
	if err != nil {
		stopSpinner()
		return err
	}

	var cmd *pty.Cmd
	if shellWrap {
		cmd = ptmx.Command(resolvedPath, buildPowerShellWrapArgs(command, cmdArgs)...)
	} else {
		cmd = ptmx.Command(resolvedPath, cmdArgs...)
	}
	cmd.Env = append(os.Environ(), "AGNT_PROJECT_PATH="+projectPath)
	if err := cmd.Start(); err != nil {
		stopSpinner()
		return fmt.Errorf("failed to start process: %w", err)
	}
	stopSpinner()

	// Create a Job Object so all child processes are killed when agnt
	// exits — see setupSessionJob for the full rationale.
	sessionJobHandle, jobCloser := setupSessionJob(cmd.Process.Pid)
	if jobCloser != nil {
		defer jobCloser()
	}

	// Disable win32-input-mode — ConPTY requests this by default which
	// causes Windows Terminal to send extended key event sequences instead
	// of raw bytes. We need standard VT input for our InputRouter to
	// work correctly. See:
	// https://github.com/microsoft/terminal/blob/main/doc/specs/%234999%20-%20Improved%20keyboard%20handling%20in%20Conpty.md
	fmt.Fprint(os.Stdout, "\x1b[?9001l")
	fmt.Fprint(os.Stdout, "\x1b[2J\x1b[H")

	processExited := make(chan struct{})
	handle := &ptyHandle{
		Backend:          ptmx,
		Width:            width,
		Height:           height,
		SessionJobHandle: sessionJobHandle,
		EnableSplash:     false, // ConPTY clears the screen on first paint
		Resize:           ptmx.Resize,
		Interrupt: func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		},
		// On Windows there is no SIGWINCH; after a menu closes (gate
		// unfreezes) the gate-unfreeze callback re-enforces the scroll
		// region. We must NOT call termOverlay.Redraw() here because
		// hideMenu / closeProcessViewer hold overlay.mu when they call
		// gate.Unfreeze, and Redraw also locks overlay.mu — would deadlock.
		PreRedraw: nil,
		// BrowserHelper auto-opens OAuth URLs that ConPTY can't surface
		// (Claude etc. try to open a browser via platform-default
		// browser, which fails inside ConPTY).
		WrapOutput: func(dest io.Writer) io.Writer { return NewBrowserHelper(dest) },
	}

	rt := runOverlayPipeline(ctx, handle, command, cmdArgs, adapter, adapterPrompt, projectPath)

	// Monitor process exit separately — close PTY when process exits so
	// io.Copy in the shared pipeline returns even if PTY stays open.
	go func() {
		_ = cmd.Wait()
		close(processExited)
		ptmx.Close()
	}()

	select {
	case <-ctx.Done():
	case <-rt.Done():
	case <-processExited:
	}
	rt.Stop()

	// Give process a moment to fully exit, force kill if needed.
	select {
	case <-processExited:
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	cleanupTerminal(height)
	return nil
}

// Compile-time interface check — the ConPTY backend (aymanbagabas/go-pty
// Pty) must satisfy the interface the shared overlay pipeline expects.
var _ overlay.PtyReadWriter = (pty.Pty)(nil)
