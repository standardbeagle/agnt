# Startup Port Pre-flight Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect and resolve port conflicts before autostart scripts launch, with configurable kill/prompt/skip/fail policies.

**Architecture:** Pre-flight check runs inside `RunAutostart()` between config load and script start. Port detection reuses existing `FindPIDsByPort()`. Kill uses process-group + descendant tree (aggressive). Prompt mode uses two new IPC verbs (`AUTOSTART CLEAR-PORTS` / `AUTOSTART CONTINUE`) for daemon↔client round-trip. Client-side prompt displays in `displayAutostartResults`.

**Tech Stack:** Go 1.24, KDL config, Unix syscalls (SIGTERM/SIGKILL process groups), `/proc` filesystem

**Design doc:** `docs/plans/2026-04-01-startup-port-preflight-design.md`

**Task dependencies:**
- Tasks 1, 2, 4 are independent — can run in parallel
- Task 3 (go-cli-server upgrade) must complete before Task 3b (agnt kill wrapper)
- Task 3b must complete before Task 5 (RunAutostart integration)
- Task 6 depends on Task 5 (IPC verbs reference pending autostart state)
- Task 7 depends on Task 6 (client-side prompt calls IPC verbs)
- Task 8 depends on Tasks 5-7 (e2e tests exercise full flow)
- Task 9 is independent (proxy overlay wiring — no port-preflight dependency)
- Tasks 10-11 are independent (proxylog serialization fixes — no port-preflight dependency)
- Task 11 depends on Task 10 (audit builds on panel_message fix)
- Task 12 depends on all prior tasks

---

### Task 1: ProcessNameByPID Helper

**Files:**
- Modify: `internal/config/portdetect_unix.go`
- Modify: `internal/config/portdetect_windows.go`
- Create: `internal/config/portdetect_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/config/portdetect_test.go
package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProcessNameByPID_Self(t *testing.T) {
	name := ProcessNameByPID(os.Getpid())
	assert.NotEmpty(t, name, "should return process name for own PID")
}

func TestProcessNameByPID_Invalid(t *testing.T) {
	name := ProcessNameByPID(-1)
	assert.Empty(t, name, "should return empty for invalid PID")
}

func TestProcessNameByPID_NonExistent(t *testing.T) {
	name := ProcessNameByPID(999999999)
	assert.Empty(t, name, "should return empty for non-existent PID")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run TestProcessNameByPID ./internal/config/`
Expected: FAIL — `ProcessNameByPID` not defined

- [ ] **Step 3: Implement ProcessNameByPID**

In `internal/config/portdetect_unix.go`, add:

```go
// ProcessNameByPID returns the process name for a given PID.
// Returns empty string if the PID doesn't exist or can't be read.
func ProcessNameByPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	// Try /proc/[pid]/comm first (short name, no path)
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err == nil {
			name := strings.TrimSpace(string(data))
			if name != "" {
				return name
			}
		}
	}
	// Fallback: /proc/[pid]/cmdline (first arg, basename)
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	parts := strings.SplitN(string(data), "\x00", 2)
	if len(parts) > 0 && parts[0] != "" {
		return filepath.Base(parts[0])
	}
	return ""
}
```

In `internal/config/portdetect_windows.go`, add a stub:

```go
// ProcessNameByPID returns the process name for a given PID.
// Returns empty string if the PID doesn't exist or can't be read.
func ProcessNameByPID(pid int) string {
	// TODO: implement via Windows API (CreateToolhelp32Snapshot)
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -run TestProcessNameByPID ./internal/config/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/portdetect_unix.go internal/config/portdetect_windows.go internal/config/portdetect_test.go
git commit -m "feat: add ProcessNameByPID helper for port conflict reporting"
```

---

### Task 2: PortConflict Type and Detection Logic

**Files:**
- Create: `internal/daemon/port_preflight.go`
- Create: `internal/daemon/port_preflight_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/daemon/port_preflight_test.go
package daemon

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/config"
)

func TestDetectPortConflicts_NoConflicts(t *testing.T) {
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{19876}, Autostart: true},
	}
	// Port 19876 should not be in use
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_WithBlocker(t *testing.T) {
	// Bind a port to simulate a blocker
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	require.Len(t, conflicts, 1)
	assert.Equal(t, "api", conflicts[0].ScriptName)
	assert.Equal(t, port, conflicts[0].Port)
	assert.Contains(t, conflicts[0].PIDs, os.Getpid())
}

func TestDetectPortConflicts_ManagedPIDSkipped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Our own PID is "managed"
	managedPIDs := map[int]bool{os.Getpid(): true}

	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, managedPIDs)
	assert.Empty(t, conflicts, "should skip managed PIDs")
}

func TestDetectPortConflicts_NoPorts(t *testing.T) {
	scripts := map[string]*config.ScriptConfig{
		"lib": {Autostart: true}, // no ports declared
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_MultiplePortsMultipleScripts(t *testing.T) {
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln1.Close()
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln2.Close()

	scripts := map[string]*config.ScriptConfig{
		"api":      {Ports: []int{ln1.Addr().(*net.TCPAddr).Port}, Autostart: true},
		"frontend": {Ports: []int{ln2.Addr().(*net.TCPAddr).Port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Len(t, conflicts, 2)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run TestDetectPortConflicts ./internal/daemon/`
Expected: FAIL — `detectPortConflicts` not defined

- [ ] **Step 3: Implement PortConflict type and detectPortConflicts**

```go
// internal/daemon/port_preflight.go
package daemon

import (
	"context"
	"sort"

	"github.com/standardbeagle/agnt/internal/config"
)

// PortConflict describes an unmanaged process blocking a declared port.
type PortConflict struct {
	ScriptName  string `json:"script_name"`
	Port        int    `json:"port"`
	PIDs        []int  `json:"pids"`
	ProcessName string `json:"process_name,omitempty"`
}

// detectPortConflicts scans all declared ports from autostart scripts and
// returns conflicts where unmanaged processes hold those ports.
// managedPIDs is a set of PIDs the daemon currently manages (skipped).
func detectPortConflicts(ctx context.Context, scripts map[string]*config.ScriptConfig, managedPIDs map[int]bool) []PortConflict {
	var conflicts []PortConflict

	// Collect script names sorted for deterministic output
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sc := scripts[name]
		for _, port := range sc.Ports {
			pids := config.FindPIDsByPort(ctx, port)
			if len(pids) == 0 {
				continue
			}

			// Filter out managed PIDs
			var unmanaged []int
			for _, pid := range pids {
				if managedPIDs != nil && managedPIDs[pid] {
					continue
				}
				unmanaged = append(unmanaged, pid)
			}
			if len(unmanaged) == 0 {
				continue
			}

			// Use first PID's name for the conflict label
			procName := config.ProcessNameByPID(unmanaged[0])

			conflicts = append(conflicts, PortConflict{
				ScriptName:  name,
				Port:        port,
				PIDs:        unmanaged,
				ProcessName: procName,
			})
		}
	}

	return conflicts
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -run TestDetectPortConflicts ./internal/daemon/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/port_preflight.go internal/daemon/port_preflight_test.go
git commit -m "feat: port conflict detection for autostart pre-flight"
```

---

### Task 3: Upgrade go-cli-server killProcesses to Use Process-Group Kill

