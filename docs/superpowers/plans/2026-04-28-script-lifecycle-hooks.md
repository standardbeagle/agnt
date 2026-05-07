# Script Lifecycle Hooks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-script lifecycle hooks (`on-start`, `on-stop`, `on-crash`, `on-restart`) to `.agnt.kdl` that run shell commands at process lifecycle transitions, blocking up to 5 seconds, with the client able to disconnect during session cleanup.

**Architecture:** `ScriptLifecycleHooks` config struct added to `ScriptConfig`. A new `hook_runner.go` executes hooks via the platform shell (same `ResolveShell()` logic), inheriting `os.Environ()` + script env overrides, with a 5s hard timeout. Wire points: `on-start` via a new `onProcessRunning` callback on `HealthTracker`; `on-stop`/`on-crash` in `process_exit_watcher.go` before `emitScriptStopped`; `on-restart` in `process_autorestart.go` before re-launch. Session UNREGISTER becomes non-blocking: cleanup goroutine is spawned before the OK response so clients can disconnect immediately.

**Tech Stack:** Go 1.24.2, `os/exec`, KDL config via `kdl-go`, `internal/config`, `internal/daemon`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `internal/config/agnt.go` | Add `ScriptLifecycleHooks` struct + `Hooks` field on `ScriptConfig` |
| Create | `internal/daemon/hook_runner.go` | Execute a lifecycle hook: resolve shell, set env, 5s timeout |
| Modify | `internal/daemon/health_tracker.go` | Add `onProcessRunning` callback, fire on any → Running |
| Modify | `internal/daemon/daemon.go` | Wire `onProcessRunning` to hook runner via `scriptConfigs` lookup |
| Modify | `internal/daemon/process_exit_watcher.go` | Fire `on-stop` / `on-crash` before `emitScriptStopped` |
| Modify | `internal/daemon/process_autorestart.go` | Fire `on-restart` before `startScriptWithRetry` |
| Modify | `internal/daemon/hub_session.go` | Non-blocking UNREGISTER: goroutine cleanup, respond OK first |
| Create | `internal/daemon/hook_runner_test.go` | Unit tests for hook execution, timeout, env injection |
| Modify | `internal/config/agnt_test.go` | KDL parse tests for `ScriptLifecycleHooks` |
| Modify | `internal/daemon/health_tracker_test.go` | `onProcessRunning` fires on → Running |

---

## Task 1: Config — `ScriptLifecycleHooks`

**Files:**
- Modify: `internal/config/agnt.go`
- Modify: `internal/config/agnt_test.go`

- [ ] **Step 1: Write the failing parse test**

Add to `internal/config/agnt_test.go`:

```go
func TestScriptLifecycleHooks_Parse(t *testing.T) {
    kdl := `
scripts {
    backend {
        command "pwsh"
        hooks {
            on-start  "scripts/on-start.ps1"
            on-stop   "scripts/on-stop.ps1"
            on-crash  "scripts/on-crash.ps1"
            on-restart "scripts/on-restart.ps1"
        }
    }
}`
    cfg, err := ParseAgntConfig([]byte(kdl))
    require.NoError(t, err)
    h := cfg.Scripts["backend"].Hooks
    require.NotNil(t, h)
    assert.Equal(t, "scripts/on-start.ps1", h.OnStart)
    assert.Equal(t, "scripts/on-stop.ps1", h.OnStop)
    assert.Equal(t, "scripts/on-crash.ps1", h.OnCrash)
    assert.Equal(t, "scripts/on-restart.ps1", h.OnRestart)
}

func TestScriptLifecycleHooks_Nil_WhenAbsent(t *testing.T) {
    kdl := `scripts { backend { command "node" } }`
    cfg, err := ParseAgntConfig([]byte(kdl))
    require.NoError(t, err)
    assert.Nil(t, cfg.Scripts["backend"].Hooks)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/config/... -run TestScriptLifecycleHooks -v
```

Expected: FAIL — `ScriptLifecycleHooks` type does not exist yet.

- [ ] **Step 3: Add the config struct**

In `internal/config/agnt.go`, after the `AutoRestart` field in `ScriptConfig` (around line 251), add:

```go
    // Hooks defines shell commands to run at lifecycle transitions.
    Hooks *ScriptLifecycleHooks `kdl:"hooks"`
```

