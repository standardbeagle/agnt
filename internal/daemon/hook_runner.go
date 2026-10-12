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
//
// dir is the hook's working directory — the script's own resolved working
// directory, so a relative hook command means the same thing as it does in the
// script's `run`. Empty falls back to the daemon's cwd (exec default).
func RunLifecycleHook(cmd, scriptID, event string, scriptCfg *config.ScriptConfig, dir string, exitCode int) error {
	return runLifecycleHookWithTimeout(hookTimeout, cmd, scriptID, event, scriptCfg, dir, exitCode)
}

// runLifecycleHookWithTimeout is RunLifecycleHook with the kill deadline as a
// parameter. Production always passes hookTimeout via RunLifecycleHook; the
// seam exists so tests can assert the "overrunning hook is killed and reports
// timeout" property without paying the 5s production window in gate time. The
// default itself is pinned directly as a constant assertion, which is free and
// clearer than inferring it from elapsed wall time.
func runLifecycleHookWithTimeout(timeout time.Duration, cmd, scriptID, event string, scriptCfg *config.ScriptConfig, dir string, exitCode int) error {
	if cmd == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	shell, shellArgs := resolveHookShell(cmd, scriptCfg)
	c := exec.CommandContext(ctx, shell, shellArgs...)
	c.Dir = dir
	c.Env = buildHookEnv(scriptCfg, scriptID, event, exitCode)
	configureHookProcessGroup(c)

	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("lifecycle hook %s/%s timeout after %s", scriptID, event, timeout)
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

// runLifecycleHook runs a script's lifecycle hook in the script's configured
// working directory, blocking up to hookTimeout. A failing hook never stops
// the lifecycle transition, but it must not vanish either: the failure is
// recorded in the project's startup log, which is what the agent and the
// session log read (debug.Warn alone only reaches the debug file).
//
// Callers that must not block run it on a goroutine.
func (d *Daemon) runLifecycleHook(cmd, scriptName, event string, scriptCfg *config.ScriptConfig, projectPath string, exitCode int) {
	if cmd == "" {
		return
	}
	dir := resolveWorkingDir(projectPath, scriptCfg.Cwd)
	if err := RunLifecycleHook(cmd, scriptName, event, scriptCfg, dir, exitCode); err != nil {
		debug.Warn("daemon", "on-%s hook for %s: %v", event, scriptName, err)
		d.startupLog(projectPath).Warn(scriptName, "lifecycle_hook_failed",
			fmt.Sprintf("on-%s hook failed (cwd %s): %v", event, dir, err))
	}
}