**Files:**
- Modify: `/home/beagle/work/core/go-cli-server/process/manager.go` (lines 537-558, `killProcesses`)
- Modify: `/home/beagle/work/core/go-cli-server/process/manager_test.go`

**Context:** `KillProcessByPort` is the existing kill path used by `cleanup_port`. It currently only sends SIGTERM/SIGKILL to individual PIDs — no process groups, no descendant tree. Dev toolchains (dotnet watch, vite, webpack) spawn deep process trees where killing just the parent leaves children holding ports. All process shutdown code must use the same aggressive escalation already in `signalProcessGroup` + `cleanupProcessTree`.

- [ ] **Step 1: Write failing test in go-cli-server**

Add to `/home/beagle/work/core/go-cli-server/process/manager_test.go`:

```go
func TestKillProcessByPort_KillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group test is Unix-only")
	}

	// Start a parent process that spawns a child, both in a new process group
	port := findFreePort(t)
	script := fmt.Sprintf(
		`import socket,os,time,subprocess
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(('127.0.0.1',%d)); s.listen(1)
child=subprocess.Popen(['sleep','60'])
time.sleep(60)`, port)
	cmd := exec.Command("python3", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	parentPID := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// Wait for port to bind
	waitForPort(t, port, 5*time.Second)

	// Get child PID before killing
	children := findAllDescendants(parentPID)
	require.NotEmpty(t, children, "parent should have spawned a child")
	childPID := children[0]

	pm := NewProcessManager(DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	pids, err := pm.KillProcessByPort(context.Background(), port)
	require.NoError(t, err)
	assert.NotEmpty(t, pids)

	// Wait for processes to die
	time.Sleep(500 * time.Millisecond)

	// Both parent AND child should be dead
	assert.False(t, isProcessAlive(parentPID), "parent should be dead")
	assert.False(t, isProcessAlive(childPID), "child should be dead (process group kill)")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/beagle/work/core/go-cli-server && go test -v -run TestKillProcessByPort_KillsProcessGroup ./process/`
Expected: FAIL — child survives because `killProcesses` only signals individual PIDs

- [ ] **Step 3: Upgrade killProcesses to use process-group kill**

Replace `killProcesses` in `/home/beagle/work/core/go-cli-server/process/manager.go`:

```go
func (pm *ProcessManager) killProcesses(pids []int) []int {
	var killedPids []int

	// Phase 1: SIGTERM to process group + descendants for each PID
	for _, pid := range pids {
		pm.signalProcessGroup(pid, syscall.SIGTERM)
		killedPids = append(killedPids, pid)
	}

	// Phase 2: Wait for graceful exit
	time.Sleep(3 * time.Second)

	// Phase 3: SIGKILL escalation for survivors
	for _, pid := range pids {
		if isProcessAlive(pid) {
			cleanupProcessTree(pid)
		}
	}

	return killedPids
}
```

Note: `signalProcessGroup` is already a method on `ProcessManager` — it handles process groups, descendant trees, and tracked PID cleanup. `cleanupProcessTree` is the SIGKILL escalation that catches escapees.