And add the new struct (after `ScriptConfig`, before `ResolveShell`):

```go
// ScriptLifecycleHooks defines shell commands run at process lifecycle transitions.
// Each field is a shell command string executed via the platform shell (same as ScriptConfig.Run).
// Hooks run with a 5-second timeout and inherit the script's effective environment.
// Injected env vars: AGNT_EVENT, AGNT_SCRIPT_ID, AGNT_EXIT_CODE (stop/crash only).
type ScriptLifecycleHooks struct {
    // OnStart fires after the process transitions to Running state.
    OnStart string `kdl:"on-start"`
    // OnStop fires after a clean process exit (user-initiated or expected).
    OnStop string `kdl:"on-stop"`
    // OnCrash fires after an unexpected exit (non-zero code, not user-initiated).
    OnCrash string `kdl:"on-crash"`
    // OnRestart fires before each auto-restart attempt.
    OnRestart string `kdl:"on-restart"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/config/... -run TestScriptLifecycleHooks -v
```

Expected: PASS

- [ ] **Step 5: Build check**

```bash
go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/config/agnt.go internal/config/agnt_test.go
git commit -m "feat(config): add ScriptLifecycleHooks to ScriptConfig"
```

---

## Task 2: Hook Runner

**Files:**
- Create: `internal/daemon/hook_runner.go`
- Create: `internal/daemon/hook_runner_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/daemon/hook_runner_test.go`:

```go
package daemon

