package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

const hookTimeout = 5 * time.Second

// RunLifecycleHook executes a lifecycle hook command for a script, blocking up
// to hookTimeout (5s). Returns an error on timeout or non-zero exit; callers
// log the error and continue — hook failure never stops the lifecycle transition.
//
// cmd is a shell command string (same as ScriptConfig.Run). scriptID is the
// script name. event is one of: start, stop, crash, restart.
// exitCode is the process exit code (meaningful for stop/crash events).
//
// Env: os.Environ() + script Env block overrides + injected AGNT_* vars.
func RunLifecycleHook(cmd, scriptID, event string, scriptCfg *config.ScriptConfig, exitCode int) error {
	if cmd == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	shell, shellArgs := resolveHookShell(cmd, scriptCfg)
	c := exec.CommandContext(ctx, shell, shellArgs...)
	c.Env = buildHookEnv(scriptCfg, scriptID, event, exitCode)
	configureHookProcessGroup(c)

	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("lifecycle hook %s/%s timeout after %s", scriptID, event, hookTimeout)
		}
		return fmt.Errorf("lifecycle hook %s/%s exited with error: %w", scriptID, event, err)
	}
	return nil
}

// resolveHookShell returns the shell and args for a hook command string.
// Uses a stack copy of scriptCfg so the shared pointer is never mutated.
func resolveHookShell(cmd string, scriptCfg *config.ScriptConfig) (string, []string) {
	tmp := *scriptCfg // shallow copy — ResolveShell only reads Shell, ShellArgs, Run
	tmp.Run = cmd
	return tmp.ResolveShell()
}

// buildHookEnv constructs the environment for a lifecycle hook:
// os.Environ() + script Env block (last-wins for duplicates) + AGNT_* injections.
func buildHookEnv(scriptCfg *config.ScriptConfig, scriptID, event string, exitCode int) []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+len(scriptCfg.Env)+3)
	env = append(env, base...)
	for k, v := range scriptCfg.Env {
		env = append(env, k+"="+v)
	}
	env = append(env,
		"AGNT_EVENT="+event,
		"AGNT_SCRIPT_ID="+scriptID,
		"AGNT_EXIT_CODE="+strconv.Itoa(exitCode),
	)
	return env
}

// runLifecycleHookAsync runs a lifecycle hook in a goroutine, logging errors.
// Use when the caller must not block.
func runLifecycleHookAsync(cmd, scriptID, event string, scriptCfg *config.ScriptConfig, exitCode int) {
	if cmd == "" {
		return
	}
	go func() {
		if err := RunLifecycleHook(cmd, scriptID, event, scriptCfg, exitCode); err != nil {
			debug.Warn("daemon", "lifecycle hook %s/%s: %v", scriptID, event, err)
		}
	}()
}