On Windows, `signalProcessGroup` already uses Job Objects + `CTRL_BREAK_EVENT`, so this change works cross-platform.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/beagle/work/core/go-cli-server && go test -v -run TestKillProcessByPort ./process/`
Expected: PASS — both parent and child killed

- [ ] **Step 5: Run full go-cli-server tests**

Run: `cd /home/beagle/work/core/go-cli-server && go test ./... -count=1 -timeout 120s`
Expected: All pass

- [ ] **Step 6: Commit in go-cli-server**

```bash
cd /home/beagle/work/core/go-cli-server
git add process/manager.go process/manager_test.go
git commit -m "fix: killProcesses uses process-group kill for full descendant cleanup"
```

- [ ] **Step 7: Update go.mod in agnt to use local go-cli-server**

If not already using `replace` directive, add/verify:

Run: `grep -q 'replace.*go-cli-server' go.mod && echo "already has replace" || echo "needs replace"`

If needed, ensure `go.mod` has:
```
replace github.com/standardbeagle/go-cli-server => ../go-cli-server
```

Run: `go mod tidy`

---

### Task 3b: killPortBlockers Wrapper + Port Verification

**Files:**
- Modify: `internal/daemon/port_preflight.go`
- Modify: `internal/daemon/port_preflight_test.go`

**Context:** Now that `KillProcessByPort` in go-cli-server uses process-group kill, the agnt-side wrapper just needs to call it per conflict and verify the port is free afterward.

- [ ] **Step 1: Write the failing test**

Add to `internal/daemon/port_preflight_test.go`:

```go
func TestKillPortBlockers_FreesPort(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("don't run as root")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Start a subprocess that listens on the port
	cmd := exec.Command("python3", "-c",
		fmt.Sprintf(`import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)`, port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// Wait for port to be bound
	waitForPort(t, port, 5*time.Second)

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	conflicts := []PortConflict{{
		ScriptName: "test", Port: port, PIDs: []int{cmd.Process.Pid},
	}}

	results := killPortBlockers(context.Background(), pm, conflicts)
	require.Len(t, results, 1)
	assert.True(t, results[0].Killed)
	assert.Empty(t, results[0].Error)

	// Verify port is free
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	assert.NoError(t, err, "port should be free after kill")
	if ln2 != nil {
		ln2.Close()
	}
}

func TestKillPortBlockers_NonExistentPID(t *testing.T) {
	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	conflicts := []PortConflict{{
		ScriptName: "test", Port: 9999, PIDs: []int{999999999},
	}}
	results := killPortBlockers(context.Background(), pm, conflicts)
	require.Len(t, results, 1)
	assert.True(t, results[0].Killed)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run TestKillPortBlockers ./internal/daemon/`
Expected: FAIL — `killPortBlockers` not defined

- [ ] **Step 3: Implement killPortBlockers**

Add to `internal/daemon/port_preflight.go`:

```go
// KillResult reports what happened for each conflict.
type KillResult struct {
	PortConflict
	Killed bool   `json:"killed"`
	Error  string `json:"error,omitempty"`
}

// killPortBlockers kills processes blocking declared ports using the
// ProcessManager's full escalation path (process groups + descendants).
// Verifies each port is free after kill.
func killPortBlockers(ctx context.Context, pm *goprocess.ProcessManager, conflicts []PortConflict) []KillResult {
	results := make([]KillResult, len(conflicts))

	for i, c := range conflicts {
		results[i].PortConflict = c

		// Use ProcessManager.KillProcessByPort — this now does process-group
		// kill with SIGTERM→wait→SIGKILL escalation and descendant cleanup.
		_, err := pm.KillProcessByPort(ctx, c.Port)
		if err != nil {
			results[i].Error = fmt.Sprintf("kill failed for port %d: %v", c.Port, err)
			continue
		}

		// Verify port is actually free
		if waitPortFree(c.Port, 2*time.Second) {
			results[i].Killed = true
		} else {
			results[i].Error = fmt.Sprintf("port %d still in use after kill", c.Port)
		}
	}

	return results
}

// waitPortFree polls until port is free or timeout expires.
func waitPortFree(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -run TestKillPortBlockers ./internal/daemon/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/port_preflight.go internal/daemon/port_preflight_test.go
git commit -m "feat: killPortBlockers wrapper with port verification"
```

---

### Task 4: PortConflictPolicy Config Parsing

**Files:**
- Modify: `internal/config/agnt.go` (lines 44-49, `AgntProjectMeta` struct)
- Modify: `internal/config/agnt_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/agnt_test.go`:

```go
func TestPortConflictPolicy_Parsing(t *testing.T) {
	tests := []struct {
		name     string
		kdl      string
		expected string
	}{
		{"default when unset", `scripts { api { run "go run ." } }`, ""},
		{"prompt", `project { port-conflict "prompt" } scripts { api { run "go run ." } }`, "prompt"},
		{"auto-kill", `project { port-conflict "auto-kill" } scripts { api { run "go run ." } }`, "auto-kill"},
		{"skip", `project { port-conflict "skip" } scripts { api { run "go run ." } }`, "skip"},
		{"fail", `project { port-conflict "fail" } scripts { api { run "go run ." } }`, "fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseAgntConfig(tt.kdl)
			require.NoError(t, err)
			got := cfg.PortConflictPolicy()
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPortConflictPolicy_DefaultsToPrompt(t *testing.T) {
	cfg, err := ParseAgntConfig(`scripts { api { run "go run ." } }`)
	require.NoError(t, err)
	// EffectivePortConflictPolicy returns "prompt" when unset
	assert.Equal(t, "prompt", cfg.EffectivePortConflictPolicy())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run TestPortConflictPolicy ./internal/config/`
Expected: FAIL — `PortConflictPolicy` / `EffectivePortConflictPolicy` not defined

- [ ] **Step 3: Implement config field and methods**

Modify `internal/config/agnt.go`, update `AgntProjectMeta`:

```go
type AgntProjectMeta struct {
	Type           string `kdl:"type"`
	Name           string `kdl:"name"`
	PortConflict   string `kdl:"port-conflict"`
}
```

Add methods on `AgntConfig`:

```go
// PortConflictPolicy returns the raw port-conflict policy from config.
// Returns "" if unset.
func (c *AgntConfig) PortConflictPolicy() string {
	if c.Project == nil {
		return ""
	}
	return c.Project.PortConflict
}

// EffectivePortConflictPolicy returns the port-conflict policy, defaulting to "prompt".
func (c *AgntConfig) EffectivePortConflictPolicy() string {
	p := c.PortConflictPolicy()
	if p == "" {
		return "prompt"
	}
	return p
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v -run TestPortConflictPolicy ./internal/config/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/agnt.go internal/config/agnt_test.go
git commit -m "feat: port-conflict policy config parsing"
```

---

### Task 5: Integrate Pre-flight into RunAutostart (auto-kill, skip, fail)

**Files:**
- Modify: `internal/daemon/daemon.go` (lines 985-1046, `AutostartResult` and `RunAutostart`)
- Modify: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Extend AutostartResult**

In `internal/daemon/daemon.go`, update the struct:

```go
type AutostartResult struct {
	Scripts       []string       `json:"scripts,omitempty"`
	Proxies       []string       `json:"proxies,omitempty"`
	Errors        []string       `json:"errors,omitempty"`
	PortConflicts []PortConflict `json:"port_conflicts,omitempty"` // Unresolved (prompt mode)
	PortsCleared  []PortConflict `json:"ports_cleared,omitempty"`  // Auto-killed
}
```

- [ ] **Step 2: Write failing test for auto-kill integration**

Add to `internal/daemon/daemon_test.go`:

```go
func TestRunAutostart_PortConflict_AutoKill(t *testing.T) {
	// Create a daemon with minimal config
	d := newTestDaemon(t)
	defer d.Shutdown(context.Background())

	// Start a listener on a random port to simulate blocker
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Write .agnt.kdl with auto-kill policy and the blocked port
	projectPath := t.TempDir()
	kdlContent := fmt.Sprintf(`
project {
    port-conflict "auto-kill"
}
scripts {
    api {
        run "echo hello"
        autostart true
        ports %d
    }
}`, port)
	require.NoError(t, os.WriteFile(filepath.Join(projectPath, ".agnt.kdl"), []byte(kdlContent), 0644))

	result := d.RunAutostart(context.Background(), projectPath)

	// Our own process can't be killed, so it should show up as a kill failure
	// but the autostart should still proceed (best-effort)
	assert.NotNil(t, result)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test -v -run TestRunAutostart_PortConflict ./internal/daemon/`
Expected: FAIL or unexpected behavior (no pre-flight check yet)

- [ ] **Step 4: Implement pre-flight in RunAutostart**

In `internal/daemon/daemon.go`, modify `RunAutostart` — insert between step 2 (config load) and step 4 (script start):

```go
func (d *Daemon) RunAutostart(ctx context.Context, projectPath string) *AutostartResult {
	result := &AutostartResult{}
	log := d.startupErrorStore

	// Step 1: Validate input (unchanged)
	if projectPath == "" {
		log.Error("", "", "autostart", "projectPath is empty")
		return result
	}
	projectPath = normalizePath(projectPath)
	log.Info("", "", "autostart", fmt.Sprintf("starting autostart for %s", projectPath))

	// Step 2: Load config (unchanged)
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		log.Error("", "", "config_error", fmt.Sprintf("failed to load .agnt.kdl from %s: %v", projectPath, err))
		return result
	}
	if agntConfig == nil {
		log.Info("", "", "no_config", fmt.Sprintf("no .agnt.kdl in %s", projectPath))
		return result
	}
	log.Info("", "", "config_loaded", fmt.Sprintf("%d scripts, %d proxies from %s", len(agntConfig.Scripts), len(agntConfig.Proxies), projectPath))

	// Step 3: Port pre-flight check (NEW)
	autostartScripts := agntConfig.GetAutostartScripts()
	managedPIDs := d.collectManagedPIDs()
	conflicts := detectPortConflicts(ctx, autostartScripts, managedPIDs)

	if len(conflicts) > 0 {
		policy := agntConfig.EffectivePortConflictPolicy()

		for _, c := range conflicts {
			log.Add(&StartupLogEntry{
				ProcessID: makeProcessID(projectPath, c.ScriptName),
				ScriptName: c.ScriptName,
				Level: "warning", EventType: "port_conflict_detected",
				Message: fmt.Sprintf("port %d blocked by %s (PIDs: %v)", c.Port, c.ProcessName, c.PIDs),
				Port: c.Port, Timestamp: time.Now(),
			})
		}

		switch policy {
		case "fail":
			msg := fmt.Sprintf("port conflicts detected, aborting (port-conflict: fail): %d conflict(s)", len(conflicts))
			log.Error("", "", "port_conflict_abort", msg)
			result.Errors = append(result.Errors, msg)
			result.PortConflicts = conflicts
			return result

		case "skip":
			for _, c := range conflicts {
				log.Add(&StartupLogEntry{
					ProcessID: makeProcessID(projectPath, c.ScriptName),
					ScriptName: c.ScriptName,
					Level: "warning", EventType: "port_conflict_skipped",
					Message: fmt.Sprintf("port %d conflict skipped (policy: skip)", c.Port),
					Port: c.Port, Timestamp: time.Now(),
				})
			}
			// Fall through to register + start scripts (they'll fail on bind)

		case "auto-kill":
			killResults := killPortBlockers(ctx, d.hub.ProcessManager(), conflicts)
			for _, kr := range killResults {
				if kr.Killed {
					result.PortsCleared = append(result.PortsCleared, kr.PortConflict)
					log.Info(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
						"port_conflict_killed",
						fmt.Sprintf("cleared port %d (was: %s PIDs %v)", kr.Port, kr.ProcessName, kr.PIDs))
				} else {
					log.Error(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
						"port_conflict_failed", kr.Error)
					result.Errors = append(result.Errors, kr.Error)
				}
			}

		case "prompt":
			// Store pending state and return — scripts NOT registered or started.
			// Client will send AUTOSTART CLEAR-PORTS or AUTOSTART CONTINUE to resume.
			d.pendingAutostarts.Store(projectPath, &pendingAutostart{
				config:      agntConfig,
				projectPath: projectPath,
				conflicts:   conflicts,
			})
			result.PortConflicts = conflicts
			return result
		}
	}

	// Step 4: Register + start (factored into helper for reuse by resumeAutostart)
	d.registerAndStartScripts(ctx, agntConfig, projectPath, result)

	return result
}

// registerAndStartScripts registers all scripts, starts autostart scripts in
// dependency order, then starts proxies. Shared by RunAutostart and resumeAutostart.
func (d *Daemon) registerAndStartScripts(ctx context.Context, cfg *config.AgntConfig, projectPath string, result *AutostartResult) {
	log := d.startupErrorStore

	// Register ALL scripts (autostart and manual) so the overlay can see them
	for name, scriptCfg := range cfg.Scripts {
		processID := makeProcessID(projectPath, name)
		d.scriptConfigs.Store(processID, scriptCfg)
		if _, err := d.scriptRegistry.Register(name, projectPath, scriptConfigToEntry(scriptCfg)); err != nil {
			log.Error(processID, name, "register_failed", fmt.Sprintf("failed to register script: %v", err))
		}
	}

	// Start scripts in dependency order
	failedScripts := d.startAutostartScripts(ctx, cfg, projectPath, result)

	// Start proxies (skip those depending on failed scripts)
	d.startAutostartProxies(ctx, cfg, projectPath, failedScripts, result)
}
```

Add helper:

```go
// collectManagedPIDs returns a set of all PIDs currently managed by the daemon.
func (d *Daemon) collectManagedPIDs() map[int]bool {
	managed := make(map[int]bool)
	for _, proc := range d.hub.ProcessManager().List("") {
		pid := proc.PID()
		if pid > 0 {
			managed[pid] = true
		}
	}
	return managed
}
```

- [ ] **Step 5: Run tests to verify**

Run: `go test -v -run TestRunAutostart ./internal/daemon/ -count=1`
Expected: PASS (existing tests still pass + new test passes)

- [ ] **Step 6: Run full test suite**

Run: `go test ./internal/daemon/ -count=1 -timeout 120s`
Expected: All pass

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go internal/daemon/port_preflight.go
git commit -m "feat: integrate port pre-flight into RunAutostart (auto-kill, skip, fail)"
```

---

### Task 6: IPC Verbs for Prompt Mode

**Files:**
- Modify: `internal/protocol/commands.go` (add verb/sub-verb constants)
- Modify: `internal/daemon/hub_handlers.go` (register command + handlers)
- Modify: `internal/daemon/daemon.go` (add pending-conflict state)
- Modify: `internal/daemon/client.go` (add client methods)

**Context:** In prompt mode, `RunAutostart` returns early with `PortConflicts` populated. The daemon stores the pending autostart state (config + project path). When the client sends `AUTOSTART CLEAR-PORTS`, daemon kills blockers then resumes autostart. `AUTOSTART CONTINUE` skips killing and resumes.

- [ ] **Step 1: Add protocol constants**

In `internal/protocol/commands.go`:

```go
// Add to verb constants:
VerbAutostart = "AUTOSTART"

// Add to sub-verb constants:
SubVerbClearPorts = "CLEAR-PORTS"
SubVerbContinue   = "CONTINUE"
```

- [ ] **Step 2: Add pending autostart state to Daemon**

In `internal/daemon/daemon.go`, add a struct and field:

```go
// pendingAutostart holds state for a two-phase autostart (prompt mode).
type pendingAutostart struct {
	config      *config.AgntConfig
	projectPath string
	conflicts   []PortConflict
}
```

Add to the `Daemon` struct (find the struct definition):

```go
pendingAutostarts sync.Map // projectPath → *pendingAutostart
```

- [ ] **Step 3: Store pending state in prompt mode**

In `RunAutostart`, update the `"prompt"` case to store pending state before returning:

```go
case "prompt":
	d.pendingAutostarts.Store(projectPath, &pendingAutostart{
		config:      agntConfig,
		projectPath: projectPath,
		conflicts:   conflicts,
	})
	result.PortConflicts = conflicts
	return result
```

- [ ] **Step 4: Implement resumeAutostart helper**

Add to `internal/daemon/daemon.go`:

```go
// resumeAutostart continues a paused autostart after port conflict resolution.
func (d *Daemon) resumeAutostart(ctx context.Context, projectPath string) *AutostartResult {
	result := &AutostartResult{}

	val, ok := d.pendingAutostarts.LoadAndDelete(projectPath)
	if !ok {
		result.Errors = append(result.Errors, "no pending autostart for this project")
		return result
	}
	pending := val.(*pendingAutostart)

	// Register and start all scripts + proxies (same path as non-prompt autostart)
	d.registerAndStartScripts(ctx, pending.config, pending.projectPath, result)

	return result
}
```

- [ ] **Step 5: Register AUTOSTART command and implement handlers**

In `internal/daemon/hub_handlers.go`, add to `registerAgntCommands()`:

```go
d.hub.RegisterCommand(hubpkg.CommandDefinition{
	Verb:        protocol.VerbAutostart,
	SubVerbs:    []string{protocol.SubVerbClearPorts, protocol.SubVerbContinue},
	Description: "Resolve port conflicts and resume autostart",
	Handler:     d.hubHandleAutostart,
})
```

Update the debug log count.

Add handler implementations:

```go
func (d *Daemon) hubHandleAutostart(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case protocol.SubVerbClearPorts:
		return d.hubHandleAutostartClearPorts(conn, cmd)
	case protocol.SubVerbContinue:
		return d.hubHandleAutostartContinue(conn, cmd)
	default:
		return conn.WriteErr(hubproto.ErrUnknownCommand, fmt.Sprintf("unknown AUTOSTART sub-verb: %s", cmd.SubVerb))
	}
}

func (d *Daemon) hubHandleAutostartClearPorts(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "project path required")
	}
	projectPath := normalizePath(cmd.Args[0])

	val, ok := d.pendingAutostarts.Load(projectPath)
	if !ok {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "no pending autostart for this project")
	}
	pending := val.(*pendingAutostart)

	// Kill blockers using ProcessManager's full escalation path
	log := d.startupErrorStore
	killResults := killPortBlockers(context.Background(), d.hub.ProcessManager(), pending.conflicts)
	var cleared []PortConflict
	for _, kr := range killResults {
		if kr.Killed {
			cleared = append(cleared, kr.PortConflict)
			log.Info(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
				"port_conflict_killed",
				fmt.Sprintf("cleared port %d (was: %s PIDs %v)", kr.Port, kr.ProcessName, kr.PIDs))
		} else {
			log.Error(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
				"port_conflict_failed", kr.Error)
		}
	}

	// Resume autostart
	result := d.resumeAutostart(context.Background(), projectPath)
	result.PortsCleared = cleared

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

func (d *Daemon) hubHandleAutostartContinue(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "project path required")
	}
	projectPath := normalizePath(cmd.Args[0])

	// Log that user declined
	if val, ok := d.pendingAutostarts.Load(projectPath); ok {
		pending := val.(*pendingAutostart)
		for _, c := range pending.conflicts {
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: makeProcessID(projectPath, c.ScriptName),
				ScriptName: c.ScriptName,
				Level: "warning", EventType: "port_conflict_skipped",
				Message: fmt.Sprintf("user declined to kill port %d blocker", c.Port),
				Port: c.Port, Timestamp: time.Now(),
			})
		}
	}

	// Resume autostart without killing
	result := d.resumeAutostart(context.Background(), projectPath)

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}
```

- [ ] **Step 6: Add client methods**

In `internal/daemon/client.go`:

```go
// AutostartClearPorts kills port blockers and resumes autostart for a project.
func (c *Client) AutostartClearPorts(projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutostart, protocol.SubVerbClearPorts, projectPath).JSON()
}

// AutostartContinue resumes autostart without killing port blockers.
func (c *Client) AutostartContinue(projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutostart, protocol.SubVerbContinue, projectPath).JSON()
}
```

- [ ] **Step 7: Run full daemon tests**

Run: `go test ./internal/daemon/ -count=1 -timeout 120s`
Expected: All pass

- [ ] **Step 8: Commit**

```bash
git add internal/protocol/commands.go internal/daemon/hub_handlers.go internal/daemon/daemon.go internal/daemon/client.go
git commit -m "feat: AUTOSTART CLEAR-PORTS / CONTINUE IPC verbs for prompt mode"
```

---

### Task 7: Client-Side Prompt in displayAutostartResults

**Files:**
- Modify: `cmd/agnt/pty_common.go` (lines 25-35 struct, lines 190-245 registration, lines 274-317 display)

**Context:** The client already captures `autostart` fields from the session register response. We need to: (1) capture `port_conflicts` and `ports_cleared`, (2) display the prompt, (3) read user input, (4) send the appropriate IPC verb.

- [ ] **Step 1: Extend daemonSessionHandle**

In `cmd/agnt/pty_common.go`, add fields to the struct (around line 25-35):

```go
type daemonSessionHandle struct {
	// ... existing fields ...
	autostartScripts    []string
	autostartProxies    []string
	autostartErrors     []string
	portConflicts       []portConflictInfo // NEW
	portsCleared        []portConflictInfo // NEW
}

type portConflictInfo struct {
	ScriptName  string
	Port        int
	PIDs        []int
	ProcessName string
}
```

- [ ] **Step 2: Capture port conflicts from session register response**

In the goroutine that processes the session register result (around line 221-245), add after the existing error capture:

```go
if conflicts, ok := autostart["port_conflicts"].([]interface{}); ok {
	for _, c := range conflicts {
		if cm, ok := c.(map[string]interface{}); ok {
			info := portConflictInfo{}
			if s, ok := cm["script_name"].(string); ok { info.ScriptName = s }
			if p, ok := cm["port"].(float64); ok { info.Port = int(p) }
			if s, ok := cm["process_name"].(string); ok { info.ProcessName = s }
			if pids, ok := cm["pids"].([]interface{}); ok {
				for _, p := range pids {
					if pid, ok := p.(float64); ok { info.PIDs = append(info.PIDs, int(pid)) }
				}
			}
			handle.portConflicts = append(handle.portConflicts, info)
		}
	}
}
if cleared, ok := autostart["ports_cleared"].([]interface{}); ok {
	for _, c := range cleared {
		if cm, ok := c.(map[string]interface{}); ok {
			info := portConflictInfo{}
			if s, ok := cm["script_name"].(string); ok { info.ScriptName = s }
			if p, ok := cm["port"].(float64); ok { info.Port = int(p) }
			if s, ok := cm["process_name"].(string); ok { info.ProcessName = s }
			handle.portsCleared = append(handle.portsCleared, info)
		}
	}
}
```

- [ ] **Step 3: Add prompt display in displayAutostartResults**

Modify `displayAutostartResults` (around line 278):

```go
func displayAutostartResults(handle *daemonSessionHandle, ov *overlay.Overlay, w io.Writer, timeout time.Duration) {
	if handle == nil {
		return
	}
	if !handle.WaitRegistered(timeout) {
		return
	}

	// Show auto-cleared ports (auto-kill mode)
	for _, c := range handle.portsCleared {
		fmt.Fprintf(w, "\x1b[33m[agnt] cleared port %d (was: %s PID %v)\x1b[0m\r\n", c.Port, c.ProcessName, c.PIDs)
	}

	// Handle port conflicts (prompt mode)
	if len(handle.portConflicts) > 0 {
		fmt.Fprintf(w, "\x1b[33m[agnt] ⚠ Port conflicts detected:\x1b[0m\r\n")
		for _, c := range handle.portConflicts {
			pidStr := fmt.Sprintf("%v", c.PIDs)
			fmt.Fprintf(w, "\x1b[33m  %d (%s) ← %s (PID %s)\x1b[0m\r\n", c.Port, c.ScriptName, c.ProcessName, pidStr)
		}
		fmt.Fprintf(w, "\x1b[33m  Kill all blocking processes? [Y/n] \x1b[0m")

		// Read single character from stdin
		answer := readPromptAnswer()

		if answer == 'n' || answer == 'N' {
			fmt.Fprintf(w, "\r\n\x1b[2m[agnt] proceeding without killing — scripts may fail to bind\x1b[0m\r\n")
			if handle.IsConnected() {
				result, err := handle.client.AutostartContinue(handle.projectPath)
				if err == nil {
					mergeAutostartResult(handle, result)
				}
			}
		} else {
			fmt.Fprintf(w, "\r\n")
			if handle.IsConnected() {
				result, err := handle.client.AutostartClearPorts(handle.projectPath)
				if err == nil {
					mergeAutostartResult(handle, result)
				}
			}
		}
	}

	// Show started scripts/proxies (existing logic, unchanged)
	started := append(handle.autostartScripts, handle.autostartProxies...)
	// ... rest of existing display logic ...
}
```

Add helper:

```go
// mergeAutostartResult updates the handle with results from the resumed autostart.
func mergeAutostartResult(handle *daemonSessionHandle, result map[string]interface{}) {
	if result == nil {
		return
	}
	if scripts, ok := result["scripts"].([]interface{}); ok {
		for _, s := range scripts {
			if str, ok := s.(string); ok {
				handle.autostartScripts = append(handle.autostartScripts, str)
			}
		}
	}
	if proxies, ok := result["proxies"].([]interface{}); ok {
		for _, p := range proxies {
			if str, ok := p.(string); ok {
				handle.autostartProxies = append(handle.autostartProxies, str)
			}
		}
	}
	if errs, ok := result["errors"].([]interface{}); ok {
		for _, e := range errs {
			if str, ok := e.(string); ok {
				handle.autostartErrors = append(handle.autostartErrors, str)
			}
		}
	}
}

// readPromptAnswer reads a single character for Y/n prompt.
// Defaults to 'Y' on Enter.
func readPromptAnswer() byte {
	buf := make([]byte, 1)
	n, err := os.Stdin.Read(buf)
	if err != nil || n == 0 {
		return 'Y' // default
	}
	if buf[0] == '\n' || buf[0] == '\r' {
		return 'Y' // Enter = default yes
	}
	return buf[0]
}
```

Note: `readPromptAnswer` must account for the PTY context. The PTY is already in raw mode, so single-char read works. The read happens on the real stdin, not the child process stdin.

- [ ] **Step 4: Add projectPath to handle**

The handle needs `projectPath` for the IPC calls. Add it to the struct and populate it during setup (around line 213 where `SessionRegister` is called):

```go
handle.projectPath = cfg.ProjectPath
```

- [ ] **Step 5: Build and manually test**

Run: `make build`
Expected: Compiles without errors

Manual test: Start a process on a port, then run `agnt run` with a config that declares that port with `port-conflict "prompt"`. Verify prompt appears.

- [ ] **Step 6: Commit**

```bash
git add cmd/agnt/pty_common.go
git commit -m "feat: client-side port conflict prompt with kill/skip choice"
```

---

### Task 8: E2E Test for Port Pre-flight

**Files:**
- Modify: `internal/daemon/e2e_autostart_test.go`

**Context:** Existing e2e tests in this file use real processes and configs. Follow the pattern in `TestE2E_GracefulRestart_PortCleanupOnAutostart` (line 2249).

- [ ] **Step 1: Write e2e test for auto-kill mode**

```go
func TestE2E_AutostartPortPreflight_AutoKill(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupE2EEnv(t)
	defer env.Cleanup()

	// Start a blocker process on a known port
	port := findFreePort(t)
	blockerCmd := exec.Command("python3", "-c",
		fmt.Sprintf(`import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)`, port))
	blockerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, blockerCmd.Start())
	defer func() { _ = blockerCmd.Process.Kill(); _ = blockerCmd.Wait() }()

	// Wait for port to be bound
	waitForPort(t, port, 5*time.Second)

	// Write config with auto-kill policy
	kdlContent := fmt.Sprintf(`
project {
    port-conflict "auto-kill"
}
scripts {
    server {
        run "python3 -c \"import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)\""
        autostart true
        ports %d
    }
}`, port, port)
	require.NoError(t, os.WriteFile(filepath.Join(env.ProjectDir, ".agnt.kdl"), []byte(kdlContent), 0644))

	result := env.Daemon.RunAutostart(context.Background(), env.ProjectDir)

	// Blocker should have been killed
	assert.NotEmpty(t, result.PortsCleared, "should have cleared ports")
	assert.Contains(t, result.Scripts, "server", "script should have started")

	// Verify blocker is dead
	assert.False(t, pidAlive(blockerCmd.Process.Pid), "blocker should be dead")
}

func TestE2E_AutostartPortPreflight_Fail(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupE2EEnv(t)
	defer env.Cleanup()

	port := findFreePort(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer ln.Close()

	kdlContent := fmt.Sprintf(`
project {
    port-conflict "fail"
}
scripts {
    server {
        run "echo hello"
        autostart true
        ports %d
    }
}`, port)
	require.NoError(t, os.WriteFile(filepath.Join(env.ProjectDir, ".agnt.kdl"), []byte(kdlContent), 0644))

	result := env.Daemon.RunAutostart(context.Background(), env.ProjectDir)

	// Should have aborted — no scripts started
	assert.Empty(t, result.Scripts, "no scripts should start in fail mode")
	assert.NotEmpty(t, result.Errors, "should have error about port conflict abort")
	assert.NotEmpty(t, result.PortConflicts, "should report conflicts")
}

func TestE2E_AutostartPortPreflight_Skip(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupE2EEnv(t)
	defer env.Cleanup()

	port := findFreePort(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer ln.Close()

	kdlContent := fmt.Sprintf(`
project {
    port-conflict "skip"
}
scripts {
    server {
        run "echo skipped"
        autostart true
        ports %d
    }
}`, port)
	require.NoError(t, os.WriteFile(filepath.Join(env.ProjectDir, ".agnt.kdl"), []byte(kdlContent), 0644))

	result := env.Daemon.RunAutostart(context.Background(), env.ProjectDir)

	// Script should have been attempted (it'll succeed since "echo" doesn't bind)
	assert.Contains(t, result.Scripts, "server")
	assert.Empty(t, result.PortConflicts, "skip mode doesn't return conflicts to client")
}

func TestE2E_AutostartPortPreflight_Prompt(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupE2EEnv(t)
	defer env.Cleanup()

	port := findFreePort(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	defer ln.Close()

	kdlContent := fmt.Sprintf(`
project {
    port-conflict "prompt"
}
scripts {
    server {
        run "echo prompted"
        autostart true
        ports %d
    }
}`, port)
	require.NoError(t, os.WriteFile(filepath.Join(env.ProjectDir, ".agnt.kdl"), []byte(kdlContent), 0644))

	result := env.Daemon.RunAutostart(context.Background(), env.ProjectDir)

	// Should return conflicts without starting scripts
	assert.NotEmpty(t, result.PortConflicts, "should report conflicts")
	assert.Empty(t, result.Scripts, "should not start scripts in prompt mode")

	// Resume via CONTINUE
	resumeResult := env.Daemon.resumeAutostart(context.Background(), env.ProjectDir)
	assert.Contains(t, resumeResult.Scripts, "server", "should start after continue")
}
```

- [ ] **Step 2: Add test helpers if missing**

Check that `findFreePort` and `waitForPort` helpers exist in the test file. If not, add:

```go
func findFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func waitForPort(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d not bound within %s", port, timeout)
}
```

- [ ] **Step 3: Run the e2e tests**

Run: `go test -v -run TestE2E_AutostartPortPreflight ./internal/daemon/ -timeout 120s`
Expected: All 4 tests pass

- [ ] **Step 4: Run full test suite**

Run: `go test ./... -count=1 -timeout 120s`
Expected: All pass

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/e2e_autostart_test.go
git commit -m "test: e2e tests for port pre-flight (auto-kill, fail, skip, prompt)"
```

---

### Task 9: Ensure Proxy→Agent Message Wiring for All Start Paths

**Files:**
- Modify: `internal/daemon/hub_handlers.go` (lines 846-926, `hubHandleProxyStart`)
- Modify: `internal/daemon/proxy_events.go` (lines 43-145, `handleURLDetected`)
- Create: `internal/daemon/proxy_overlay_test.go`

**Context:** Browser messages (panel messages, sketch data, design mode) flow from proxy → overlay socket → PTY stdin. The daemon binds the overlay endpoint during proxy creation (`hubHandleProxyStart` line 907-926) via session registry lookup. But there are failure modes:
1. **Session not yet registered** when proxy starts (race during autostart)
2. **Session overlay path empty** (e.g., `agnt mcp` without `agnt run`)
3. **Session disconnected/reconnected** — overlay path may change but existing proxies keep the old one
4. **Proxy restart** — overlay endpoint must be re-bound

The fix: add a fallback that re-binds overlay endpoints when sessions register or reconnect, not just at proxy creation time. Also surface diagnostic warnings when overlay is unbound.

- [ ] **Step 1: Write failing test — proxy created before session has overlay**

```go
// internal/daemon/proxy_overlay_test.go
package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyOverlayBinding_LateSessionRegistration(t *testing.T) {
	// Create daemon with proxy but no sessions yet
	d := newTestDaemon(t)
	defer d.Shutdown(context.Background())

	// Start a proxy (no session registered yet — overlay should be unbound)
	proxyServer, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:        "test-proxy",
		TargetURL: "http://localhost:3000",
		Path:      "/test/project",
	})
	require.NoError(t, err)
	assert.False(t, proxyServer.HasOverlayEndpoint(), "overlay should not be bound yet")

	// Now register a session with an overlay endpoint
	session := &Session{
		Code:        "test-session",
		ProjectPath: "/test/project",
		OverlayPath: "/tmp/test-overlay.sock",
		Status:      SessionStatusActive,
		StartedAt:   time.Now(),
		LastSeen:    time.Now(),
	}
	require.NoError(t, d.sessionRegistry.Register(session))

	// Trigger overlay rebind (should happen on session registration)
	d.rebindProxyOverlays(session)

	// Proxy should now have the overlay endpoint
	assert.True(t, proxyServer.HasOverlayEndpoint(), "overlay should be bound after session registers")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run TestProxyOverlayBinding ./internal/daemon/`
Expected: FAIL — `rebindProxyOverlays` and `HasOverlayEndpoint` not defined

- [ ] **Step 3: Add HasOverlayEndpoint to proxy.ProxyServer**

In `internal/proxy/server.go`, add:

```go
// HasOverlayEndpoint returns true if this proxy has an overlay endpoint bound.
func (ps *ProxyServer) HasOverlayEndpoint() bool {
	return ps.overlayNotifier.IsEnabled()
}
```

- [ ] **Step 4: Implement rebindProxyOverlays on Daemon**

In `internal/daemon/hub_handlers.go` (or a new `proxy_overlay.go`):

```go
// rebindProxyOverlays updates overlay endpoints for all proxies matching the
// session's project path. Called when a session registers or reconnects,
// ensuring proxies created before the session (or during reconnect) get wired up.
func (d *Daemon) rebindProxyOverlays(session *Session) {
	if session.OverlayPath == "" {
		return
	}
	normalizedPath := normalizePath(session.ProjectPath)

	for _, p := range d.proxym.List() {
		if normalizePath(p.Path) != normalizedPath {
			continue
		}
		if !p.HasOverlayEndpoint() {
			p.SetOverlayEndpoint(session.OverlayPath)
			debug.Log("daemon", "Late-bound overlay endpoint for proxy %s from session %s: %s",
				p.ID, session.Code, session.OverlayPath)
		}
	}
}
```

- [ ] **Step 5: Call rebindProxyOverlays during session registration**

In `hubHandleSessionRegister` (line ~2440 in `hub_handlers.go`), after session is registered and autostart completes, add:

```go
// Rebind overlay endpoints for existing proxies that may have been
// created before this session registered (e.g., daemon restart, or
// proxy started via MCP tool before agnt run overlay was ready).
d.rebindProxyOverlays(session)
```

Also add it to the reconnect handler if one exists.

- [ ] **Step 6: Add diagnostic warning to proxy startup log**

In `hubHandleProxyStart`, after the overlay binding block (line 926), add a startup log entry if no overlay was found:

```go
if !proxyServer.HasOverlayEndpoint() {
	d.startupErrorStore.Add(&StartupLogEntry{
		ProcessID:  proxyID,
		ScriptName: proxyID,
		Level:      "warning",
		EventType:  "proxy_no_overlay",
		Message:    fmt.Sprintf("proxy %s has no overlay endpoint — browser messages will not reach agent (no active session for path %q)", proxyID, path),
		Timestamp:  time.Now(),
	})
}
```

- [ ] **Step 7: Run tests**

Run: `go test -v -run TestProxyOverlay ./internal/daemon/ -count=1`
Expected: PASS

- [ ] **Step 8: Run full test suite**

Run: `go test ./... -count=1 -timeout 120s`
Expected: All pass

- [ ] **Step 9: Commit**

```bash
git add internal/daemon/proxy_overlay.go internal/daemon/proxy_overlay_test.go internal/daemon/hub_handlers.go internal/proxy/server.go
git commit -m "feat: late-bind proxy overlay on session registration, diagnostic for unbound proxies"
```

---

### Task 10: Fix Panel Message Serialization in Proxylog Query

**Files:**
- Modify: `internal/tools/proxy_tools.go` (lines 954-1122, `handleProxyLogQueryRaw`; lines 1131-1258, `handleProxyLogQueryCompact`)
- Modify: `internal/tools/proxy_tools_test.go`

**Context:** Panel messages typed in the browser indicator are stored correctly in the traffic logger (`PanelMessage` struct with message, attachments, data fields). But when queried via `proxylog query`, the raw handler has **no case for `LogTypePanelMessage`** — entries come back with empty `data` and `timestamp` fields. The compact handler shows the message text but drops attachment content. This causes AI agents to see empty messages and creates confusion about what the user typed.

- [ ] **Step 1: Write failing test for raw format**

Add to `internal/tools/proxy_tools_test.go`:

```go
func TestProxyLogQueryRaw_PanelMessage(t *testing.T) {
	logger := proxy.NewTrafficLogger(100)
	logger.LogPanelMessage(proxy.PanelMessage{
		ID:        "msg-1",
		Timestamp: time.Now(),
		Message:   "please update the card styles",
		URL:       "http://localhost:5173/dashboard",
		Attachments: []proxy.PanelAttachment{{
			Type:    "element",
			Selector: ".card",
			Tag:     "div",
			Summary: "card component",
			Data:    map[string]interface{}{"classes": []string{"card", "shadow-md"}},
		}},
		RequestNotification: true,
	})

	entries := logger.Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypePanelMessage}})
	require.Len(t, entries, 1)

	result, output, err := handleProxyLogQueryRaw(entries, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, 1)

	entry := output.Entries[0]
	assert.Equal(t, "panel_message", entry.Type)
	assert.False(t, entry.Timestamp.IsZero(), "timestamp should not be zero")
	assert.Contains(t, entry.Data, "please update the card styles")
	assert.Contains(t, entry.Data, "element")
	assert.Contains(t, entry.Data, ".card")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v -run TestProxyLogQueryRaw_PanelMessage ./internal/tools/`
Expected: FAIL — entry has empty data and zero timestamp

- [ ] **Step 3: Add panel_message case to handleProxyLogQueryRaw**

In `internal/tools/proxy_tools.go`, add after the `LogTypeDiagnostic` case (line 1121):

```go
		case proxy.LogTypePanelMessage:
			if entry.PanelMessage != nil {
				data["id"] = entry.PanelMessage.ID
				data["message"] = entry.PanelMessage.Message
				data["url"] = entry.PanelMessage.URL
				data["request_notification"] = entry.PanelMessage.RequestNotification
				if len(entry.PanelMessage.Attachments) > 0 {
					attachments := make([]map[string]interface{}, len(entry.PanelMessage.Attachments))
					for j, att := range entry.PanelMessage.Attachments {
						a := map[string]interface{}{
							"type": att.Type,
						}
						if att.Selector != "" { a["selector"] = att.Selector }
						if att.Tag != "" { a["tag"] = att.Tag }
						if att.ID != "" { a["id"] = att.ID }
						if att.Text != "" { a["text"] = att.Text }
						if att.Summary != "" { a["summary"] = att.Summary }
						if att.FilePath != "" { a["file_path"] = att.FilePath }
						if att.Area != nil {
							a["area"] = map[string]interface{}{
								"x": att.Area.X, "y": att.Area.Y,
								"width": att.Area.Width, "height": att.Area.Height,
							}
						}
						if len(att.Data) > 0 { a["data"] = att.Data }
						attachments[j] = a
					}
					data["attachments"] = attachments
				}
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.PanelMessage.Timestamp,
					Data:      marshalData(data),
				}
			}
```

- [ ] **Step 4: Write test for compact format with attachments**

```go
func TestProxyLogQueryCompact_PanelMessageWithAttachments(t *testing.T) {
	logger := proxy.NewTrafficLogger(100)
	logger.LogPanelMessage(proxy.PanelMessage{
		ID:        "msg-2",
		Timestamp: time.Now(),
		Message:   "fix the button hover state",
		Attachments: []proxy.PanelAttachment{
			{Type: "screenshot", Summary: "hover state screenshot", FilePath: "/tmp/shot.png"},
			{Type: "element", Selector: ".btn-primary", Summary: "primary button"},
		},
	})

	entries := logger.Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypePanelMessage}})
	result, output, err := handleProxyLogQueryCompact(entries, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, output.Entries, 1)

	entry := output.Entries[0]
	assert.Contains(t, entry.Data, "fix the button hover state")
	assert.Contains(t, entry.Data, "2 attachments")
	assert.Contains(t, entry.Data, "screenshot")
	assert.Contains(t, entry.Data, ".btn-primary")
}
```

- [ ] **Step 5: Update compact handler to show attachment details**

In `handleProxyLogQueryCompact`, update the `LogTypePanelMessage` case (around line 1250):

```go
		case proxy.LogTypePanelMessage:
			if entry.PanelMessage != nil {
				timestamp = entry.PanelMessage.Timestamp
				parts := []string{entry.PanelMessage.Message}
				if len(entry.PanelMessage.Attachments) > 0 {
					attParts := make([]string, len(entry.PanelMessage.Attachments))
					for j, att := range entry.PanelMessage.Attachments {
						desc := att.Type
						if att.Selector != "" { desc += ":" + att.Selector }
						if att.Summary != "" { desc += " (" + att.Summary + ")" }
						if att.FilePath != "" { desc += " → " + att.FilePath }
						attParts[j] = desc
					}
					parts = append(parts, fmt.Sprintf("[%d attachments: %s]",
						len(entry.PanelMessage.Attachments), strings.Join(attParts, ", ")))
				}
				if entry.PanelMessage.URL != "" {
					parts = append(parts, "page: "+entry.PanelMessage.URL)
				}
				data = strings.Join(parts, "\n  ")
			}
```

- [ ] **Step 6: Run all proxylog tests**

Run: `go test -v -run TestProxyLog ./internal/tools/ -count=1`
Expected: All pass including new tests

- [ ] **Step 7: Commit**

```bash
git add internal/tools/proxy_tools.go internal/tools/proxy_tools_test.go
git commit -m "fix: panel message serialization in proxylog query (raw + compact)"
```

---

### Task 11: Add Missing Log Types Audit + Default Case

**Files:**
- Modify: `internal/tools/proxy_tools.go`
- Modify: `internal/tools/proxy_tools_test.go`

**Context:** The panel_message gap suggests other log types may also be missing from the query handlers. The traffic logger supports 14 types but the query handlers may not cover all of them. Add a default case that serializes unknown types generically so no future log type additions silently produce empty output.

- [ ] **Step 1: Audit all log types vs handler cases**

Check `internal/proxy/logger.go` for all `LogType*` constants and compare against cases in both `handleProxyLogQueryRaw` and `handleProxyLogQueryCompact`.

Known types from logger.go:
- `LogTypeHTTP`, `LogTypeError`, `LogTypePerformance`, `LogTypeCustom`
- `LogTypeScreenshot`, `LogTypeExecution`, `LogTypeResponse`
- `LogTypeInteraction`, `LogTypeMutation`, `LogTypePanelMessage`
- `LogTypeSketch`, `LogTypeDesignState`, `LogTypeDesignRequest`, `LogTypeDesignChat`
- `LogTypeDiagnostic`

Cross-reference against switch cases in both handlers. Add missing cases for:
- `LogTypeInteraction`
- `LogTypeMutation`
- `LogTypeSketch`
- `LogTypeDesignState`
- `LogTypeDesignRequest`
- `LogTypeDesignChat`

- [ ] **Step 2: Add default case to handleProxyLogQueryRaw**

After the last case in the switch, add:

```go
		default:
			// Generic fallback: marshal the entire LogEntry to avoid silent data loss.
			// This catches new log types that haven't been given explicit formatting.
			b, _ := json.Marshal(entry)
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: time.Now(),
				Data:      string(b),
			}