import (
    "os"
    "runtime"
    "strings"
    "testing"
    "time"

    "github.com/standardbeagle/agnt/internal/config"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestRunLifecycleHook_SetsEnvVars(t *testing.T) {
    // Write env vars to a temp file so we can inspect them
    tmp, err := os.CreateTemp(t.TempDir(), "hook-env-*.txt")
    require.NoError(t, err)
    tmp.Close()

    var cmd string
    if runtime.GOOS == "windows" {
        cmd = `pwsh -Command "$env:AGNT_EVENT + '|' + $env:AGNT_SCRIPT_ID | Out-File -FilePath '` + tmp.Name() + `' -NoNewline"`
    } else {
        cmd = `echo "$AGNT_EVENT|$AGNT_SCRIPT_ID" > ` + tmp.Name()
    }

    scriptCfg := &config.ScriptConfig{}
    err = RunLifecycleHook(cmd, "mybackend", "start", scriptCfg, 0)
    require.NoError(t, err)

    data, _ := os.ReadFile(tmp.Name())
    content := strings.TrimSpace(string(data))
    assert.Equal(t, "start|mybackend", content)
}

func TestRunLifecycleHook_RespectsTimeout(t *testing.T) {
    var cmd string
    if runtime.GOOS == "windows" {
        cmd = "Start-Sleep -Seconds 60"
    } else {
        cmd = "sleep 60"
    }
    scriptCfg := &config.ScriptConfig{}
    start := time.Now()
    err := RunLifecycleHook(cmd, "backend", "stop", scriptCfg, 0)
    elapsed := time.Since(start)
    assert.ErrorContains(t, err, "timeout")
    assert.Less(t, elapsed, 7*time.Second, "must not block longer than timeout + buffer")
}

func TestRunLifecycleHook_ExitCodeEnvVar(t *testing.T) {
    tmp, err := os.CreateTemp(t.TempDir(), "hook-exitcode-*.txt")
    require.NoError(t, err)
    tmp.Close()

    var cmd string
    if runtime.GOOS == "windows" {
        cmd = `pwsh -Command "$env:AGNT_EXIT_CODE | Out-File -FilePath '` + tmp.Name() + `' -NoNewline"`
    } else {
        cmd = `echo "$AGNT_EXIT_CODE" > ` + tmp.Name()
    }

    scriptCfg := &config.ScriptConfig{}
    err = RunLifecycleHook(cmd, "svc", "crash", scriptCfg, 137)
    require.NoError(t, err)

    data, _ := os.ReadFile(tmp.Name())
    assert.Contains(t, strings.TrimSpace(string(data)), "137")
}

func TestRunLifecycleHook_InheritsScriptEnv(t *testing.T) {
    tmp, err := os.CreateTemp(t.TempDir(), "hook-custenv-*.txt")
    require.NoError(t, err)
    tmp.Close()

    var cmd string
    if runtime.GOOS == "windows" {
        cmd = `pwsh -Command "$env:MY_CUSTOM_VAR | Out-File -FilePath '` + tmp.Name() + `' -NoNewline"`
    } else {
        cmd = `echo "$MY_CUSTOM_VAR" > ` + tmp.Name()
    }

    scriptCfg := &config.ScriptConfig{
        Env: map[string]string{"MY_CUSTOM_VAR": "hello-from-script"},
    }
    err = RunLifecycleHook(cmd, "svc", "start", scriptCfg, 0)
    require.NoError(t, err)

    data, _ := os.ReadFile(tmp.Name())
    assert.Contains(t, strings.TrimSpace(string(data)), "hello-from-script")
}

func TestRunLifecycleHook_EmptyCmd_NoOp(t *testing.T) {
    err := RunLifecycleHook("", "svc", "start", &config.ScriptConfig{}, 0)
    assert.NoError(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/daemon/... -run TestRunLifecycleHook -v
```

Expected: FAIL — `RunLifecycleHook` undefined.

- [ ] **Step 3: Implement the hook runner**

Create `internal/daemon/hook_runner.go`:

```go
package daemon

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "runtime"
    "strconv"
    "time"

    "github.com/standardbeagle/agnt/internal/config"
    "github.com/standardbeagle/agnt/internal/debug"
)

const hookTimeout = 5 * time.Second

// RunLifecycleHook executes a lifecycle hook command for a script, blocking up
// to hookTimeout (5s). Returns an error on timeout or non-zero exit; callers
// should log the error and continue — hook failure never stops the lifecycle
// transition.
//
// cmd is a shell command string (same as ScriptConfig.Run). scriptID is the
// script name (e.g., "backend"). event is one of: start, stop, crash, restart.
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

    if err := c.Run(); err != nil {
        if ctx.Err() != nil {
            return fmt.Errorf("lifecycle hook %s/%s timeout after %s", scriptID, event, hookTimeout)
        }
        return fmt.Errorf("lifecycle hook %s/%s exited with error: %w", scriptID, event, err)
    }
    return nil
}

// resolveHookShell returns the shell and args for executing a hook command.
// Respects ScriptConfig.Shell/ShellArgs overrides; falls back to platform default.
// Uses a stack copy so the shared *ScriptConfig pointer is never mutated.
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
// Use this when the caller must not block (e.g., health_tracker callbacks).
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
```

> **Note:** `resolveHookShell` uses a stack copy (`tmp := *scriptCfg`) so the shared `*ScriptConfig` pointer stored in `d.scriptConfigs` is never mutated — no data race with concurrent readers.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/daemon/... -run TestRunLifecycleHook -v
```

Expected: PASS

- [ ] **Step 5: Build check**

```bash
go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/hook_runner.go internal/daemon/hook_runner_test.go
git commit -m "feat(daemon): add lifecycle hook runner with 5s timeout"
```

---

## Task 3: `on-start` — HealthTracker callback

**Files:**
- Modify: `internal/daemon/health_tracker.go`
- Modify: `internal/daemon/health_tracker_test.go`
- Modify: `internal/daemon/daemon.go`

- [ ] **Step 1: Write failing test for `onProcessRunning`**

Add to `internal/daemon/health_tracker_test.go` (find existing test file and append):

```go
func TestHealthTracker_OnProcessRunning_FiresOnStartTransition(t *testing.T) {
    var fired []string
    tracker := NewHealthTracker(
        func(id string) (*process.ManagedProcess, error) { return nil, nil },
        func(proxy.LogEntry, string) {},
    )
    tracker.onProcessRunning = func(processID string) {
        fired = append(fired, processID)
    }

    // Pending → Starting → Running should fire
    tracker.observe("proxy1", "proc1", goprocess.StateStarting)
    tracker.observe("proxy1", "proc1", goprocess.StateRunning)
    assert.Equal(t, []string{"proc1"}, fired)

    // Running → Running (steady state) should NOT fire again
    tracker.observe("proxy1", "proc1", goprocess.StateRunning)
    assert.Equal(t, []string{"proc1"}, fired)

    // Running → Stopped → Running (restart) should fire again
    tracker.observe("proxy1", "proc1", goprocess.StateStopped)
    tracker.observe("proxy1", "proc1", goprocess.StateStarting)
    tracker.observe("proxy1", "proc1", goprocess.StateRunning)
    assert.Equal(t, []string{"proc1", "proc1"}, fired)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/daemon/... -run TestHealthTracker_OnProcessRunning -v
```

Expected: FAIL — `onProcessRunning` field does not exist.

- [ ] **Step 3: Add the callback field and fire point**

In `internal/daemon/health_tracker.go`:

After the `onReturnToHealthy` field (around line 125), add:

```go
    // onProcessRunning fires on every any-state → Running transition.
    // Shares the same condition as onReturnToHealthy (prevState != Running →
    // Running) but is a separate slot so daemon and OutageClassifier can both
    // register independently without chaining.
    onProcessRunning func(processID string)
```

In `observe()`, after the block that sets `returnedToHealthy = true` (around line 248), in the existing callbacks section, add alongside the `onReturnToHealthy` call:

```go
    if returnedToHealthy && h.onProcessRunning != nil {
        func() {
            defer func() { _ = recover() }()
            h.onProcessRunning(processID)
        }()
    }
```

- [ ] **Step 4: Wire in daemon.go**

In `internal/daemon/daemon.go`, after `d.healthTracker = NewHealthTracker(...)` (around line 373), add:

```go
    d.healthTracker.onProcessRunning = func(processID string) {
        cfgVal, ok := d.scriptConfigs.Load(processID)
        if !ok {
            return
        }
        scriptCfg, ok := cfgVal.(*config.ScriptConfig)
        if !ok || scriptCfg.Hooks == nil || scriptCfg.Hooks.OnStart == "" {
            return
        }
        // Resolve script name from registry for AGNT_SCRIPT_ID
        scriptName := processID
        if entry, ok := d.scriptRegistry.GetByProcessID(processID); ok {
            scriptName = entry.Name
        }
        runLifecycleHookAsync(scriptCfg.Hooks.OnStart, scriptName, "start", scriptCfg, 0)
    }
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/daemon/... -run TestHealthTracker_OnProcessRunning -v
```

Expected: PASS

- [ ] **Step 6: Build check + full test run**

```bash
go build ./... && go test ./internal/daemon/... -count=1 -timeout 120s
```

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/health_tracker.go internal/daemon/health_tracker_test.go internal/daemon/daemon.go
git commit -m "feat(daemon): fire on-start hook via onProcessRunning callback"
```

---

## Task 4: `on-stop` and `on-crash` — process exit watcher

**Files:**
- Modify: `internal/daemon/process_exit_watcher.go`

- [ ] **Step 1: Locate the fire point**

In `process_exit_watcher.go`, `watchProcessExit()` goroutine: after `info := captureExitInfo(proc)` and after the guard `if current, err := ...; err == nil && current != proc { return }` — this is where the hook should fire, before `d.processExitInfo.Set(info)`.

The existing logic distinguishes clean stops via `info.Reason == "stopped"`.

- [ ] **Step 2: Add hook dispatch**

In `watchProcessExit` goroutine, after the guard block (around line 269), add:

```go
            // Fire on-stop or on-crash lifecycle hook if configured.
            if cfgVal, ok := d.scriptConfigs.Load(proc.ID); ok {
                if scriptCfg, ok := cfgVal.(*config.ScriptConfig); ok && scriptCfg.Hooks != nil {
                    scriptName := proc.ID
                    if entry, ok := d.scriptRegistry.GetByProcessID(proc.ID); ok {
                        scriptName = entry.Name
                    }
                    if info.Reason == "stopped" && scriptCfg.Hooks.OnStop != "" {
                        if err := RunLifecycleHook(scriptCfg.Hooks.OnStop, scriptName, "stop", scriptCfg, info.ExitCode); err != nil {
                            debug.Warn("daemon", "on-stop hook for %s: %v", scriptName, err)
                        }
                    } else if info.Reason != "stopped" && scriptCfg.Hooks.OnCrash != "" {
                        if err := RunLifecycleHook(scriptCfg.Hooks.OnCrash, scriptName, "crash", scriptCfg, info.ExitCode); err != nil {
                            debug.Warn("daemon", "on-crash hook for %s: %v", scriptName, err)
                        }
                    }
                }
            }
```

> **Note:** `RunLifecycleHook` blocks up to 5s, which is the approved budget. The watcher goroutine blocks here, but it doesn't hold any locks — proxy teardown (`emitScriptStopped`) simply waits 5s longer, which is acceptable.

- [ ] **Step 3: Build + verify existing tests still pass**

```bash
go build ./... && go test ./internal/daemon/... -run TestProcessExitWatcher -v -timeout 30s
```

- [ ] **Step 4: Manual smoke test (optional)**

Add a script to `.agnt.kdl` with `on-crash "echo crashed > /tmp/hook-fired"` and kill the process. Verify the file appears.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/process_exit_watcher.go
git commit -m "feat(daemon): fire on-stop/on-crash hooks from process exit watcher"
```

---

## Task 5: `on-restart` — auto-restarter

**Files:**
- Modify: `internal/daemon/process_autorestart.go`

- [ ] **Step 1: Locate the fire point**

In `process_autorestart.go`, the restart loop (around line 376): after marking `StateRestarting` and before the `startScriptWithRetry` call (around line 404), add the hook.

- [ ] **Step 2: Add hook dispatch**

After `scriptEntry.AddRestartMarker()` (around line 381), add:

```go
            // Fire on-restart hook before re-launching (blocks up to 5s).
            if cfgVal, ok := r.daemon.scriptConfigs.Load(processID); ok {
                if scriptCfg, ok := cfgVal.(*config.ScriptConfig); ok && scriptCfg.Hooks != nil && scriptCfg.Hooks.OnRestart != "" {
                    scriptName := processID
                    if entry, ok := r.daemon.scriptRegistry.GetByProcessID(processID); ok {
                        scriptName = entry.Name
                    }
                    if err := RunLifecycleHook(scriptCfg.Hooks.OnRestart, scriptName, "restart", scriptCfg, exitCode); err != nil {
                        debug.Warn("daemon", "on-restart hook for %s: %v", scriptName, err)
                    }
                }
            }
```

- [ ] **Step 3: Build + test**

```bash
go build ./... && go test ./internal/daemon/... -run TestAutoRestart -v -timeout 60s
```

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/process_autorestart.go
git commit -m "feat(daemon): fire on-restart hook before auto-restart"
```

---

## Task 6: Non-blocking UNREGISTER

**Files:**
- Modify: `internal/daemon/hub_session.go`

The problem: `hubHandleSessionUnregister` calls `CleanupSessionResources` synchronously. With hooks blocked up to 5s per script, the client is held open during cleanup. The fix: cancel pending deferred cleanup synchronously, then spawn `doCleanup` in a goroutine and respond OK immediately.

- [ ] **Step 1: Modify `hubHandleSessionUnregister`**

In `internal/daemon/hub_session.go` around line 373, change:

```go
// Before:
func (d *Daemon) hubHandleSessionUnregister(conn *hubpkg.Connection, cmd *hubproto.Command) error {
    if len(cmd.Args) < 1 {
        return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION UNREGISTER requires: <code>")
    }
    code := cmd.Args[0]
    d.CleanupSessionResources(code)
    return conn.WriteOK(fmt.Sprintf("session %s unregistered", code))
}
```

To:

```go
// After:
func (d *Daemon) hubHandleSessionUnregister(conn *hubpkg.Connection, cmd *hubproto.Command) error {
    if len(cmd.Args) < 1 {
        return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION UNREGISTER requires: <code>")
    }
    code := cmd.Args[0]
    // Cancel any pending deferred cleanup synchronously (safe: just cancels a timer).
    d.cancelPendingCleanup(code)
    // Run cleanup in background so client can disconnect immediately.
    // d.wg ensures daemon shutdown waits for in-flight cleanups.
    d.wg.Add(1)
    go func() {
        defer d.wg.Done()
        d.doCleanup(code)
    }()
    return conn.WriteOK(fmt.Sprintf("session %s unregistered", code))
}
```

- [ ] **Step 2: Verify `cancelPendingCleanup` and `doCleanup` are accessible**

Both are methods on `*Daemon`. `cancelPendingCleanup` is defined in `daemon_session_cleanup.go`. Confirm:

```bash
grep -n "func (d \*Daemon) cancelPendingCleanup\|func (d \*Daemon) doCleanup" internal/daemon/daemon_session_cleanup.go
```

- [ ] **Step 3: Build + test**

```bash
go build ./... && go test ./internal/daemon/... -count=1 -timeout 120s
```

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/hub_session.go
git commit -m "feat(daemon): non-blocking SESSION UNREGISTER — cleanup runs in background"
```

---

## Task 7: End-to-end test

**Files:**
- Create: `internal/daemon/lifecycle_hooks_test.go`

Use the pattern from `autostart_async_test.go`: `New(DaemonConfig{SocketPath: ...})` + `d.Start()` + `writeConfig(t, dir, kdl)` + `d.RunAutostart(ctx, dir)`. Stop processes via `d.hub.ProcessManager().Stop(ctx, id)`.

- [ ] **Step 1: Write integration tests**

Create `internal/daemon/lifecycle_hooks_test.go`:

```go
package daemon

import (
    "context"
    "os"
    "path/filepath"
    "runtime"
    "strings"
    "testing"
    "time"

    goprocess "github.com/standardbeagle/go-cli-server/process"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// waitForState polls the ProcessManager until processID reaches wantState or timeout.
func waitForState(t *testing.T, d *Daemon, processID string, wantState goprocess.ProcessState, timeout time.Duration) {
    t.Helper()
    require.Eventually(t, func() bool {
        proc, err := d.hub.ProcessManager().Get(processID)
        if err != nil {
            return false
        }
        return proc.State() == wantState
    }, timeout, 100*time.Millisecond, "process %s did not reach state %s", processID, wantState)
}

func TestLifecycleHooks_OnStart(t *testing.T) {
    // No t.Parallel(): starts real processes; PID-reuse kills them under high concurrency.
    if runtime.GOOS == "windows" {
        t.Skip("shell differences covered by TestRunLifecycleHook_SetsEnvVars")
    }

    dir := t.TempDir()
    flagFile := filepath.Join(dir, "on-start-fired")

    writeConfig(t, dir, `scripts {
    svc {
        run "sleep 60"
        autostart true
        hooks {
            on-start "touch `+flagFile+`"
        }
    }
}`)

    d := newDaemon(t, dir)
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    d.RunAutostart(ctx, dir)

    waitForState(t, d, "svc", goprocess.StateRunning, 10*time.Second)

    require.Eventually(t, func() bool {
        _, err := os.Stat(flagFile)
        return err == nil
    }, 3*time.Second, 50*time.Millisecond, "on-start flag file not created")
}

func TestLifecycleHooks_OnStop(t *testing.T) {
    // No t.Parallel(): starts real processes.
    if runtime.GOOS == "windows" {
        t.Skip("covered by hook_runner_test.go")
    }

    dir := t.TempDir()
    flagFile := filepath.Join(dir, "on-stop-fired")

    writeConfig(t, dir, `scripts {
    svc {
        run "sleep 60"
        autostart true
        hooks {
            on-stop "touch `+flagFile+`"
        }
    }
}`)

    d := newDaemon(t, dir)
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    d.RunAutostart(ctx, dir)

    waitForState(t, d, "svc", goprocess.StateRunning, 10*time.Second)

    stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer stopCancel()
    _ = d.hub.ProcessManager().Stop(stopCtx, "svc")

    require.Eventually(t, func() bool {
        _, err := os.Stat(flagFile)
        return err == nil
    }, 8*time.Second, 50*time.Millisecond, "on-stop flag file not created")
}

func TestLifecycleHooks_EnvVarsInjected(t *testing.T) {
    // No t.Parallel(): starts real processes.
    if runtime.GOOS == "windows" {
        t.Skip("covered by hook_runner_test.go")
    }

    dir := t.TempDir()
    envFile := filepath.Join(dir, "hook-env.txt")

    writeConfig(t, dir, `scripts {
    svc {
        run "sleep 60"
        autostart true
        hooks {
            on-start "echo $AGNT_EVENT:$AGNT_SCRIPT_ID > `+envFile+`"
        }
    }
}`)

    d := newDaemon(t, dir)
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    d.RunAutostart(ctx, dir)

    waitForState(t, d, "svc", goprocess.StateRunning, 10*time.Second)

    require.Eventually(t, func() bool {
        data, err := os.ReadFile(envFile)
        if err != nil {
            return false
        }
        return strings.Contains(strings.TrimSpace(string(data)), "start:svc")
    }, 3*time.Second, 50*time.Millisecond, "env vars not injected into on-start hook")
}

func TestLifecycleHooks_OnRestart(t *testing.T) {
    // No t.Parallel(): starts real processes.
    if runtime.GOOS == "windows" {
        t.Skip("covered by hook_runner_test.go")
    }

    dir := t.TempDir()
    flagFile := filepath.Join(dir, "on-restart-fired")

    // Process exits immediately (exit code 1) triggering auto-restart.
    writeConfig(t, dir, `scripts {
    svc {
        run "exit 1"
        autostart true
        auto-restart true
        hooks {
            on-restart "touch `+flagFile+`"
        }
    }
}`)

    d := newDaemon(t, dir)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    d.RunAutostart(ctx, dir)

    // on-restart fires before re-launch — flag appears within auto-restart window.
    require.Eventually(t, func() bool {
        _, err := os.Stat(flagFile)
        return err == nil
    }, 15*time.Second, 100*time.Millisecond, "on-restart hook did not fire")
}
```

- [ ] **Step 2: Verify helper availability**

`newDaemon` and `writeConfig` are defined in `autostart_async_test.go` in package `daemon`. Since the new file uses `package daemon` (not `daemon_test`), they are directly accessible.

- [ ] **Step 3: Run integration tests**

```bash
go test ./internal/daemon/... -run TestLifecycleHooks -v -timeout 60s
```

Expected: PASS

- [ ] **Step 4: Full test suite**

```bash
go test -count=1 -race -p 1 ./... -timeout 300s
```

All tests must pass.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/lifecycle_hooks_test.go
git commit -m "test(daemon): integration tests for script lifecycle hooks"
```

---

## Task 8: Documentation in default KDL template

**Files:**
- Modify: `internal/config/agnt.go` (default KDL string, around line 998)

- [ ] **Step 1: Add hooks example to default config comment block**

Find the `scripts` example in the `defaultKDL` string and add a commented-out `hooks` block:

```kdl
    // scripts {
    //     backend {
    //         run "npm run dev"
    //         autostart true
    //         ports 3000
    //         // hooks {
    //         //     on-start  "scripts/on-start.sh"   // fires when process reaches Running
    //         //     on-stop   "scripts/cleanup.sh"    // fires on clean exit
    //         //     on-crash  "scripts/on-crash.sh"   // fires on unexpected exit
    //         //     on-restart "scripts/on-restart.sh" // fires before auto-restart
    //         // }
    //     }
    // }
```

- [ ] **Step 2: Build + quick parse test**

```bash
go build ./... && go test ./internal/config/... -v -timeout 30s
```

- [ ] **Step 3: Commit**

```bash
git add internal/config/agnt.go
git commit -m "docs(config): add lifecycle hooks example to default KDL template"
```

---

## Completion Checklist

- [ ] All tests pass: `go test -count=1 -race -p 1 ./... -timeout 300s`
- [ ] `go build ./...` clean
- [ ] `gofmt -l ./internal/...` produces no output
- [ ] on-start fires when process reaches Running (not just starts)
- [ ] on-stop fires only on clean exits (`reason == "stopped"`)
- [ ] on-crash fires only on unexpected exits (`reason != "stopped"`)
- [ ] on-restart fires before each auto-restart attempt
- [ ] Hooks block up to 5s; timeout logged as warning, lifecycle continues
- [ ] AGNT_EVENT, AGNT_SCRIPT_ID, AGNT_EXIT_CODE injected into hook env
- [ ] Script Env block variables available in hook env
- [ ] Shell override (ScriptConfig.Shell) respected for hooks
- [ ] Signal-killed processes (SIGTERM/SIGKILL) fire `on-crash`, not `on-stop` — `info.Reason` is `"signal"` not `"stopped"`; this is intentional
- [ ] Works on Linux (sh -c) and Windows (cmd.exe /c or pwsh)
- [ ] SESSION UNREGISTER responds OK before cleanup finishes
- [ ] Daemon shutdown waits for in-flight cleanup goroutines (via d.wg)
- [ ] No hook configured → no performance cost (early return on empty string)