```

- [ ] **Step 3: Add default case to handleProxyLogQueryCompact**

```go
		default:
			timestamp = time.Now()
			b, _ := json.Marshal(entry)
			data = string(b)
```

- [ ] **Step 4: Add cases for sketch, design, interaction, mutation types**

For each missing type, add a proper case that extracts the relevant fields. Follow the same pattern as the existing cases. Reference the struct definitions in `logger.go` for field names.

- [ ] **Step 5: Write test verifying no log type produces empty output**

```go
func TestProxyLogQuery_NoEmptyOutput(t *testing.T) {
	// Create entries for every log type and verify none produce empty data
	logger := proxy.NewTrafficLogger(100)
	
	// Add at least one entry for each type that has a struct
	logger.LogPanelMessage(proxy.PanelMessage{Message: "test", Timestamp: time.Now()})
	// ... add entries for other types ...
	
	entries := logger.Query(proxy.LogFilter{Limit: 100})
	_, rawOutput, _ := handleProxyLogQueryRaw(entries, nil)
	for _, entry := range rawOutput.Entries {
		assert.NotEmpty(t, entry.Data, "log type %s should not have empty data", entry.Type)
		assert.False(t, entry.Timestamp.IsZero(), "log type %s should not have zero timestamp", entry.Type)
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test -v -run TestProxyLogQuery ./internal/tools/ -count=1`
Expected: All pass

- [ ] **Step 7: Commit**

```bash
git add internal/tools/proxy_tools.go internal/tools/proxy_tools_test.go
git commit -m "fix: add missing log type cases and default fallback in proxylog query"
```

---

### Task 12: Update CLAUDE.md and Design Doc

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/plans/2026-04-01-startup-port-preflight-design.md`

- [ ] **Step 1: Add port pre-flight to CLAUDE.md**

In the `RunAutostart` / autostart section of CLAUDE.md, add documentation about the new pre-flight step and the `port-conflict` config option. Also update the `AutostartResult` struct documentation.

- [ ] **Step 2: Mark design doc as implemented**

Add a status line at the top of `docs/plans/2026-04-01-startup-port-preflight-design.md`:

```markdown
**Status:** Implemented (2026-04-01)
```

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md docs/plans/2026-04-01-startup-port-preflight-design.md
git commit -m "docs: document port pre-flight check and port-conflict config"
```
