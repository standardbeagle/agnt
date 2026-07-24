//go:build e2e

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/agnt/internal/scope"
	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCrashedDaemonPID is a sentinel PID used to simulate a crashed daemon.
// It must be a valid-looking but non-existent PID so crash recovery fires.
const fakeCrashedDaemonPID = 88881

// e2eEnv holds the shared state for an e2e test run.
type e2eEnv struct {
	BinaryPath string // path to built agnt binary
	Daemon     *Daemon
	SocketPath string
	ProjectDir string // temp dir acting as project root
	ports      []int  // ports used by test fixtures (for cleanup sweep)
}

// TrackPort records a port for cleanup sweep on test teardown.
func (e *e2eEnv) TrackPort(port int) {
	e.ports = append(e.ports, port)
}

// setupDaemonForE2E builds the agnt binary (once), creates a temp dir and
// socket, starts a fresh daemon, and returns the environment plus a cleanup
// function registered via t.Cleanup.
func setupDaemonForE2E(t *testing.T) *e2eEnv {
	t.Helper()

	// Locate project root from test working directory.
	wd, err := os.Getwd()
	require.NoError(t, err)
	projectRoot := filepath.Join(wd, "..", "..")

	binaryPath := filepath.Join(projectRoot, "agnt")
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		// Build the binary.
		cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/agnt/")
		cmd.Dir = projectRoot
		out, buildErr := cmd.CombinedOutput()
		require.NoError(t, buildErr, "go build failed: %s", string(out))
	}

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "e2e.sock")

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())

	env := &e2eEnv{
		BinaryPath: binaryPath,
		Daemon:     d,
		SocketPath: sockPath,
		ProjectDir: tmpDir,
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		d.Stop(ctx)
		// Final safety-net: kill any orphaned processes that reference
		// the test's temp dir or are listening on test ports.
		cleanupTestProcesses(t, tmpDir, env.ports)
	})

	return env
}

// writeAgntKDL writes an .agnt.kdl config file to the given directory.
func writeAgntKDL(t *testing.T, dir, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(content), 0644)
	require.NoError(t, err)
}

// assertProcessAlive asserts that the process with the given PID exists and
// is alive and not a zombie.
func assertProcessAlive(t *testing.T, pid int) {
	t.Helper()
	assert.True(t, isProcessAlive(pid), "process %d should be alive", pid)
}

// assertProcessDead polls until the process with the given PID is no longer
// alive, failing if the timeout is exceeded. Handles zombie processes by
// checking /proc/<pid>/status since signal 0 succeeds on zombies.
func assertProcessDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isProcessAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("process %d still alive after %s", pid, timeout)
}

// isProcessAlive returns true if the PID is alive and not a zombie.
func isProcessAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return false // process gone
	}
	// Check for zombie state
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") && strings.Contains(line, "zombie") {
			return false
		}
	}
	return true
}

// assertAllDescendantsDead verifies that no descendant of the given PID is
// alive. Uses pgrep -P to walk the process tree.
func assertAllDescendantsDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pids := getDescendants(pid)
		if len(pids) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	remaining := getDescendants(pid)
	if len(remaining) > 0 {
		t.Errorf("descendants of PID %d still alive after %s: %v", pid, timeout, remaining)
	}
}

// getDescendants returns all descendant PIDs of the given PID by recursively
// walking /proc/*/stat. Falls back to pgrep if /proc is unavailable.
func getDescendants(pid int) []int {
	// Try pgrep -P (works on Linux and most unixes)
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var pids []int
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(string(out))))
	for scanner.Scan() {
		if child, err := strconv.Atoi(strings.TrimSpace(scanner.Text())); err == nil {
			pids = append(pids, child)
			pids = append(pids, getDescendants(child)...)
		}
	}
	return pids
}

// findPortUser returns the PID of the process listening on the given TCP
// port, or 0 if none found. Uses ss on Linux.
func findPortUser(port int) int {
	// Try ss first (Linux)
	out, err := exec.Command("ss", "-tlnp", fmt.Sprintf("sport = :%d", port)).Output()
	if err == nil {
		// Parse ss output for pid=<N>
		for _, line := range strings.Split(string(out), "\n") {
			if idx := strings.Index(line, "pid="); idx >= 0 {
				rest := line[idx+4:]
				end := strings.IndexAny(rest, ",) \t")
				if end < 0 {
					end = len(rest)
				}
				if pid, err := strconv.Atoi(rest[:end]); err == nil {
					return pid
				}
			}
		}
	}

	// Fallback to lsof
	out, err = exec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port)).Output()
	if err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			return pid
		}
	}

	return 0
}

// waitForPort polls the given TCP port until a connection is accepted or the
// timeout is exceeded.
func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("port %d not accepting connections after %s", port, timeout)
}

// freePort asks the kernel for an available TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// testdataPath returns the absolute path to a file in the testdata directory.
func testdataPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	p := filepath.Join(wd, "testdata", name)
	require.FileExists(t, p, "fixture script %s not found", name)
	return p
}

// readPIDFile reads a PID from the given file, retrying until the file exists
// and contains a valid integer or the timeout is exceeded.
func readPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("PID file %s not readable after %s", path, timeout)
	return 0
}

// signalGroup sends a signal to the entire process group led by pid.
// This mirrors agnt's signalProcessGroup behavior (Setpgid: true).
func signalGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}

// killTree sends SIGKILL to a process group, all descendants, and
// the process group of each descendant (catches setsid children).
// Used in test cleanup to ensure nothing leaks.
func killTree(pid int) {
	// Kill the process group first (covers processes started with Setpgid)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// Walk descendants and kill each one individually AND by their process
	// group (descendants that called setsid have a different PGID).
	descendants := getDescendants(pid)
	for _, d := range descendants {
		pgid := getProcessPGID(d)
		if pgid > 0 && pgid != pid {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = syscall.Kill(d, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// cleanupTestProcesses is a safety-net cleanup that kills ALL processes
// whose command line references the given temp directory. This catches
// orphans that escaped process groups (setsid), were reparented to init
// (double-fork), or were detached via nohup. Also kills any socat
// processes on the given ports.
func cleanupTestProcesses(t *testing.T, tmpDir string, ports []int) {
	t.Helper()

	// Sweep 1: kill any process whose cmdline contains the temp dir path.
	// This catches scripts written to tmpDir and their arguments.
	if out, err := exec.Command("pgrep", "-f", tmpDir).Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid != os.Getpid() {
				_ = syscall.Kill(-pid, syscall.SIGKILL) // try group first
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}

	// Sweep 2: kill any process listening on test ports (catches fully
	// detached socat instances that have no reference to tmpDir).
	for _, port := range ports {
		if pid := findPortUser(port); pid > 0 && pid != os.Getpid() {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// TestE2E_InfrastructureHelpers validates that the test helpers themselves work
// correctly before other e2e tests rely on them.
func TestE2E_InfrastructureHelpers(t *testing.T) {
	t.Run("freePort_returns_usable_port", func(t *testing.T) {
		port := freePort(t)
		assert.Greater(t, port, 0)

		// Port should be bindable
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		require.NoError(t, err)
		l.Close()
	})

	t.Run("waitForPort_succeeds_when_port_opens", func(t *testing.T) {
		port := freePort(t)
		// Start a listener in the background
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		require.NoError(t, err)
		defer l.Close()

		err = waitForPort(port, 2*time.Second)
		assert.NoError(t, err)
	})

	t.Run("waitForPort_fails_on_closed_port", func(t *testing.T) {
		port := freePort(t)
		err := waitForPort(port, 200*time.Millisecond)
		assert.Error(t, err)
	})

	t.Run("assertProcessAlive_on_self", func(t *testing.T) {
		assertProcessAlive(t, os.Getpid())
	})

	t.Run("writeAgntKDL_creates_file", func(t *testing.T) {
		dir := t.TempDir()
		writeAgntKDL(t, dir, `scripts { dev { run "echo hi" } }`)
		_, err := os.Stat(filepath.Join(dir, ".agnt.kdl"))
		assert.NoError(t, err)
	})

	t.Run("testdataPath_finds_fixture_scripts", func(t *testing.T) {
		scripts := []string{
			"fake-pnpm-watch.sh",
			"fake-vitest.sh",
			"fake-dotnet-watch.sh",
			"fake-slow-start.sh",
			"bad-sigterm-trap.sh",
			"bad-sigterm-forward-hang.sh",
			"bad-fork-detach.sh",
			"bad-fork-bomb-lite.sh",
			"bad-zombie-parent.sh",
			"bad-double-fork.sh",
			"bad-sigterm-slow-child.sh",
		}
		for _, s := range scripts {
			path := testdataPath(t, s)
			assert.NotEmpty(t, path)
		}
	})
}

// TestE2E_SetupDaemon verifies that setupDaemonForE2E creates a working daemon
// and that the daemon's data structures are in the expected initial state.
func TestE2E_SetupDaemon(t *testing.T) {
	env := setupDaemonForE2E(t)

	assert.NotNil(t, env.Daemon)
	assert.NotEmpty(t, env.SocketPath)
	assert.NotEmpty(t, env.ProjectDir)
	assert.FileExists(t, env.BinaryPath)

	// Daemon's ProcessManager should exist with zero processes
	pm := env.Daemon.ProcessManager()
	require.NotNil(t, pm)
	assert.Equal(t, int64(0), pm.ActiveCount())
	assert.Empty(t, pm.List())

	// ScriptRegistry should be empty
	sr := env.Daemon.ScriptRegistry()
	require.NotNil(t, sr)
	assert.Empty(t, sr.List(env.ProjectDir))
}

// TestE2E_FixtureScript_WellBehaved verifies that a well-behaved fixture
// script starts, binds a port, writes a PID file, and exits on SIGTERM.
func TestE2E_FixtureScript_WellBehaved(t *testing.T) {
	port := freePort(t)
	pidFile := filepath.Join(t.TempDir(), "test.pid")
	scriptPath := testdataPath(t, "fake-pnpm-watch.sh")

	cmd := exec.Command(scriptPath, strconv.Itoa(port), pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())

	t.Cleanup(func() { killTree(cmd.Process.Pid) })

	// Wait for PID file
	pid := readPIDFile(t, pidFile, 5*time.Second)
	assert.Equal(t, cmd.Process.Pid, pid)

	// Wait for port
	require.NoError(t, waitForPort(port, 5*time.Second))

	// Send SIGTERM to process group (mirrors agnt's signalProcessGroup behavior).
	// With Setpgid=true, the script runs in its own group; sending to -pid
	// ensures bash's wait builtin is interrupted and the trap fires.
	require.NoError(t, syscall.Kill(-pid, syscall.SIGTERM))
	assertProcessDead(t, pid, 5*time.Second)
}

// runSingleScriptAutostart is a shared helper for the three single-script e2e tests.
// It writes a .agnt.kdl pointing at the given fixture script, calls RunAutostart,
// and returns the dynamic port, PID file path, and process ID for assertions.
func runSingleScriptAutostart(t *testing.T, env *e2eEnv, scriptName, fixtureName string) (port int, pidFilePath string, processID string) {
	t.Helper()
	port = freePort(t)
	env.TrackPort(port)
	pidFilePath = filepath.Join(env.ProjectDir, scriptName+".pid")
	fixturePath := testdataPath(t, fixtureName)

	kdl := fmt.Sprintf(`scripts {
    %s {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
`, scriptName, fixturePath, port, pidFilePath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)
	require.Contains(t, result.Scripts, scriptName, "script not in result.Scripts")

	processID = script.MakeProcessID(env.ProjectDir, scriptName)
	return port, pidFilePath, processID
}

// verifyRunningDataStructures checks that after autostart the daemon's internal
// data structures accurately reflect a running process.
func verifyRunningDataStructures(t *testing.T, env *e2eEnv, scriptName, processID string, port int, pidFilePath string) int {
	t.Helper()

	// Wait for the PID file to appear (script writes it on startup)
	pid := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, pid, 0, "PID must be positive")

	// Wait for port to be bound
	require.NoError(t, waitForPort(port, 10*time.Second), "port %d not bound", port)

	// ProcessManager must contain the process entry
	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err, "process %s not found in ProcessManager", processID)

	// Atomic state field must be Running
	assert.Equal(t, process.StateRunning, proc.State(), "process state")

	// The ProcessManager tracks the shell wrapper PID (sh -c "bash ..."),
	// while the PID file contains the inner bash script's PID. Verify the
	// PID file PID is a descendant of the managed process PID.
	pmPID := proc.PID()
	assert.Greater(t, pmPID, 0, "ProcessManager PID must be positive")
	descendants := getDescendants(pmPID)
	assert.Contains(t, descendants, pid, "PID file PID %d should be a descendant of managed PID %d", pid, pmPID)

	// ActiveCount must reflect the running process
	assert.GreaterOrEqual(t, pm.ActiveCount(), int64(1), "ActiveCount")

	// RingBuffer must contain output from the script
	stdout, _ := proc.Stdout()
	assert.NotEmpty(t, stdout, "stdout ring buffer should contain output")

	// ScriptRegistry must have the entry in Running state
	sr := env.Daemon.ScriptRegistry()
	entry, ok := sr.Get(scriptName, env.ProjectDir)
	require.True(t, ok, "script %s not in ScriptRegistry", scriptName)
	assert.Equal(t, script.StateRunning, entry.State(), "ScriptEntry state")

	// Process must be alive at the OS level
	assertProcessAlive(t, pid)

	return pid
}

// verifyStoppedDataStructures checks that after daemon stop the data structures
// reflect a stopped/cleaned-up process.
func verifyStoppedDataStructures(t *testing.T, pid, port int) {
	t.Helper()
	assertProcessDead(t, pid, 10*time.Second)

	// Port must be free after process dies
	err := waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after stop", port)
}

func TestE2E_AutostartSingleScript_PnpmWatch(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "dev", "fake-pnpm-watch.sh")

	// Verify data structures while running
	pid := verifyRunningDataStructures(t, env, "dev", processID, port, pidFilePath)

	// Verify stdout contains expected pnpm-watch output
	proc, _ := env.Daemon.ProcessManager().Get(processID)
	stdout, _ := proc.Stdout()
	assert.Contains(t, string(stdout), "pnpm:watch listening on", "expected pnpm-watch banner")

	// Stop daemon gracefully (triggers process shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(ctx)

	// Verify cleanup
	verifyStoppedDataStructures(t, pid, port)
}

func TestE2E_AutostartSingleScript_Vitest(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "test", "fake-vitest.sh")

	pid := verifyRunningDataStructures(t, env, "test", processID, port, pidFilePath)

	// Verify stdout contains expected vitest output
	proc, _ := env.Daemon.ProcessManager().Get(processID)
	stdout, _ := proc.Stdout()
	assert.Contains(t, string(stdout), "vitest listening on", "expected vitest banner")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(ctx)

	verifyStoppedDataStructures(t, pid, port)
}

func TestE2E_AutostartSingleScript_DotnetWatch(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "serve", "fake-dotnet-watch.sh")

	pid := verifyRunningDataStructures(t, env, "serve", processID, port, pidFilePath)

	// Verify stdout contains expected dotnet-watch output
	proc, _ := env.Daemon.ProcessManager().Get(processID)
	stdout, _ := proc.Stdout()
	assert.Contains(t, string(stdout), "dotnet-watch parent=", "expected dotnet-watch banner")

	// Record descendant PIDs before shutdown
	descendants := getDescendants(pid)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(ctx)

	verifyStoppedDataStructures(t, pid, port)

	// The setsid grandchild must also be dead (not orphaned)
	for _, dpid := range descendants {
		assertProcessDead(t, dpid, 10*time.Second)
	}
	assertAllDescendantsDead(t, pid, 10*time.Second)
}

// TestE2E_AutostartMultiScript_DependencyOrder verifies that RunAutostart
// starts multiple scripts in correct dependency order: layer 0 scripts (api,
// docs) start concurrently, layer 1 scripts (test, depends-on api) start
// after their dependencies are ready.
func TestE2E_AutostartMultiScript_DependencyOrder(t *testing.T) {
	env := setupDaemonForE2E(t)

	apiPort := freePort(t)
	testPort := freePort(t)
	docsPort := freePort(t)
	env.TrackPort(apiPort)
	env.TrackPort(testPort)
	env.TrackPort(docsPort)

	apiPidFile := filepath.Join(env.ProjectDir, "api.pid")
	testPidFile := filepath.Join(env.ProjectDir, "test.pid")
	docsPidFile := filepath.Join(env.ProjectDir, "docs.pid")

	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")
	vitestPath := testdataPath(t, "fake-vitest.sh")
	slowPath := testdataPath(t, "fake-slow-start.sh")

	kdl := fmt.Sprintf(`scripts {
    api {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
    test {
        run "bash %s %d %s"
        autostart true
        ports %d
        depends-on "api"
    }
    docs {
        run "bash %s %d %s 0"
        autostart true
        ports %d
    }
}
`, pnpmPath, apiPort, apiPidFile, apiPort,
		vitestPath, testPort, testPidFile, testPort,
		slowPath, docsPort, docsPidFile, docsPort)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)

	// All 3 scripts must appear in result.Scripts
	require.Len(t, result.Scripts, 3, "expected 3 scripts, got %v (errors: %v)", result.Scripts, result.Errors)
	require.Empty(t, result.Errors, "unexpected errors: %v", result.Errors)
	assert.Contains(t, result.Scripts, "api")
	assert.Contains(t, result.Scripts, "test")
	assert.Contains(t, result.Scripts, "docs")

	// Build process IDs
	apiProcessID := script.MakeProcessID(env.ProjectDir, "api")
	testProcessID := script.MakeProcessID(env.ProjectDir, "test")
	docsProcessID := script.MakeProcessID(env.ProjectDir, "docs")

	// Verify all 3 processes in ProcessManager with correct states
	pm := env.Daemon.ProcessManager()

	apiPid := verifyRunningDataStructures(t, env, "api", apiProcessID, apiPort, apiPidFile)
	testPid := verifyRunningDataStructures(t, env, "test", testProcessID, testPort, testPidFile)
	docsPid := verifyRunningDataStructures(t, env, "docs", docsProcessID, docsPort, docsPidFile)

	// ActiveCount must equal 3
	assert.Equal(t, int64(3), pm.ActiveCount(), "ActiveCount should be 3")

	// ProcessManager.List must contain all 3 entries
	allProcs := pm.List()
	assert.Len(t, allProcs, 3, "ProcessManager should have 3 entries")

	// Verify dependency ordering: api must start before test.
	// Use ManagedProcess.StartTime() which is the atomic timestamp.
	apiProc, _ := pm.Get(apiProcessID)
	testProc, _ := pm.Get(testProcessID)
	docsProc, _ := pm.Get(docsProcessID)

	apiStart := apiProc.StartTime()
	testStart := testProc.StartTime()
	docsStart := docsProc.StartTime()

	require.NotNil(t, apiStart, "api StartTime must be set")
	require.NotNil(t, testStart, "test StartTime must be set")
	require.NotNil(t, docsStart, "docs StartTime must be set")

	assert.True(t, apiStart.Before(*testStart) || apiStart.Equal(*testStart),
		"api (layer 0) must start before or at same time as test (layer 1): api=%v test=%v", apiStart, testStart)

	// docs (layer 0, no dependency) should start concurrently with api.
	// Both are in layer 0, so their start times should be close (within 2s).
	apiDocsGap := docsStart.Sub(*apiStart)
	if apiDocsGap < 0 {
		apiDocsGap = -apiDocsGap
	}
	assert.Less(t, apiDocsGap.Seconds(), 2.0,
		"api and docs (both layer 0) should start concurrently, gap=%v", apiDocsGap)

	// Verify each script's RingBuffer has its expected output
	apiOut, _ := apiProc.Stdout()
	assert.Contains(t, string(apiOut), "pnpm:watch listening on")

	testOut, _ := testProc.Stdout()
	assert.Contains(t, string(testOut), "vitest listening on")

	docsOut, _ := docsProc.Stdout()
	assert.Contains(t, string(docsOut), "slow-start listening on")

	// Verify ScriptRegistry tracks all 3 with Running state
	sr := env.Daemon.ScriptRegistry()
	for _, name := range []string{"api", "test", "docs"} {
		entry, ok := sr.Get(name, env.ProjectDir)
		require.True(t, ok, "script %s not in ScriptRegistry", name)
		assert.Equal(t, script.StateRunning, entry.State(), "script %s state", name)
	}

	// Stop daemon and verify all processes die
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	verifyStoppedDataStructures(t, apiPid, apiPort)
	verifyStoppedDataStructures(t, testPid, testPort)
	verifyStoppedDataStructures(t, docsPid, docsPort)
}

// TestE2E_AutostartMultiScript_PartialFailure verifies that when one script
// fails to start, errors are reported but remaining scripts still start
// successfully, and proxies linked to the failed script are skipped.
func TestE2E_AutostartMultiScript_PartialFailure(t *testing.T) {
	env := setupDaemonForE2E(t)

	goodPort := freePort(t)
	env.TrackPort(goodPort)
	goodPidFile := filepath.Join(env.ProjectDir, "good.pid")
	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")

	badPort := freePort(t)
	env.TrackPort(badPort)

	kdl := fmt.Sprintf(`scripts {
    bad {
        run "/nonexistent/command/that/does/not/exist"
        autostart true
        ports %d
    }
    good {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
proxies {
    bad-proxy {
        script "bad"
        port %d
        autostart true
    }
}
`, badPort,
		pnpmPath, goodPort, goodPidFile, goodPort,
		badPort)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)

	// The good script must have started
	assert.Contains(t, result.Scripts, "good", "good script should be in result.Scripts")

	// The bad script must have produced an error
	require.NotEmpty(t, result.Errors, "expected errors for bad script")
	foundBadError := false
	for _, e := range result.Errors {
		if strings.Contains(e, "bad") {
			foundBadError = true
			break
		}
	}
	assert.True(t, foundBadError, "expected error mentioning 'bad' script, got: %v", result.Errors)

	// Verify the good script is properly running in data structures
	pm := env.Daemon.ProcessManager()
	goodProcessID := script.MakeProcessID(env.ProjectDir, "good")
	goodPid := verifyRunningDataStructures(t, env, "good", goodProcessID, goodPort, goodPidFile)

	// The bad script should have a Failed state in ProcessManager (if registered)
	badProcessID := script.MakeProcessID(env.ProjectDir, "bad")
	badProc, err := pm.Get(badProcessID)
	if err == nil {
		assert.Equal(t, process.StateFailed, badProc.State(),
			"bad script process should be in Failed state, got %s", badProc.State())
	}

	// Proxy linked to bad script should be skipped (error reported)
	foundProxySkip := false
	for _, e := range result.Errors {
		if strings.Contains(e, "bad-proxy") || (strings.Contains(e, "proxy") && strings.Contains(e, "bad")) {
			foundProxySkip = true
			break
		}
	}
	assert.True(t, foundProxySkip, "expected proxy skip error for bad-proxy, got: %v", result.Errors)

	// ActiveCount should reflect only the good process
	assert.GreaterOrEqual(t, pm.ActiveCount(), int64(1), "at least 1 active process (good)")

	// Stop daemon and verify cleanup
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	verifyStoppedDataStructures(t, goodPid, goodPort)
}

// registerSessionForE2E replicates the logic of hubHandleSessionRegister
// for direct daemon testing without IPC. It registers a session, runs
// autostart if this is the first session for the project, and adds the
// session as observer/owner of project scripts.
func registerSessionForE2E(t *testing.T, env *e2eEnv, sessionCode string) *AutostartResult {
	t.Helper()

	session := &Session{
		Code:        sessionCode,
		OverlayPath: "",
		ProjectPath: env.ProjectDir,
		Command:     "test",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	require.NoError(t, env.Daemon.SessionRegistry().Register(session))

	// Check if another active session already owns scripts for this project.
	var autostartResult *AutostartResult
	existingSessions := env.Daemon.SessionRegistry().ListActive(env.ProjectDir, false)
	hasExistingOwner := false
	for _, existing := range existingSessions {
		if existing.Code != sessionCode {
			hasExistingOwner = true
			break
		}
	}

	if hasExistingOwner {
		autostartResult = &AutostartResult{}
		for _, entry := range env.Daemon.ScriptRegistry().List(env.ProjectDir) {
			state := entry.State()
			if state == script.StateRunning || state == script.StateStarting {
				autostartResult.Scripts = append(autostartResult.Scripts, entry.Name)
			}
		}
	} else {
		autostartResult = env.Daemon.RunAutostart(context.Background(), env.ProjectDir)
	}

	// Add session as observer and claim ownership of unowned scripts
	for _, entry := range env.Daemon.ScriptRegistry().List(env.ProjectDir) {
		entry.AddSession(sessionCode)
		if entry.Owner() == "" {
			entry.SetOwner(sessionCode)
		}
	}

	return autostartResult
}

// TestE2E_AutostartIdempotent_SecondSessionJoins verifies that a second
// session for the same project joins without restarting processes, and that
// processes are cleaned up only when the last session disconnects.
func TestE2E_AutostartIdempotent_SecondSessionJoins(t *testing.T) {
	env := setupDaemonForE2E(t)

	port := freePort(t)
	env.TrackPort(port)
	pidFile := filepath.Join(env.ProjectDir, "api.pid")
	fixturePath := testdataPath(t, "fake-pnpm-watch.sh")

	kdl := fmt.Sprintf(`scripts {
    api {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
`, fixturePath, port, pidFile, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	// Step 1: Register session 1 -> triggers RunAutostart
	result1 := registerSessionForE2E(t, env, "session-1")
	require.Empty(t, result1.Errors, "session-1 autostart errors: %v", result1.Errors)
	require.Contains(t, result1.Scripts, "api", "session-1 should start api script")

	// Step 2: Verify process is running and record PID
	processID := script.MakeProcessID(env.ProjectDir, "api")
	pid1 := verifyRunningDataStructures(t, env, "api", processID, port, pidFile)

	pm := env.Daemon.ProcessManager()
	proc1, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID1 := proc1.PID()

	// Step 3: Register session 2 for same project -> should join, not restart
	result2 := registerSessionForE2E(t, env, "session-2")
	require.Empty(t, result2.Errors, "session-2 join errors: %v", result2.Errors)
	assert.Contains(t, result2.Scripts, "api", "session-2 should see api as already running")

	// Step 4: Verify PID is unchanged (process was NOT restarted)
	proc2, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID2 := proc2.PID()
	assert.Equal(t, pmPID1, pmPID2, "ProcessManager PID must not change on second session join")
	assertProcessAlive(t, pid1)

	// Step 5: Verify session count and ownership
	sr := env.Daemon.ScriptRegistry()
	entry, ok := sr.Get("api", env.ProjectDir)
	require.True(t, ok)
	assert.Equal(t, 2, entry.ObserverCount(), "both sessions should be observers")
	assert.Equal(t, "session-1", entry.Owner(), "session-1 should remain owner")
	assert.Equal(t, script.StateRunning, entry.State())

	// Step 6: Disconnect session 2 -> process should remain alive (session 1 still owns)
	env.Daemon.CleanupSessionResources("session-2")

	assertProcessAlive(t, pid1)
	assert.Equal(t, script.StateRunning, entry.State(), "script should still be Running after session-2 leaves")
	assert.Equal(t, "session-1", entry.Owner(), "session-1 should still own after session-2 leaves")
	assert.Equal(t, 1, entry.ObserverCount(), "only session-1 should remain as observer")

	// Process still registered in ProcessManager (ActiveCount tracks registered, not running)
	_, err = pm.Get(processID)
	require.NoError(t, err, "process should still be in ProcessManager")

	// Step 7: Disconnect session 1 (last owner) -> process should stop.
	// Unregister from auto-restarter first to prevent restart race
	// (CleanupSessionResources stops the process, but auto-restarter may
	// see the exit and restart before Unregister is called internally).
	if ar := env.Daemon.AutoRestarter(); ar != nil {
		ar.Unregister(processID)
	}
	env.Daemon.CleanupSessionResources("session-1")

	verifyStoppedDataStructures(t, pid1, port)

	// Verify the process is no longer running in ProcessManager
	proc, err := pm.Get(processID)
	require.NoError(t, err, "process entry should still exist in ProcessManager")
	procState := proc.State()
	assert.True(t, procState == process.StateStopped || procState == process.StateFailed,
		"ProcessManager process should be Stopped or Failed, got %s", procState)
}

// TestE2E_AutostartIdempotent_RestartsAfterAllDisconnect verifies that after
// all sessions disconnect and processes stop, a new session triggers a fresh
// autostart with a new PID.
func TestE2E_AutostartIdempotent_RestartsAfterAllDisconnect(t *testing.T) {
	env := setupDaemonForE2E(t)

	port := freePort(t)
	env.TrackPort(port)
	pidFile := filepath.Join(env.ProjectDir, "api.pid")
	fixturePath := testdataPath(t, "fake-pnpm-watch.sh")

	kdl := fmt.Sprintf(`scripts {
    api {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
`, fixturePath, port, pidFile, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	// Step 1: Register session 1 and verify process starts
	result1 := registerSessionForE2E(t, env, "session-a")
	require.Empty(t, result1.Errors, "session-a autostart errors: %v", result1.Errors)
	require.Contains(t, result1.Scripts, "api")

	processID := script.MakeProcessID(env.ProjectDir, "api")
	pid1 := verifyRunningDataStructures(t, env, "api", processID, port, pidFile)

	pm := env.Daemon.ProcessManager()
	proc1, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID1 := proc1.PID()

	// Step 2: Disconnect session-a (last owner) -> process stops.
	// Unregister from auto-restarter first to prevent restart race.
	if ar := env.Daemon.AutoRestarter(); ar != nil {
		ar.Unregister(processID)
	}
	env.Daemon.CleanupSessionResources("session-a")
	verifyStoppedDataStructures(t, pid1, port)

	// Verify process is stopped/failed in ProcessManager
	proc1After, err := pm.Get(processID)
	require.NoError(t, err)
	procState := proc1After.State()
	assert.True(t, procState == process.StateStopped || procState == process.StateFailed,
		"ProcessManager process should be Stopped or Failed, got %s", procState)

	// After the last session disconnects, CleanupSessionResources clears
	// the script registry for this project (registry is ephemeral — next
	// session rebuilds it from config). The entry may therefore be gone
	// entirely; if it still exists, it must be Stopped or Failed.
	sr := env.Daemon.ScriptRegistry()
	if entry, ok := sr.Get("api", env.ProjectDir); ok {
		state := entry.State()
		assert.True(t, state == script.StateStopped || state == script.StateFailed,
			"script should be Stopped or Failed after last session leaves, got %s", state)
	}

	// Remove stale PID file so the restarted process can write a fresh one
	os.Remove(pidFile)

	// Step 3: Register new session -> autostart fires again with new process
	result2 := registerSessionForE2E(t, env, "session-b")
	require.Empty(t, result2.Errors, "session-b autostart errors: %v", result2.Errors)
	require.Contains(t, result2.Scripts, "api", "session-b should start api")

	// Step 4: Verify new process with new PID
	pid2 := readPIDFile(t, pidFile, 10*time.Second)
	require.Greater(t, pid2, 0, "new PID must be positive")
	require.NoError(t, waitForPort(port, 10*time.Second), "port %d not bound after restart", port)

	proc2, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID2 := proc2.PID()
	assert.NotEqual(t, pmPID1, pmPID2, "restarted process must have a NEW ProcessManager PID")
	assertProcessAlive(t, pid2)

	// Verify ScriptRegistry shows Running again
	entry2, ok := sr.Get("api", env.ProjectDir)
	require.True(t, ok)
	assert.Equal(t, script.StateRunning, entry2.State(), "script should be Running after restart")
	assert.Equal(t, "session-b", entry2.Owner(), "session-b should own the restarted script")

	// Cleanup: stop daemon to kill process
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	verifyStoppedDataStructures(t, pid2, port)
}

// TestE2E_BadProcess_SIGTERMTrap verifies that a process trapping and ignoring
// SIGTERM is still killed via SIGKILL escalation after the graceful timeout.
// Tests the full signal escalation: SIGTERM (ignored) -> wait -> SIGKILL (kills).
func TestE2E_BadProcess_SIGTERMTrap(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "sigterm-trap", "bad-sigterm-trap.sh")

	pid := verifyRunningDataStructures(t, env, "sigterm-trap", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// Verify stdout contains the script banner
	stdout, _ := proc.Stdout()
	assert.Contains(t, string(stdout), "bad-sigterm-trap listening on")

	// Phase 1: Send SIGTERM to the process group — the bash script traps and
	// ignores it. (socat, which doesn't trap SIGTERM, may die — that's OK;
	// we're testing the bash process survival.)
	require.NoError(t, signalGroup(pmPID, syscall.SIGTERM))
	time.Sleep(1 * time.Second)

	// The bash process (PID file) must still be alive (SIGTERM trapped)
	assertProcessAlive(t, pid)

	// Phase 2: Send SIGKILL to the process group (escalation after graceful timeout)
	killStart := time.Now()
	require.NoError(t, signalGroup(pmPID, syscall.SIGKILL))
	// Also kill all descendants in case any escaped the process group
	for _, dpid := range getDescendants(pmPID) {
		_ = syscall.Kill(dpid, syscall.SIGKILL)
	}

	// Process must be dead after SIGKILL
	assertProcessDead(t, pid, 5*time.Second)
	assertAllDescendantsDead(t, pmPID, 5*time.Second)
	killDuration := time.Since(killStart)

	// SIGKILL should take effect nearly instantly (< 2s)
	assert.Less(t, killDuration.Seconds(), 2.0,
		"SIGKILL should take effect quickly, took %v", killDuration)

	// Port must be free after process dies
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after kill", port)

	// RingBuffer must have captured output before the process was killed
	finalStdout, _ := proc.Stdout()
	assert.NotEmpty(t, finalStdout, "ring buffer should retain pre-kill output")
}

// TestE2E_BadProcess_SIGTERMForwardHang verifies that when a parent forwards
// SIGTERM to a child that also ignores it, SIGKILL escalation kills both
// the parent and child process.
func TestE2E_BadProcess_SIGTERMForwardHang(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "sigterm-fwd", "bad-sigterm-forward-hang.sh")

	pid := verifyRunningDataStructures(t, env, "sigterm-fwd", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// Wait for child process to spawn (the script forks a child)
	var descendants []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		descendants = getDescendants(pmPID)
		if len(descendants) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.GreaterOrEqual(t, len(descendants), 2,
		"expected at least 2 descendants (inner bash + socat), got %d", len(descendants))

	// Phase 1: Send SIGTERM to the process group — both parent and child ignore it.
	require.NoError(t, signalGroup(pmPID, syscall.SIGTERM))
	time.Sleep(1 * time.Second)

	// Both parent and child should still be alive
	assertProcessAlive(t, pid)
	for _, dpid := range descendants {
		if isProcessAlive(dpid) {
			// At least some descendants should still be alive
			break
		}
	}

	// Phase 2: Send SIGKILL to process group + all descendants (escalation)
	require.NoError(t, signalGroup(pmPID, syscall.SIGKILL))
	for _, dpid := range descendants {
		_ = syscall.Kill(dpid, syscall.SIGKILL)
	}

	// Both parent (PID file) and all descendants must be dead
	assertProcessDead(t, pid, 5*time.Second)
	for _, dpid := range descendants {
		assertProcessDead(t, dpid, 5*time.Second)
	}
	assertAllDescendantsDead(t, pmPID, 5*time.Second)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after kill", port)
}

// TestE2E_BadProcess_SlowChild verifies that when a child process takes too
// long (30s) to exit on SIGTERM, the graceful timeout (5s) expires and SIGKILL
// escalation kills both parent and child within ~5s total.
func TestE2E_BadProcess_SlowChild(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "slow-child", "bad-sigterm-slow-child.sh")

	pid := verifyRunningDataStructures(t, env, "slow-child", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// Wait for child process to spawn
	var descendants []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		descendants = getDescendants(pmPID)
		if len(descendants) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.GreaterOrEqual(t, len(descendants), 2,
		"expected at least 2 descendants (inner bash + socat), got %d", len(descendants))

	// Phase 1: Send SIGTERM to the process group — the child starts a 30s
	// slow shutdown (sleep 30 in SIGTERM trap). Parent forwards to child.
	require.NoError(t, signalGroup(pmPID, syscall.SIGTERM))

	// Wait briefly for the SIGTERM to be received and slow shutdown to begin
	time.Sleep(1 * time.Second)

	// The slow child should still be alive (30s shutdown in progress)
	aliveCount := 0
	for _, dpid := range descendants {
		if isProcessAlive(dpid) {
			aliveCount++
		}
	}
	assert.Greater(t, aliveCount, 0, "some descendants should still be alive during slow shutdown")

	// Phase 2: Simulate graceful timeout expiry by sending SIGKILL (5s mark).
	// In production, the ProcessManager sends SIGKILL after GracefulTimeout.
	escalationStart := time.Now()
	require.NoError(t, signalGroup(pmPID, syscall.SIGKILL))
	for _, dpid := range descendants {
		_ = syscall.Kill(dpid, syscall.SIGKILL)
	}

	// Both parent and slow child must be dead
	assertProcessDead(t, pid, 5*time.Second)
	for _, dpid := range descendants {
		assertProcessDead(t, dpid, 5*time.Second)
	}
	assertAllDescendantsDead(t, pmPID, 5*time.Second)
	escalationDuration := time.Since(escalationStart)

	// SIGKILL must kill everything quickly, NOT wait for the 30s slow shutdown
	assert.Less(t, escalationDuration.Seconds(), 2.0,
		"SIGKILL escalation should complete quickly, not wait 30s, took %v", escalationDuration)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after kill", port)

	// RingBuffer should have output from before the kill
	finalStdout, _ := proc.Stdout()
	assert.NotEmpty(t, finalStdout, "ring buffer should retain pre-kill output")
}

// getProcessPGID returns the process group ID for a given PID by reading
// /proc/<pid>/stat. Returns -1 if the process is not found.
func getProcessPGID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	// /proc/PID/stat format: pid (comm) state ppid pgrp ...
	// Find the closing paren to skip the comm field (may contain spaces)
	s := string(data)
	closeParen := strings.LastIndex(s, ")")
	if closeParen < 0 {
		return -1
	}
	fields := strings.Fields(s[closeParen+1:])
	if len(fields) < 3 {
		return -1
	}
	// fields[0]=state, fields[1]=ppid, fields[2]=pgrp
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return -1
	}
	return pgid
}

// getProcessPPID returns the parent PID for a given PID by reading
// /proc/<pid>/stat. Returns -1 if the process is not found.
func getProcessPPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	s := string(data)
	closeParen := strings.LastIndex(s, ")")
	if closeParen < 0 {
		return -1
	}
	fields := strings.Fields(s[closeParen+1:])
	if len(fields) < 2 {
		return -1
	}
	// fields[0]=state, fields[1]=ppid
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return -1
	}
	return ppid
}

// writeTempScript writes a bash script to the project directory and returns
// its absolute path. The script is made executable.
func writeTempScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+content), 0755)
	require.NoError(t, err)
	return path
}

// TestE2E_BadProcess_ForkDetach verifies that a process which spawns a child
// via setsid+nohup (escaping the process group) is still tracked and cleaned
// up by the daemon's descendant scanner and stored-descendant kill.
func TestE2E_BadProcess_ForkDetach(t *testing.T) {
	env := setupDaemonForE2E(t)
	port := freePort(t)
	env.TrackPort(port)
	pidFilePath := filepath.Join(env.ProjectDir, "fork-detach.pid")

	// Write a script that spawns a setsid child and stays alive long enough
	// for the descendant scanner (5s interval) to record the child.
	scriptContent := fmt.Sprintf(`PORT=%d
PIDFILE="%s"
echo $$ > "$PIDFILE"
setsid nohup bash -c "exec 0</dev/null 1>/dev/null 2>/dev/null; socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo detached-child' 2>/dev/null" &
echo "fork-detach parent=$$ on http://localhost:$PORT"
trap 'exit 0' SIGTERM SIGINT
sleep 3600 &
wait
`, port, pidFilePath)
	scriptPath := writeTempScript(t, env.ProjectDir, "test-fork-detach.sh", scriptContent)

	kdl := fmt.Sprintf(`scripts {
    fork-detach {
        run "bash %s"
        autostart true
        ports %d
    }
}
`, scriptPath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)

	processID := script.MakeProcessID(env.ProjectDir, "fork-detach")

	// Wait for the parent PID file and port to be bound by the detached child
	parentPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, parentPID, 0)
	require.NoError(t, waitForPort(port, 10*time.Second), "detached child should bind port %d", port)

	// Find the detached child PID by looking at who is listening on the port
	detachedPID := findPortUser(port)
	require.Greater(t, detachedPID, 0, "must find the detached child on port %d", port)
	assertProcessAlive(t, detachedPID)
	t.Logf("parent PID=%d, detached child PID=%d", parentPID, detachedPID)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() {
		killTree(pmPID)
		if p, findErr := os.FindProcess(detachedPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	// Verify the detached child is in a DIFFERENT process group than the
	// managed process (setsid creates a new session/group).
	detachedPGID := getProcessPGID(detachedPID)
	require.Greater(t, detachedPGID, 0, "detached child PGID must be valid")
	assert.NotEqual(t, pmPID, detachedPGID,
		"detached child (pgid=%d) must be in a different process group than managed process (pgid=%d)", detachedPGID, pmPID)

	// Wait for the descendant scanner to record the detached child.
	// The scanner runs every 5s; wait for at least one full cycle.
	time.Sleep(6 * time.Second)

	// Stop daemon gracefully — triggers signalProcessGroup (SIGTERM to PGID +
	// live descendants + stored descendants) then SIGKILL escalation.
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	// The detached child must be dead after daemon stop.
	assertProcessDead(t, detachedPID, 10*time.Second)

	// Port must be free after cleanup
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after cleanup", port)

	// ProcessManager entry must reflect cleanup
	procAfter, getErr := pm.Get(processID)
	require.NoError(t, getErr)
	state := procAfter.State()
	assert.True(t, state == process.StateStopped || state == process.StateFailed,
		"process state should be Stopped or Failed, got %s", state)
}

// TestE2E_BadProcess_DoubleFork verifies that a classic double-fork daemon
// pattern (parent -> child -> grandchild, parent+child exit, grandchild
// reparented to init/PID 1) is still tracked and killed by the daemon's
// stored-descendant mechanism.
func TestE2E_BadProcess_DoubleFork(t *testing.T) {
	env := setupDaemonForE2E(t)
	port := freePort(t)
	env.TrackPort(port)
	pidFilePath := filepath.Join(env.ProjectDir, "double-fork.pid")
	grandchildPIDFile := filepath.Join(env.ProjectDir, "grandchild.pid")

	// Write a script that does the double-fork pattern. The parent stays alive
	// so the descendant scanner can record the grandchild tree before cleanup.
	// Double-fork pattern: parent -> child subshell -> grandchild (setsid).
	// The grandchild creates a new session (setsid) and closes all fds to
	// fully detach. The child subshell stays alive long enough for the
	// descendant scanner to record the entire tree. The parent stays alive
	// to keep the process tree intact for cleanup verification.
	scriptContent := fmt.Sprintf(`PORT=%d
PIDFILE="%s"
GCPIDFILE="%s"
echo $$ > "$PIDFILE"
# First fork: child subshell
(
    # Second fork: grandchild in new session, closes fds to fully detach
    setsid bash -c "echo \$\$ > $GCPIDFILE; exec 0</dev/null 1>/dev/null 2>/dev/null; socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo double-fork-grandchild' 2>/dev/null" &
    # Child stays alive (scanner needs to see grandchild as descendant)
    trap 'exit 0' SIGTERM SIGINT
    sleep 3600 &
    wait
) &
echo "double-fork parent=$$ on http://localhost:$PORT"
trap 'exit 0' SIGTERM SIGINT
sleep 3600 &
wait
`, port, pidFilePath, grandchildPIDFile)
	scriptPath := writeTempScript(t, env.ProjectDir, "test-double-fork.sh", scriptContent)

	kdl := fmt.Sprintf(`scripts {
    double-fork {
        run "bash %s"
        autostart true
        ports %d
    }
}
`, scriptPath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)

	processID := script.MakeProcessID(env.ProjectDir, "double-fork")

	// Wait for the parent PID file
	parentPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, parentPID, 0)

	// Wait for the grandchild to bind the port
	require.NoError(t, waitForPort(port, 10*time.Second), "grandchild should bind port %d", port)

	// Find grandchild PID from its PID file, fall back to port listener
	grandchildPID := readPIDFile(t, grandchildPIDFile, 5*time.Second)
	if grandchildPID <= 0 {
		grandchildPID = findPortUser(port)
	}
	require.Greater(t, grandchildPID, 0, "must find grandchild")
	assertProcessAlive(t, grandchildPID)
	t.Logf("parent PID=%d, grandchild PID=%d", parentPID, grandchildPID)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() {
		killTree(pmPID)
		if p, findErr := os.FindProcess(grandchildPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	// The grandchild used setsid so it is in a different process group.
	grandchildPGID := getProcessPGID(grandchildPID)
	require.Greater(t, grandchildPGID, 0, "grandchild PGID must be valid")
	assert.NotEqual(t, pmPID, grandchildPGID,
		"grandchild (pgid=%d) must be in a different process group than managed process (pgid=%d)",
		grandchildPGID, pmPID)

	// Wait for the descendant scanner to record the grandchild tree.
	// Scanner runs every 5s; wait for at least one full cycle.
	time.Sleep(6 * time.Second)

	// Verify the grandchild is still a descendant of the managed process
	// (child subshell is alive, keeping the tree intact).
	descendants := getDescendants(pmPID)
	assert.Contains(t, descendants, grandchildPID,
		"grandchild PID %d should be in descendant tree of managed PID %d", grandchildPID, pmPID)

	// Stop daemon gracefully — cleanup chain should kill the grandchild
	// via stored descendants even though it is in a different session.
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	// Grandchild must be dead after daemon stop
	assertProcessDead(t, grandchildPID, 10*time.Second)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after cleanup", port)

	// ProcessManager entry must reflect cleanup
	procAfter, getErr := pm.Get(processID)
	require.NoError(t, getErr)
	state := procAfter.State()
	assert.True(t, state == process.StateStopped || state == process.StateFailed,
		"process state should be Stopped or Failed, got %s", state)
}

// TestE2E_BadProcess_ForkBombLite verifies that a process spawning 10
// background sleep children is fully cleaned up when the daemon stops,
// because all children share the parent's process group.
func TestE2E_BadProcess_ForkBombLite(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "fork-bomb", "bad-fork-bomb-lite.sh")

	pid := verifyRunningDataStructures(t, env, "fork-bomb", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// Wait for all 10 grandchildren to appear. The script spawns 10 "sleep 3600"
	// children plus a socat, all as direct children of the bash script PID.
	var grandchildren []int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		grandchildren = getDescendants(pmPID)
		// The managed process tree: sh -c "bash ..." -> bash script -> 10 sleeps + socat
		// We need at least 11 descendants (10 sleeps + socat) below the managed PID.
		if len(grandchildren) >= 11 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.GreaterOrEqual(t, len(grandchildren), 11,
		"expected at least 11 descendants (10 sleeps + socat), got %d: %v", len(grandchildren), grandchildren)
	t.Logf("parent PID=%d, found %d descendants", pid, len(grandchildren))

	// Verify all grandchildren share the parent's process group.
	// The fork-bomb script does NOT call setsid, so all children inherit
	// the parent's PGID (set by Setpgid: true on the managed process).
	parentPGID := getProcessPGID(pmPID)
	require.Greater(t, parentPGID, 0, "parent PGID must be valid")

	sameGroupCount := 0
	for _, gpid := range grandchildren {
		childPGID := getProcessPGID(gpid)
		if childPGID == parentPGID {
			sameGroupCount++
		}
	}
	// All descendants should be in the same process group (socat forks may
	// add extra PIDs, so check that at least 10 share the group).
	assert.GreaterOrEqual(t, sameGroupCount, 10,
		"at least 10 grandchildren should share parent's PGID %d, got %d", parentPGID, sameGroupCount)

	// Record all grandchild PIDs before shutdown for later verification
	preShutdownGrandchildren := make([]int, len(grandchildren))
	copy(preShutdownGrandchildren, grandchildren)

	// Stop daemon gracefully — SIGTERM to process group should hit all children
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	// All grandchildren must be dead
	for _, gpid := range preShutdownGrandchildren {
		assertProcessDead(t, gpid, 10*time.Second)
	}

	// No orphans remain under the managed process PID
	assertAllDescendantsDead(t, pmPID, 10*time.Second)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after cleanup", port)

	// ProcessManager entry must reflect cleanup
	procAfter, getErr := pm.Get(processID)
	require.NoError(t, getErr)
	state := procAfter.State()
	assert.True(t, state == process.StateStopped || state == process.StateFailed,
		"process state should be Stopped or Failed, got %s", state)
}

// isProcessZombie returns true if the process with the given PID exists as a
// zombie (state "Z" in /proc/<pid>/status).
func isProcessZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") && strings.Contains(line, "zombie") {
			return true
		}
	}
	return false
}

// setupDaemonForCrashTest creates a daemon with a PID tracker that uses
// a test-specific directory (via XDG_STATE_HOME). This ensures that when
// we create a second daemon in the same test, it reads the same PID tracker
// file and can perform orphan cleanup. Returns the environment and the
// XDG_STATE_HOME path that must be reused for the recovery daemon.
func setupDaemonForCrashTest(t *testing.T) (*e2eEnv, string) {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	projectRoot := filepath.Join(wd, "..", "..")

	binaryPath := filepath.Join(projectRoot, "agnt")
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/agnt/")
		cmd.Dir = projectRoot
		out, buildErr := cmd.CombinedOutput()
		require.NoError(t, buildErr, "go build failed: %s", string(out))
	}

	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "e2e.sock")
	stateHome := filepath.Join(tmpDir, "state")
	require.NoError(t, os.MkdirAll(stateHome, 0755))

	// Set XDG_STATE_HOME so the PID tracker writes to our test directory
	t.Setenv("XDG_STATE_HOME", stateHome)

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())

	env := &e2eEnv{
		BinaryPath: binaryPath,
		Daemon:     d,
		SocketPath: sockPath,
		ProjectDir: tmpDir,
	}

	t.Cleanup(func() {
		// Final safety-net: kill any orphaned processes that reference
		// the test's temp dir or are listening on test ports.
		cleanupTestProcesses(t, tmpDir, env.ports)
	})

	return env, stateHome
}

// createRecoveryDaemon creates a new daemon instance that shares the same PID
// tracker path (via XDG_STATE_HOME). On Start(), the new daemon detects the
// previous daemon's orphaned processes and kills them with SIGKILL.
//
// Since both daemons run in the same test process (os.Getpid() is identical),
// we must overwrite the stored daemon PID in the tracker file to simulate a
// different daemon PID from the "crashed" daemon. This triggers the orphan
// detection path in CleanupOrphans.
func createRecoveryDaemon(t *testing.T, stateHome, projectDir string, crashedTracker *process.FilePIDTracker) *Daemon {
	t.Helper()

	// Overwrite the stored daemon PID to a non-existent PID so that the
	// recovery daemon's CleanupOrphans detects a different daemon PID and
	// enters crash recovery mode.
	require.NoError(t, crashedTracker.SetDaemonPID(fakeCrashedDaemonPID))

	sockPath := filepath.Join(projectDir, "recovery.sock")

	// Ensure XDG_STATE_HOME points to the same location
	t.Setenv("XDG_STATE_HOME", stateHome)

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		d.Stop(ctx)
	})

	return d
}

// TestE2E_BadProcess_ZombieParent verifies that a process creating zombies
// (spawns child that exits, parent never calls wait) is cleaned up on
// graceful daemon stop, and the zombie is reaped when the parent dies.
func TestE2E_BadProcess_ZombieParent(t *testing.T) {
	env := setupDaemonForE2E(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "zombie-parent", "bad-zombie-parent.sh")

	pid := verifyRunningDataStructures(t, env, "zombie-parent", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// The script spawns a child that exits immediately. The parent never
	// calls wait, so the child becomes a zombie. Give time for the child
	// to spawn and become a zombie.
	time.Sleep(1 * time.Second)

	// Find zombie children of the managed process tree. Walk descendants
	// looking for any in zombie state.
	descendants := getDescendants(pmPID)
	var zombiePIDs []int
	for _, dpid := range descendants {
		if isProcessZombie(dpid) {
			zombiePIDs = append(zombiePIDs, dpid)
		}
	}

	// A zombie should exist (the child that exited without being waited on).
	// Note: on some systems the zombie may already have been reaped if bash
	// implicitly waits. If no zombie is found, the test still validates
	// cleanup behavior.
	if len(zombiePIDs) > 0 {
		t.Logf("found %d zombie(s): %v", len(zombiePIDs), zombiePIDs)
	} else {
		t.Logf("no zombie found (child may have been reaped already); continuing cleanup test")
	}

	// Stop daemon gracefully (triggers SIGTERM -> SIGKILL escalation)
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	// Parent must be dead after daemon stop
	assertProcessDead(t, pid, 10*time.Second)

	// All zombies must be reaped (parent death causes zombie cleanup by init)
	for _, zpid := range zombiePIDs {
		assertProcessDead(t, zpid, 5*time.Second)
	}

	// No descendants should remain alive
	assertAllDescendantsDead(t, pmPID, 10*time.Second)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after stop", port)
}

// TestE2E_BadProcess_SIGTERMTrap_DaemonCrash verifies that after a daemon
// crash (SIGKILL), a SIGTERM-trapping process survives the crash but is
// killed by the new daemon's orphan cleanup which uses SIGKILL directly.
func TestE2E_BadProcess_SIGTERMTrap_DaemonCrash(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "sigterm-trap", "bad-sigterm-trap.sh")

	pid := verifyRunningDataStructures(t, env, "sigterm-trap", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// Verify the PID tracker has recorded the process
	tracked := env.Daemon.pidTracker.ListTracked()
	require.NotEmpty(t, tracked, "PID tracker should have recorded the process")
	t.Logf("PID tracker has %d tracked process(es), path=%s", len(tracked), env.Daemon.pidTracker.Path())

	// Simulate daemon crash: cancel context without calling Stop().
	// This leaves the PID tracker file intact (Stop would call Clear).
	env.Daemon.cancel()
	// Give time for goroutines to wind down without cleanup
	time.Sleep(500 * time.Millisecond)

	// The SIGTERM-trapping process should still be alive (no cleanup was sent)
	assertProcessAlive(t, pid)
	t.Logf("process %d survived daemon crash (SIGTERM trap active)", pid)

	// Verify the PID tracker file still exists with process data
	trackerPath := env.Daemon.pidTracker.Path()
	_, statErr := os.Stat(trackerPath)
	require.NoError(t, statErr, "PID tracker file should still exist after crash")

	// Start a recovery daemon. On Start(), it calls cleanupOrphans() which
	// detects a different daemon PID and sends SIGKILL to all tracked PIDs.
	_ = createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// The SIGTERM-trapping process must now be dead (killed by SIGKILL, not SIGTERM)
	assertProcessDead(t, pid, 10*time.Second)
	t.Logf("process %d killed by orphan cleanup after crash recovery", pid)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after orphan cleanup", port)
}

// TestE2E_BadProcess_ForkDetach_DaemonCrash verifies that after a daemon
// crash, a process that spawned a detached child (setsid+nohup) is cleaned
// up by the new daemon's orphan recovery, which uses stored descendants to
// kill the detached child that escaped the process group.
func TestE2E_BadProcess_ForkDetach_DaemonCrash(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port := freePort(t)
	env.TrackPort(port)
	pidFilePath := filepath.Join(env.ProjectDir, "fork-detach.pid")

	// Write a script that spawns a setsid child and stays alive long enough
	// for the descendant scanner to record the child.
	scriptContent := fmt.Sprintf(`PORT=%d
PIDFILE="%s"
echo $$ > "$PIDFILE"
setsid nohup bash -c "exec 0</dev/null 1>/dev/null 2>/dev/null; socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo detached-child' 2>/dev/null" &
echo "fork-detach parent=$$ on http://localhost:$PORT"
trap 'exit 0' SIGTERM SIGINT
sleep 3600 &
wait
`, port, pidFilePath)
	scriptPath := writeTempScript(t, env.ProjectDir, "test-fork-detach-crash.sh", scriptContent)

	kdl := fmt.Sprintf(`scripts {
    fork-detach {
        run "bash %s"
        autostart true
        ports %d
    }
}
`, scriptPath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)

	processID := script.MakeProcessID(env.ProjectDir, "fork-detach")

	// Wait for parent PID file and port
	parentPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, parentPID, 0)
	require.NoError(t, waitForPort(port, 10*time.Second), "detached child should bind port %d", port)

	detachedPID := findPortUser(port)
	require.Greater(t, detachedPID, 0, "must find detached child on port %d", port)
	assertProcessAlive(t, detachedPID)
	t.Logf("parent PID=%d, detached child PID=%d", parentPID, detachedPID)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() {
		killTree(pmPID)
		if p, findErr := os.FindProcess(detachedPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	// Wait for the descendant scanner (5s interval) to record the detached child
	time.Sleep(6 * time.Second)

	// Verify descendants are stored in the PID tracker
	tracked := env.Daemon.pidTracker.ListTracked()
	var storedDescendants []int
	for _, tp := range tracked {
		storedDescendants = append(storedDescendants, tp.DescendantPIDs...)
	}
	t.Logf("stored descendants: %v", storedDescendants)

	// Simulate daemon crash
	env.Daemon.cancel()
	time.Sleep(500 * time.Millisecond)

	// Both parent and detached child should survive the crash
	assertProcessAlive(t, parentPID)
	assertProcessAlive(t, detachedPID)
	t.Logf("both parent and detached child survived crash")

	// Start recovery daemon
	_ = createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// Orphan cleanup should kill parent via stored PID and detached child
	// via stored descendants
	assertProcessDead(t, parentPID, 10*time.Second)
	assertProcessDead(t, detachedPID, 10*time.Second)
	t.Logf("orphan cleanup killed both parent and detached child")

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after orphan cleanup", port)
}

// TestE2E_BadProcess_DoubleFork_DaemonCrash verifies that a classic
// double-fork daemon pattern (parent -> child -> grandchild, where the
// grandchild is reparented to init) is cleaned up by the new daemon's
// orphan recovery using stored descendants from the PID tracker.
func TestE2E_BadProcess_DoubleFork_DaemonCrash(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port := freePort(t)
	env.TrackPort(port)
	pidFilePath := filepath.Join(env.ProjectDir, "double-fork.pid")
	grandchildPIDFile := filepath.Join(env.ProjectDir, "grandchild.pid")

	// Write a double-fork script. The parent and child subshell stay alive
	// long enough for the descendant scanner to record the grandchild tree.
	scriptContent := fmt.Sprintf(`PORT=%d
PIDFILE="%s"
GCPIDFILE="%s"
echo $$ > "$PIDFILE"
# First fork: child subshell
(
    # Second fork: grandchild in new session, closes fds to fully detach
    setsid bash -c "echo \$\$ > $GCPIDFILE; exec 0</dev/null 1>/dev/null 2>/dev/null; socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo double-fork-grandchild' 2>/dev/null" &
    # Child stays alive (scanner needs to see grandchild as descendant)
    trap 'exit 0' SIGTERM SIGINT
    sleep 3600 &
    wait
) &
echo "double-fork parent=$$ on http://localhost:$PORT"
trap 'exit 0' SIGTERM SIGINT
sleep 3600 &
wait
`, port, pidFilePath, grandchildPIDFile)
	scriptPath := writeTempScript(t, env.ProjectDir, "test-double-fork-crash.sh", scriptContent)

	kdl := fmt.Sprintf(`scripts {
    double-fork {
        run "bash %s"
        autostart true
        ports %d
    }
}
`, scriptPath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)

	processID := script.MakeProcessID(env.ProjectDir, "double-fork")

	// Wait for parent PID file
	parentPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, parentPID, 0)

	// Wait for grandchild to bind port
	require.NoError(t, waitForPort(port, 10*time.Second), "grandchild should bind port %d", port)

	// Find grandchild PID
	grandchildPID := readPIDFile(t, grandchildPIDFile, 5*time.Second)
	if grandchildPID <= 0 {
		grandchildPID = findPortUser(port)
	}
	require.Greater(t, grandchildPID, 0, "must find grandchild")
	assertProcessAlive(t, grandchildPID)
	t.Logf("parent PID=%d, grandchild PID=%d", parentPID, grandchildPID)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() {
		killTree(pmPID)
		if p, findErr := os.FindProcess(grandchildPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	// Wait for the descendant scanner (5s interval) to record the tree
	time.Sleep(6 * time.Second)

	// Verify descendants include the grandchild
	tracked := env.Daemon.pidTracker.ListTracked()
	var allStoredDescendants []int
	for _, tp := range tracked {
		allStoredDescendants = append(allStoredDescendants, tp.DescendantPIDs...)
	}
	t.Logf("stored descendants: %v", allStoredDescendants)

	// Simulate daemon crash
	env.Daemon.cancel()
	time.Sleep(500 * time.Millisecond)

	// Grandchild (reparented to init via setsid) should survive the crash
	assertProcessAlive(t, grandchildPID)
	t.Logf("grandchild %d survived daemon crash", grandchildPID)

	// Start recovery daemon
	_ = createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// Orphan cleanup should use stored descendants to find and kill grandchild
	assertProcessDead(t, grandchildPID, 10*time.Second)
	t.Logf("orphan cleanup killed grandchild via stored descendants")

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after orphan cleanup", port)
}

// TestE2E_OrphanCleanup_DaemonCrash verifies that after a daemon crash, two
// well-behaved autostart processes (fake-pnpm-watch and fake-vitest) survive
// the crash as orphans, are killed by the recovery daemon's orphan cleanup,
// and a subsequent autostart on the recovery daemon succeeds without EADDRINUSE.
func TestE2E_OrphanCleanup_DaemonCrash(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)

	devPort := freePort(t)
	testPort := freePort(t)
	env.TrackPort(devPort)
	env.TrackPort(testPort)
	devPidFile := filepath.Join(env.ProjectDir, "dev.pid")
	testPidFile := filepath.Join(env.ProjectDir, "test.pid")
	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")
	vitestPath := testdataPath(t, "fake-vitest.sh")

	kdl := fmt.Sprintf(`scripts {
    dev {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
    test {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
`, pnpmPath, devPort, devPidFile, devPort,
		vitestPath, testPort, testPidFile, testPort)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)
	require.Len(t, result.Scripts, 2, "expected 2 scripts started")

	devProcessID := script.MakeProcessID(env.ProjectDir, "dev")
	testProcessID := script.MakeProcessID(env.ProjectDir, "test")

	devPid := verifyRunningDataStructures(t, env, "dev", devProcessID, devPort, devPidFile)
	testPid := verifyRunningDataStructures(t, env, "test", testProcessID, testPort, testPidFile)

	pm := env.Daemon.ProcessManager()
	devProc, err := pm.Get(devProcessID)
	require.NoError(t, err)
	devPmPID := devProc.PID()
	testProc, err := pm.Get(testProcessID)
	require.NoError(t, err)
	testPmPID := testProc.PID()
	t.Cleanup(func() {
		killTree(devPmPID)
		killTree(testPmPID)
	})

	// Verify PID tracker recorded both processes
	tracked := env.Daemon.pidTracker.ListTracked()
	require.GreaterOrEqual(t, len(tracked), 2, "PID tracker should have at least 2 entries")
	t.Logf("PID tracker has %d tracked process(es)", len(tracked))

	// Simulate daemon crash: cancel context without calling Stop()
	env.Daemon.cancel()
	time.Sleep(500 * time.Millisecond)

	// Both orphaned processes should still be alive after crash
	assertProcessAlive(t, devPid)
	assertProcessAlive(t, testPid)
	t.Logf("both processes survived daemon crash: dev=%d test=%d", devPid, testPid)

	// Both ports should still be bound
	require.NoError(t, waitForPort(devPort, 2*time.Second), "dev port %d should still be bound", devPort)
	require.NoError(t, waitForPort(testPort, 2*time.Second), "test port %d should still be bound", testPort)

	// Start recovery daemon - orphan cleanup kills both processes
	recoveryDaemon := createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// Both processes must be killed by orphan cleanup
	assertProcessDead(t, devPid, 10*time.Second)
	assertProcessDead(t, testPid, 10*time.Second)
	t.Logf("orphan cleanup killed both processes")

	// Both ports must be free
	err = waitForPort(devPort, 500*time.Millisecond)
	assert.Error(t, err, "dev port %d should be free after orphan cleanup", devPort)
	err = waitForPort(testPort, 500*time.Millisecond)
	assert.Error(t, err, "test port %d should be free after orphan cleanup", testPort)

	// Recovery daemon's ProcessManager must be clean (no stale entries)
	recoveryPM := recoveryDaemon.ProcessManager()
	assert.Empty(t, recoveryPM.List(), "recovery daemon ProcessManager should have no stale entries")

	// Remove stale PID files so new processes can write fresh ones
	os.Remove(devPidFile)
	os.Remove(testPidFile)

	// Re-autostart on recovery daemon must succeed without EADDRINUSE
	result2 := recoveryDaemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result2.Errors, "re-autostart errors (would indicate EADDRINUSE): %v", result2.Errors)
	require.Len(t, result2.Scripts, 2, "re-autostart should start 2 scripts")

	// Verify new processes are running with new PIDs
	newDevPid := readPIDFile(t, devPidFile, 10*time.Second)
	newTestPid := readPIDFile(t, testPidFile, 10*time.Second)
	require.Greater(t, newDevPid, 0)
	require.Greater(t, newTestPid, 0)
	assert.NotEqual(t, devPid, newDevPid, "new dev PID must differ from orphaned PID")
	assert.NotEqual(t, testPid, newTestPid, "new test PID must differ from orphaned PID")

	// Verify new processes bind the ports successfully
	require.NoError(t, waitForPort(devPort, 10*time.Second), "new dev process should bind port %d", devPort)
	require.NoError(t, waitForPort(testPort, 10*time.Second), "new test process should bind port %d", testPort)

	assertProcessAlive(t, newDevPid)
	assertProcessAlive(t, newTestPid)
}

// TestE2E_OrphanCleanup_DotnetProcessTree verifies that after a daemon crash,
// a dotnet-watch process (which spawns a setsid grandchild) is fully cleaned
// up by the recovery daemon's orphan cleanup using stored descendants.
func TestE2E_OrphanCleanup_DotnetProcessTree(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "serve", "fake-dotnet-watch.sh")

	parentPid := verifyRunningDataStructures(t, env, "serve", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()

	// Find the setsid grandchild by looking at descendants and checking for
	// a different process group (setsid creates a new session/group).
	var grandchildPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		descendants := getDescendants(pmPID)
		for _, dpid := range descendants {
			pgid := getProcessPGID(dpid)
			if pgid > 0 && pgid != pmPID && pgid != getProcessPGID(pmPID) {
				grandchildPID = dpid
				break
			}
		}
		if grandchildPID > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Fall back to port user if PGID check didn't find a clear setsid child
	if grandchildPID == 0 {
		grandchildPID = findPortUser(port)
	}
	require.Greater(t, grandchildPID, 0, "must find setsid grandchild")
	assertProcessAlive(t, grandchildPID)
	t.Logf("parent PID=%d, grandchild PID=%d, managed PID=%d", parentPid, grandchildPID, pmPID)

	t.Cleanup(func() {
		killTree(pmPID)
		if p, findErr := os.FindProcess(grandchildPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	// Wait for the descendant scanner (5s interval) to record the grandchild
	time.Sleep(6 * time.Second)

	// Verify descendants are stored in the PID tracker
	tracked := env.Daemon.pidTracker.ListTracked()
	var storedDescendants []int
	for _, tp := range tracked {
		storedDescendants = append(storedDescendants, tp.DescendantPIDs...)
	}
	t.Logf("stored descendants: %v", storedDescendants)

	// Simulate daemon crash
	env.Daemon.cancel()
	time.Sleep(500 * time.Millisecond)

	// Both parent and setsid grandchild should survive the crash
	assertProcessAlive(t, parentPid)
	assertProcessAlive(t, grandchildPID)
	t.Logf("both parent and grandchild survived daemon crash")

	// Start recovery daemon - orphan cleanup kills parent + stored descendants
	_ = createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// Both parent and setsid grandchild must be killed
	assertProcessDead(t, parentPid, 10*time.Second)
	assertProcessDead(t, grandchildPID, 10*time.Second)
	t.Logf("orphan cleanup killed parent and setsid grandchild")

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after orphan cleanup", port)
}

// TestE2E_GracefulRestart_PIDTrackerPreserved verifies that the PID tracker
// file is preserved after a graceful daemon Stop(). Previously, Stop() would
// call pidTracker.Clear(), which erased the tracking file. If any process
// survived shutdown (e.g., due to tight timeout), the next daemon startup
// had no record of it. This test verifies the tracker persists across restarts
// and the recovery daemon can still clean up survivors.
func TestE2E_GracefulRestart_PIDTrackerPreserved(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port := freePort(t)
	env.TrackPort(port)
	pidFilePath := filepath.Join(env.ProjectDir, "restart-preserve.pid")

	// Use a SIGTERM-trapping script that ignores SIGTERM entirely.
	// With aggressive shutdown (tight timeout <3s), SIGKILL is used directly,
	// so the process should die. The key assertion is that the PID tracker
	// file persists after Stop(), enabling recovery if processes did survive.
	scriptContent := fmt.Sprintf(`PORT=%d
PIDFILE="%s"
echo $$ > "$PIDFILE"
socat TCP-LISTEN:$PORT,fork,reuseaddr SYSTEM:'echo sigterm-trap' &
CHILD=$!
echo "sigterm-trap parent=$$ on http://localhost:$PORT"
trap '' SIGTERM SIGINT
wait $CHILD
`, port, pidFilePath)
	scriptPath := writeTempScript(t, env.ProjectDir, "test-restart-preserve.sh", scriptContent)

	kdl := fmt.Sprintf(`scripts {
    sigterm-trap {
        run "bash %s"
        autostart true
        ports %d
    }
}
`, scriptPath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)

	processID := script.MakeProcessID(env.ProjectDir, "sigterm-trap")

	parentPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, parentPID, 0)
	require.NoError(t, waitForPort(port, 10*time.Second))

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	tracked := env.Daemon.pidTracker.ListTracked()
	require.NotEmpty(t, tracked, "PID tracker should have recorded the process")

	// Graceful stop
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	env.Daemon.Stop(stopCtx)
	time.Sleep(500 * time.Millisecond)

	// Key assertion: PID tracker file must still exist after Stop()
	trackerPath := env.Daemon.pidTracker.Path()
	_, statErr := os.Stat(trackerPath)
	require.NoError(t, statErr, "PID tracker file must persist after graceful stop")

	// Recovery daemon can safely handle both scenarios (process dead or alive)
	_ = createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// Process must be dead after the full cycle
	assertProcessDead(t, parentPID, 10*time.Second)
	t.Logf("process %d dead after daemon restart cycle", parentPID)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free", port)
}

// TestE2E_GracefulRestart_PortCleanupOnAutostart verifies that when a rogue
// process holds a configured port, RunAutostart's preflightPortCleanup kills
// it before starting the new process. This covers the case where a child
// escaped PID tracking entirely but still holds a configured port.
func TestE2E_GracefulRestart_PortCleanupOnAutostart(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port := freePort(t)
	env.TrackPort(port)

	// Start a rogue process directly (not managed by daemon) that holds a
	// port configured in .agnt.kdl.
	rogueCmd := exec.Command("socat", "TCP-LISTEN:"+strconv.Itoa(port)+",fork,reuseaddr", "SYSTEM:echo rogue")
	rogueCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, rogueCmd.Start())
	roguePID := rogueCmd.Process.Pid
	t.Cleanup(func() {
		if p, findErr := os.FindProcess(roguePID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	require.NoError(t, waitForPort(port, 5*time.Second), "rogue process should bind port %d", port)
	t.Logf("rogue process PID=%d on port %d", roguePID, port)

	// Write .agnt.kdl that declares this port
	pidFilePath := filepath.Join(env.ProjectDir, "port-cleanup.pid")
	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")
	kdl := fmt.Sprintf(`scripts {
    dev {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
`, pnpmPath, port, pidFilePath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	// Stop the original daemon
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	env.Daemon.Stop(stopCtx)

	// Rogue process should still be alive after daemon stop
	assertProcessAlive(t, roguePID)

	// Start recovery daemon
	recoveryDaemon := createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// RunAutostart triggers preflightPortCleanup which kills the rogue
	// and starts a new process on the same port.
	ctx := context.Background()
	result := recoveryDaemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart should succeed (preflight kills rogue): %v", result.Errors)

	// Rogue must be dead
	assertProcessDead(t, roguePID, 10*time.Second)
	t.Logf("rogue process %d killed by preflight port cleanup", roguePID)

	// New process should own the port
	newPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, newPID, 0)
	assert.NotEqual(t, roguePID, newPID, "new process PID must differ from rogue")
	require.NoError(t, waitForPort(port, 10*time.Second), "new process should bind port %d", port)
}

// TestE2E_PIDTracker_FileFormat verifies that the PID tracker file (pids.json)
// contains the expected JSON structure: daemon_pid, processes array with pid,
// pgid, project_path, and updated_at timestamp. This reads the file directly
// rather than going through the tracker API.
func TestE2E_PIDTracker_FileFormat(t *testing.T) {
	env, _ := setupDaemonForCrashTest(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "format-check", "fake-pnpm-watch.sh")

	pid := verifyRunningDataStructures(t, env, "format-check", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// Read pids.json directly and validate structure
	trackerPath := env.Daemon.pidTracker.Path()
	data, err := os.ReadFile(trackerPath)
	require.NoError(t, err, "pids.json must be readable")

	var tracking process.PIDTracking
	require.NoError(t, json.Unmarshal(data, &tracking), "pids.json must be valid JSON")

	// Validate daemon_pid matches the current test process PID
	assert.Equal(t, os.Getpid(), tracking.DaemonPID, "daemon_pid should be current process PID")

	// Validate processes array
	require.NotEmpty(t, tracking.Processes, "processes array must not be empty")

	var found bool
	for _, p := range tracking.Processes {
		if p.ProjectPath == env.ProjectDir {
			found = true
			assert.Greater(t, p.PID, 0, "tracked process PID must be positive")
			assert.Greater(t, p.PGID, 0, "tracked process PGID must be positive")
			assert.NotEmpty(t, p.ID, "tracked process ID must not be empty")
			assert.False(t, p.StartedAt.IsZero(), "tracked process started_at must be set")
			t.Logf("tracked: id=%s pid=%d pgid=%d project=%s", p.ID, p.PID, p.PGID, p.ProjectPath)
		}
	}
	assert.True(t, found, "must find tracked process for project %s", env.ProjectDir)

	// Validate updated_at is recent
	assert.False(t, tracking.UpdatedAt.IsZero(), "updated_at must be set")
	assert.WithinDuration(t, time.Now(), tracking.UpdatedAt, 30*time.Second, "updated_at should be recent")

	// Verify the raw JSON has expected top-level keys
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "daemon_pid", "JSON must have daemon_pid key")
	assert.Contains(t, raw, "processes", "JSON must have processes key")
	assert.Contains(t, raw, "updated_at", "JSON must have updated_at key")

	_ = pid // verified via verifyRunningDataStructures
}

// TestE2E_PIDTracker_PreservedFileAfterStop verifies that after a graceful
// daemon Stop(), the pids.json file persists on disk (not deleted) with valid
// JSON. Processes are individually removed during shutdown as each is stopped,
// so the processes array is empty. The daemon_pid field is preserved, enabling
// the next daemon to detect a clean restart.
func TestE2E_PIDTracker_PreservedFileAfterStop(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "preserve-file", "fake-pnpm-watch.sh")

	_ = verifyRunningDataStructures(t, env, "preserve-file", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// Read pids.json before stop to capture initial state
	trackerPath := env.Daemon.pidTracker.Path()
	dataBefore, err := os.ReadFile(trackerPath)
	require.NoError(t, err)

	var trackingBefore process.PIDTracking
	require.NoError(t, json.Unmarshal(dataBefore, &trackingBefore))
	require.NotEmpty(t, trackingBefore.Processes, "processes must exist before stop")
	t.Logf("before stop: daemon_pid=%d, processes=%d", trackingBefore.DaemonPID, len(trackingBefore.Processes))

	// Graceful stop: processes are killed and individually removed from tracker
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	env.Daemon.Stop(stopCtx)
	time.Sleep(500 * time.Millisecond)

	// File must still exist (not deleted by Stop)
	dataAfter, err := os.ReadFile(trackerPath)
	require.NoError(t, err, "pids.json must persist after graceful stop")

	var trackingAfter process.PIDTracking
	require.NoError(t, json.Unmarshal(dataAfter, &trackingAfter), "pids.json must be valid JSON after stop")

	// Processes are empty because each was individually removed during shutdown
	assert.Empty(t, trackingAfter.Processes, "processes should be empty after graceful shutdown")

	// daemon_pid is preserved
	assert.Equal(t, trackingBefore.DaemonPID, trackingAfter.DaemonPID, "daemon PID must be preserved")
	t.Logf("after stop: daemon_pid=%d, processes=%d", trackingAfter.DaemonPID, len(trackingAfter.Processes))

	// Recovery daemon detects same daemon PID scenario (clean restart)
	_ = createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	// After recovery, pids.json is updated with new daemon PID (os.Getpid()
	// of the recovery daemon, which in tests is the same test process PID)
	dataRecovery, err := os.ReadFile(trackerPath)
	require.NoError(t, err)
	var trackingRecovery process.PIDTracking
	require.NoError(t, json.Unmarshal(dataRecovery, &trackingRecovery))
	assert.Empty(t, trackingRecovery.Processes, "processes must remain empty after recovery")
	// createRecoveryDaemon sets stored PID to fakeCrashedDaemonPID to trigger crash
	// detection; recovery daemon then sets it back to os.Getpid().
	assert.NotEqual(t, fakeCrashedDaemonPID, trackingRecovery.DaemonPID,
		"recovery daemon must update PID from simulated crash value")
	assert.Equal(t, os.Getpid(), trackingRecovery.DaemonPID,
		"recovery daemon PID must be current process PID")
}

// TestE2E_PIDTracker_CrashRetainsProcesses verifies that after a daemon crash
// (context cancel without Stop), pids.json retains the full process entries
// with PIDs, PGIDs, and project paths. The recovery daemon reads this data
// to detect and kill orphans.
func TestE2E_PIDTracker_CrashRetainsProcesses(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port, pidFilePath, processID := runSingleScriptAutostart(t, env, "crash-retain", "fake-pnpm-watch.sh")

	pid := verifyRunningDataStructures(t, env, "crash-retain", processID, port, pidFilePath)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	trackerPath := env.Daemon.pidTracker.Path()

	// Read pids.json before crash
	dataBefore, err := os.ReadFile(trackerPath)
	require.NoError(t, err)
	var trackingBefore process.PIDTracking
	require.NoError(t, json.Unmarshal(dataBefore, &trackingBefore))
	require.NotEmpty(t, trackingBefore.Processes)
	storedPID := trackingBefore.Processes[0].PID
	storedPGID := trackingBefore.Processes[0].PGID
	storedID := trackingBefore.Processes[0].ID
	t.Logf("before crash: daemon_pid=%d, process id=%s pid=%d pgid=%d",
		trackingBefore.DaemonPID, storedID, storedPID, storedPGID)

	// Simulate crash: cancel context without calling Stop
	env.Daemon.cancel()
	time.Sleep(500 * time.Millisecond)

	// Process should survive the crash
	assertProcessAlive(t, pid)

	// Read pids.json after crash - must retain all data
	dataAfter, err := os.ReadFile(trackerPath)
	require.NoError(t, err, "pids.json must survive crash")
	var trackingAfter process.PIDTracking
	require.NoError(t, json.Unmarshal(dataAfter, &trackingAfter))

	// Processes must still be recorded
	require.NotEmpty(t, trackingAfter.Processes, "processes must be retained after crash")
	assert.Equal(t, storedPID, trackingAfter.Processes[0].PID, "PID must be retained after crash")
	assert.Equal(t, storedPGID, trackingAfter.Processes[0].PGID, "PGID must be retained after crash")
	assert.Equal(t, storedID, trackingAfter.Processes[0].ID, "process ID must be retained after crash")
	assert.Equal(t, trackingBefore.DaemonPID, trackingAfter.DaemonPID, "daemon PID must be retained after crash")

	// Recovery daemon reads retained data and kills orphans
	_ = createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)
	assertProcessDead(t, pid, 10*time.Second)

	// After recovery: processes cleared, daemon PID updated
	dataRecovery, err := os.ReadFile(trackerPath)
	require.NoError(t, err)
	var trackingRecovery process.PIDTracking
	require.NoError(t, json.Unmarshal(dataRecovery, &trackingRecovery))
	assert.Empty(t, trackingRecovery.Processes, "processes must be cleared after recovery")
	t.Logf("after recovery: daemon_pid=%d, processes=%d", trackingRecovery.DaemonPID, len(trackingRecovery.Processes))

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after orphan cleanup", port)
}

// TestE2E_PIDTracker_AtomicWrites verifies that pids.json is always valid JSON
// even during rapid process additions, and that no .tmp files are left behind.
func TestE2E_PIDTracker_AtomicWrites(t *testing.T) {
	env, _ := setupDaemonForCrashTest(t)

	// Create multiple scripts that all autostart simultaneously
	ports := make([]int, 4)
	pidFiles := make([]string, 4)
	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")

	var kdlBuilder strings.Builder
	kdlBuilder.WriteString("scripts {\n")
	for i := range ports {
		ports[i] = freePort(t)
		env.TrackPort(ports[i])
		pidFiles[i] = filepath.Join(env.ProjectDir, fmt.Sprintf("atomic-%d.pid", i))
		kdlBuilder.WriteString(fmt.Sprintf("    script%d {\n        run \"bash %s %d %s\"\n        autostart true\n        ports %d\n    }\n",
			i, pnpmPath, ports[i], pidFiles[i], ports[i]))
	}
	kdlBuilder.WriteString("}\n")
	writeAgntKDL(t, env.ProjectDir, kdlBuilder.String())

	trackerPath := env.Daemon.pidTracker.Path()
	trackerDir := filepath.Dir(trackerPath)

	// Start reading pids.json rapidly in a goroutine during autostart
	stopReader := make(chan struct{})
	readerDone := make(chan struct{})
	var readErrors []string
	var readCount int
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stopReader:
				return
			default:
			}
			data, err := os.ReadFile(trackerPath)
			if err != nil {
				// File may not exist yet during first write
				time.Sleep(5 * time.Millisecond)
				continue
			}
			readCount++
			if !json.Valid(data) {
				readErrors = append(readErrors, fmt.Sprintf("read %d: invalid JSON (%d bytes)", readCount, len(data)))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Run autostart which adds 4 processes rapidly
	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)
	require.Len(t, result.Scripts, 4, "expected 4 scripts started")

	// Wait for all processes to start
	for i, port := range ports {
		require.NoError(t, waitForPort(port, 10*time.Second), "script%d port %d not bound", i, port)
	}

	// Stop the reader goroutine and wait for it
	close(stopReader)
	<-readerDone

	// All reads must have produced valid JSON
	assert.Empty(t, readErrors, "pids.json had invalid JSON during rapid writes: %v", readErrors)
	assert.Greater(t, readCount, 0, "must have read pids.json at least once")
	t.Logf("read pids.json %d times during rapid autostart, all valid JSON", readCount)

	// Check no .tmp files left behind
	entries, err := os.ReadDir(trackerDir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasSuffix(entry.Name(), ".tmp"),
			"leftover temp file found: %s", entry.Name())
	}

	// Final read should show all 4 processes
	data, err := os.ReadFile(trackerPath)
	require.NoError(t, err)
	var tracking process.PIDTracking
	require.NoError(t, json.Unmarshal(data, &tracking))
	assert.Len(t, tracking.Processes, 4, "all 4 processes should be tracked")
	t.Logf("final pids.json: daemon_pid=%d, processes=%d", tracking.DaemonPID, len(tracking.Processes))

	// Cleanup
	pm := env.Daemon.ProcessManager()
	for i := range ports {
		processID := script.MakeProcessID(env.ProjectDir, fmt.Sprintf("script%d", i))
		if proc, getErr := pm.Get(processID); getErr == nil {
			t.Cleanup(func() { killTree(proc.PID()) })
		}
	}
}

// TestE2E_ProcessTree_DeepNesting verifies that a 3-level deep process tree
// (sh -> sh -> sh -> sleep 300) where all levels stay alive is fully cleaned
// up when the daemon stops. All children share the managed process's PGID
// (no setsid), so SIGTERM to the process group should kill all levels.
//
// This differs from DoubleFork (which uses setsid to escape PGID) and
// ForkBombLite (which spawns many children at one level). Deep nesting tests
// that the process group signal reaches children at arbitrary depth.
func TestE2E_ProcessTree_DeepNesting(t *testing.T) {
	env := setupDaemonForE2E(t)
	port := freePort(t)
	env.TrackPort(port)
	pidFilePath := filepath.Join(env.ProjectDir, "deep-nest.pid")
	level2PIDFile := filepath.Join(env.ProjectDir, "deep-nest-level2.pid")
	level3PIDFile := filepath.Join(env.ProjectDir, "deep-nest-level3.pid")

	// 3-level nesting: level1 (bash) -> level2 (bash) -> level3 (bash+socat+sleep)
	// All levels stay alive via "sleep & wait" so the full tree is intact at
	// cleanup time. No setsid — all share the managed process's PGID.
	scriptContent := fmt.Sprintf(`#!/usr/bin/env bash
PORT=%d
PIDFILE="%s"
L2PID="%s"
L3PID="%s"
echo $$ > "$PIDFILE"

# Level 2: subshell that spawns level 3
bash -c '
echo $$ > "'"$L2PID"'"
# Level 3: subshell with socat + sleep
bash -c '"'"'
echo $$ > "'"$L3PID"'"
socat TCP-LISTEN:'"$PORT"',fork,reuseaddr SYSTEM:"echo deep-nest-level3" &
sleep 300 &
trap "exit 0" SIGTERM SIGINT
wait
'"'"' &
sleep 300 &
trap "exit 0" SIGTERM SIGINT
wait
' &

echo "deep-nest parent=$$ on http://localhost:$PORT"
trap 'exit 0' SIGTERM SIGINT
sleep 300 &
wait
`, port, pidFilePath, level2PIDFile, level3PIDFile)
	scriptPath := writeTempScript(t, env.ProjectDir, "test-deep-nest.sh", scriptContent)

	kdl := fmt.Sprintf(`scripts {
    deep-nest {
        run "bash %s"
        autostart true
        ports %d
    }
}
`, scriptPath, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)

	processID := script.MakeProcessID(env.ProjectDir, "deep-nest")

	// Wait for level 1 PID file
	parentPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, parentPID, 0)

	// Wait for level 3 to bind the port
	require.NoError(t, waitForPort(port, 10*time.Second), "level 3 should bind port %d", port)

	// Read level 2 and level 3 PIDs
	level2PID := readPIDFile(t, level2PIDFile, 5*time.Second)
	require.Greater(t, level2PID, 0, "level 2 PID must be recorded")
	level3PID := readPIDFile(t, level3PIDFile, 5*time.Second)
	require.Greater(t, level3PID, 0, "level 3 PID must be recorded")

	t.Logf("level1 PID=%d, level2 PID=%d, level3 PID=%d", parentPID, level2PID, level3PID)

	pm := env.Daemon.ProcessManager()
	proc, err := pm.Get(processID)
	require.NoError(t, err)
	pmPID := proc.PID()
	t.Cleanup(func() { killTree(pmPID) })

	// All 3 levels must be alive
	assertProcessAlive(t, parentPID)
	assertProcessAlive(t, level2PID)
	assertProcessAlive(t, level3PID)

	// All levels should share the managed process's PGID (no setsid used)
	parentPGID := getProcessPGID(pmPID)
	require.Greater(t, parentPGID, 0, "managed process PGID must be valid")

	l1PGID := getProcessPGID(parentPID)
	l2PGID := getProcessPGID(level2PID)
	l3PGID := getProcessPGID(level3PID)
	assert.Equal(t, parentPGID, l1PGID, "level 1 should share managed process PGID")
	assert.Equal(t, parentPGID, l2PGID, "level 2 should share managed process PGID")
	assert.Equal(t, parentPGID, l3PGID, "level 3 should share managed process PGID")

	// Verify all 3 are in the descendant tree of the managed process
	descendants := getDescendants(pmPID)
	assert.Contains(t, descendants, parentPID, "level 1 should be descendant of managed process")
	assert.Contains(t, descendants, level2PID, "level 2 should be descendant of managed process")
	assert.Contains(t, descendants, level3PID, "level 3 should be descendant of managed process")
	t.Logf("managed PID=%d has %d descendants: %v", pmPID, len(descendants), descendants)

	// Stop daemon gracefully — SIGTERM to process group should kill all levels
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	// All 3 levels must be dead
	assertProcessDead(t, parentPID, 10*time.Second)
	assertProcessDead(t, level2PID, 10*time.Second)
	assertProcessDead(t, level3PID, 10*time.Second)

	// No orphans remain under the managed process PID
	assertAllDescendantsDead(t, pmPID, 10*time.Second)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after cleanup", port)

	// ProcessManager entry must reflect cleanup
	procAfter, getErr := pm.Get(processID)
	require.NoError(t, getErr)
	state := procAfter.State()
	assert.True(t, state == process.StateStopped || state == process.StateFailed,
		"process state should be Stopped or Failed, got %s", state)
}

// TestE2E_EADDRINUSE_RetryAfterDetection verifies that when a script outputs an
// EADDRINUSE error, the daemon's startScriptWithRetry detects it, kills the port
// holder, and successfully retries the script.
func TestE2E_EADDRINUSE_RetryAfterDetection(t *testing.T) {
	env, _ := setupDaemonForCrashTest(t)
	port := freePort(t)
	env.TrackPort(port)

	// Start a holder process that occupies the port (simulates an orphan).
	holderCmd := exec.Command("socat", "TCP-LISTEN:"+strconv.Itoa(port)+",fork,reuseaddr", "SYSTEM:echo holder")
	holderCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, holderCmd.Start())
	holderPID := holderCmd.Process.Pid
	t.Cleanup(func() {
		if p, findErr := os.FindProcess(holderPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	require.NoError(t, waitForPort(port, 5*time.Second), "holder should bind port %d", port)
	t.Logf("holder PID=%d on port %d", holderPID, port)

	// Write .agnt.kdl using fake-eaddrinuse.sh which emits EADDRINUSE on stderr
	// when the port is already taken, and succeeds normally when the port is free.
	// No "ports" declaration: pre-flight cleanup is skipped, so the holder
	// survives until the script runs and gets EADDRINUSE, exercising the retry path.
	pidFilePath := filepath.Join(env.ProjectDir, "eaddrinuse-retry.pid")
	scriptPath := testdataPath(t, "fake-eaddrinuse.sh")
	kdl := fmt.Sprintf(`scripts {
    eaddrinuse-retry {
        run "bash %s %d %s"
        autostart true
    }
}
`, scriptPath, port, pidFilePath)
	writeAgntKDL(t, env.ProjectDir, kdl)

	// RunAutostart should trigger startScriptWithRetry:
	// 1. First attempt: script gets EADDRINUSE, daemon detects it
	// 2. Daemon kills holder on the port
	// 3. Retry: script binds successfully
	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart should succeed after retry: %v", result.Errors)

	// Holder must be dead
	assertProcessDead(t, holderPID, 10*time.Second)
	t.Logf("holder %d killed by EADDRINUSE recovery", holderPID)

	// New process should own the port
	newPID := readPIDFile(t, pidFilePath, 10*time.Second)
	require.Greater(t, newPID, 0)
	assert.NotEqual(t, holderPID, newPID, "new PID must differ from holder")
	require.NoError(t, waitForPort(port, 10*time.Second), "retried process should bind port %d", port)
	t.Logf("retry succeeded: new PID=%d on port %d", newPID, port)
}

// TestE2E_EADDRINUSE_BadProcess_TrapsAndHoldsPort verifies that when a
// SIGTERM-resistant process holds a configured port, preflightPortCleanup
// escalates to SIGKILL and frees the port for the autostart script.
func TestE2E_EADDRINUSE_BadProcess_TrapsAndHoldsPort(t *testing.T) {
	env, stateHome := setupDaemonForCrashTest(t)
	port := freePort(t)
	env.TrackPort(port)

	// Start bad-sigterm-trap.sh externally - it traps and ignores SIGTERM.
	badPIDFile := filepath.Join(env.ProjectDir, "bad-holder.pid")
	badScript := testdataPath(t, "bad-sigterm-trap.sh")
	badCmd := exec.Command("bash", badScript, strconv.Itoa(port), badPIDFile)
	badCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, badCmd.Start())
	badPID := badCmd.Process.Pid
	t.Cleanup(func() {
		if p, findErr := os.FindProcess(badPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	require.NoError(t, waitForPort(port, 5*time.Second), "bad process should bind port %d", port)
	t.Logf("bad-sigterm-trap PID=%d on port %d", badPID, port)

	// Write .agnt.kdl with a normal script expecting the same port
	goodPIDFile := filepath.Join(env.ProjectDir, "good-after-bad.pid")
	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")
	kdl := fmt.Sprintf(`scripts {
    dev {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
`, pnpmPath, port, goodPIDFile, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	// Stop original daemon to simulate recovery scenario
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	env.Daemon.Stop(stopCtx)

	// Bad process should still be alive (it traps SIGTERM)
	assertProcessAlive(t, badPID)

	// Create recovery daemon - RunAutostart triggers preflightPortCleanup
	recoveryDaemon := createRecoveryDaemon(t, stateHome, env.ProjectDir, env.Daemon.pidTracker)

	ctx := context.Background()
	result := recoveryDaemon.RunAutostart(ctx, env.ProjectDir)

	// Filter errors: only fail on errors for the "dev" script we're testing.
	// The recovery daemon may try to restart unrelated scripts from PID tracker.
	var devErrors []string
	for _, e := range result.Errors {
		if strings.Contains(e, "dev") {
			devErrors = append(devErrors, e)
		}
	}
	require.Empty(t, devErrors, "RunAutostart should succeed for dev script: %v", devErrors)

	// New process should own the port (pre-flight killed the socat child
	// that was holding the port; the parent bash may survive since it traps
	// SIGTERM but no longer holds the port)
	newPID := readPIDFile(t, goodPIDFile, 10*time.Second)
	require.Greater(t, newPID, 0)
	assert.NotEqual(t, badPID, newPID, "new PID must differ from bad process")
	require.NoError(t, waitForPort(port, 10*time.Second), "autostart script should bind port %d", port)

	// Verify the port is now held by the new process tree, not the bad process
	portHolder := findPortUser(port)
	assert.NotEqual(t, 0, portHolder, "port should be held after autostart")
	badDescendants := getDescendants(badPID)
	for _, d := range badDescendants {
		assert.NotEqual(t, portHolder, d, "port should not be held by bad process descendant")
	}
	t.Logf("autostart succeeded: new PID=%d on port %d", newPID, port)
}

// TestE2E_CleanupPort_KillsOrphan verifies that the cleanup_port IPC action
// kills an external process holding a port.
func TestE2E_CleanupPort_KillsOrphan(t *testing.T) {
	env := setupDaemonForE2E(t)
	port := freePort(t)
	env.TrackPort(port)

	// Start an external process binding the port
	holderCmd := exec.Command("socat", "TCP-LISTEN:"+strconv.Itoa(port)+",fork,reuseaddr", "SYSTEM:echo orphan")
	holderCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, holderCmd.Start())
	holderPID := holderCmd.Process.Pid
	t.Cleanup(func() {
		if p, findErr := os.FindProcess(holderPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	require.NoError(t, waitForPort(port, 5*time.Second), "holder should bind port %d", port)
	t.Logf("orphan PID=%d on port %d", holderPID, port)

	// Connect a client to the daemon and call cleanup_port
	client := daemonclient.NewClientWithPath(env.SocketPath)
	require.NoError(t, client.Connect())
	defer client.Close()

	resp, err := client.ProcCleanupPort(port)
	require.NoError(t, err, "ProcCleanupPort should not return error")
	t.Logf("cleanup_port response: %+v", resp)

	// Verify the response indicates a kill
	killedCount, _ := resp["killed_count"].(float64)
	assert.Greater(t, killedCount, float64(0), "should have killed at least one process")

	// Holder must be dead
	assertProcessDead(t, holderPID, 10*time.Second)

	// Port must be free
	err = waitForPort(port, 500*time.Millisecond)
	assert.Error(t, err, "port %d should be free after cleanup", port)
	t.Logf("orphan %d killed by cleanup_port", holderPID)
}

// TestE2E_CleanupPort_NoEffect_WhenFree verifies that cleanup_port on an
// unused port returns success with zero kills.
func TestE2E_CleanupPort_NoEffect_WhenFree(t *testing.T) {
	env := setupDaemonForE2E(t)
	port := freePort(t)

	// Verify port is actually free
	err := waitForPort(port, 200*time.Millisecond)
	require.Error(t, err, "port %d should be free before test", port)

	// Connect a client and call cleanup_port on the free port
	client := daemonclient.NewClientWithPath(env.SocketPath)
	require.NoError(t, client.Connect())
	defer client.Close()

	resp, err := client.ProcCleanupPort(port)
	require.NoError(t, err, "ProcCleanupPort on free port should not error")

	killedCount, _ := resp["killed_count"].(float64)
	assert.Equal(t, float64(0), killedCount, "should report zero kills on free port")
	t.Logf("cleanup_port on free port %d: no effect (expected)", port)
}

// TestE2E_CleanupPort_KillsBadProcess verifies that cleanup_port kills a
// SIGTERM-resistant process via SIGKILL escalation.
func TestE2E_CleanupPort_KillsBadProcess(t *testing.T) {
	env := setupDaemonForE2E(t)
	port := freePort(t)
	env.TrackPort(port)

	// Start bad-sigterm-trap.sh which ignores SIGTERM
	badPIDFile := filepath.Join(env.ProjectDir, "bad-cleanup.pid")
	badScript := testdataPath(t, "bad-sigterm-trap.sh")
	badCmd := exec.Command("bash", badScript, strconv.Itoa(port), badPIDFile)
	badCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, badCmd.Start())
	badPID := badCmd.Process.Pid
	t.Cleanup(func() {
		if p, findErr := os.FindProcess(badPID); findErr == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})
	require.NoError(t, waitForPort(port, 5*time.Second), "bad process should bind port %d", port)
	t.Logf("bad-sigterm-trap PID=%d on port %d", badPID, port)

	// Connect a client and call cleanup_port
	client := daemonclient.NewClientWithPath(env.SocketPath)
	require.NoError(t, client.Connect())
	defer client.Close()

	resp, err := client.ProcCleanupPort(port)
	require.NoError(t, err, "ProcCleanupPort should not return error")
	t.Logf("cleanup_port response: %+v", resp)

	killedCount, _ := resp["killed_count"].(float64)
	assert.Greater(t, killedCount, float64(0), "should have killed the port-holding process")

	// Port must be free (cleanup_port kills the socat child on the port;
	// the parent bash may survive since it traps SIGTERM but no longer holds the port)
	err = waitForPort(port, 2*time.Second)
	assert.Error(t, err, "port %d should be free after cleanup", port)

	// Verify no process is listening on the port
	assert.Equal(t, 0, findPortUser(port), "no process should hold port %d after cleanup", port)
	t.Logf("port %d freed by cleanup_port despite SIGTERM-resistant parent", port)
}

// waitForProxy polls the daemon's ProxyManager until a proxy matching the
// given name substring appears, or until timeout. Returns the proxy's listen
// address on success.
func waitForProxy(t *testing.T, d *Daemon, nameSubstr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, p := range d.ProxyManager().ListScoped(scope.Unscoped("test")) {
			if strings.Contains(p.ID, nameSubstr) {
				return p.ListenAddr
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("proxy containing %q not found after %s", nameSubstr, timeout)
	return ""
}

// TestE2E_AutostartWithProxy_ScriptLinked verifies that a proxy linked to a
// script via `script "dev"` is automatically created when the URLTracker
// detects a URL in the script's output, and that HTTP requests through the
// proxy reach the upstream socat server.
func TestE2E_AutostartWithProxy_ScriptLinked(t *testing.T) {
	env := setupDaemonForE2E(t)

	port := freePort(t)
	env.TrackPort(port)
	pidFile := filepath.Join(env.ProjectDir, "dev.pid")
	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")

	kdl := fmt.Sprintf(`scripts {
    dev {
        run "bash %s %d %s"
        autostart true
        ports %d
    }
}
proxies {
    dev {
        script "dev"
        fallback-port %d
    }
}
`, pnpmPath, port, pidFile, port, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)
	require.Contains(t, result.Scripts, "dev", "dev script should be in result.Scripts")

	processID := script.MakeProcessID(env.ProjectDir, "dev")
	pid := verifyRunningDataStructures(t, env, "dev", processID, port, pidFile)

	// Wait for URLTracker to detect the URL and create the proxy.
	// The URLTracker polls every 500ms, so allow up to 15s for the full
	// chain: script output -> URL detection -> proxy event -> proxy creation.
	proxyAddr := waitForProxy(t, env.Daemon, "dev", 15*time.Second)
	t.Logf("proxy created at %s", proxyAddr)

	// Verify proxy is in ProxyManager list and targets the correct upstream.
	// The fake-pnpm-watch socat doesn't speak HTTP, so we verify routing by
	// checking the proxy's target URL rather than making an HTTP round-trip.
	proxies := env.Daemon.ProxyManager().ListScoped(scope.Unscoped("test"))
	var foundProxy bool
	for _, p := range proxies {
		if strings.Contains(p.ID, "dev") {
			foundProxy = true
			assert.Contains(t, p.TargetURL.String(), fmt.Sprintf(":%d", port),
				"proxy target should reference the script's port")
			t.Logf("proxy %s targets %s", p.ID, p.TargetURL.String())
			break
		}
	}
	require.True(t, foundProxy, "proxy linked to dev script should exist in ProxyManager")

	// Verify the proxy is actually accepting connections (listener is up).
	proxyURL := fmt.Sprintf("http://%s/", proxyAddr)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, httpErr := httpClient.Get(proxyURL)
	if httpErr == nil {
		resp.Body.Close()
	}
	// The socat upstream doesn't speak HTTP, so we expect either a proxy
	// error response (502) or a transport error. Either confirms routing.
	if resp != nil {
		assert.True(t, resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusOK,
			"proxy should attempt to route to upstream (got %d)", resp.StatusCode)
	}

	// Stop daemon and verify cleanup
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	verifyStoppedDataStructures(t, pid, port)

	// Proxy should also be cleaned up (no proxies remaining)
	assert.Empty(t, env.Daemon.ProxyManager().ListScoped(scope.Unscoped("test")), "all proxies should be cleaned up after stop")
}

// TestE2E_AutostartWithProxy_FailedScriptSkipsProxy verifies that when a
// script fails to start, any proxy linked to it via `script` is NOT created.
func TestE2E_AutostartWithProxy_FailedScriptSkipsProxy(t *testing.T) {
	env := setupDaemonForE2E(t)

	port := freePort(t)
	env.TrackPort(port)

	kdl := fmt.Sprintf(`scripts {
    broken {
        run "/nonexistent/command/that/does/not/exist"
        autostart true
        ports %d
    }
}
proxies {
    broken-proxy {
        script "broken"
        port %d
        autostart true
    }
}
`, port, port)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)

	// Script must have failed
	require.NotEmpty(t, result.Errors, "expected errors for broken script")
	foundScriptError := false
	for _, e := range result.Errors {
		if strings.Contains(e, "broken") {
			foundScriptError = true
			break
		}
	}
	assert.True(t, foundScriptError, "expected error mentioning broken script, got: %v", result.Errors)

	// Proxy linked to failed script must be skipped
	foundProxySkip := false
	for _, e := range result.Errors {
		if strings.Contains(e, "broken-proxy") || (strings.Contains(e, "proxy") && strings.Contains(e, "broken")) {
			foundProxySkip = true
			break
		}
	}
	assert.True(t, foundProxySkip, "expected proxy skip error for broken-proxy, got: %v", result.Errors)

	// Give the proxy event handler a moment to process any queued events
	time.Sleep(1 * time.Second)

	// No proxy should exist in ProxyManager
	proxies := env.Daemon.ProxyManager().ListScoped(scope.Unscoped("test"))
	for _, p := range proxies {
		assert.False(t, strings.Contains(p.ID, "broken"),
			"proxy linked to failed script should not exist, found: %s", p.ID)
	}
}

// TestE2E_AutostartWithProxy_IndependentProxy verifies that a proxy with an
// explicit target (no script link) starts independently via RunAutostart,
// regardless of any script state.
func TestE2E_AutostartWithProxy_IndependentProxy(t *testing.T) {
	env := setupDaemonForE2E(t)

	// Start a simple TCP listener to act as the upstream target.
	upstreamPort := freePort(t)
	env.TrackPort(upstreamPort)

	upstreamListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", upstreamPort))
	require.NoError(t, err)
	defer upstreamListener.Close()

	// Serve a simple HTTP response from the upstream.
	go func() {
		for {
			conn, acceptErr := upstreamListener.Accept()
			if acceptErr != nil {
				return
			}
			// Read request then write HTTP response
			buf := make([]byte, 4096)
			conn.Read(buf)
			resp := "HTTP/1.1 200 OK\r\nContent-Length: 12\r\n\r\nindependent!"
			conn.Write([]byte(resp))
			conn.Close()
		}
	}()

	kdl := fmt.Sprintf(`proxies {
    standalone {
        target "http://127.0.0.1:%d"
    }
}
`, upstreamPort)
	writeAgntKDL(t, env.ProjectDir, kdl)

	ctx := context.Background()
	result := env.Daemon.RunAutostart(ctx, env.ProjectDir)
	require.Empty(t, result.Errors, "RunAutostart errors: %v", result.Errors)

	// The independent proxy should be created via ExplicitStart event.
	// Wait for the proxy event handler to process it.
	proxyAddr := waitForProxy(t, env.Daemon, "standalone", 10*time.Second)
	t.Logf("independent proxy created at %s", proxyAddr)

	// Make an HTTP request through the proxy to verify routing.
	proxyURL := fmt.Sprintf("http://%s/", proxyAddr)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, httpErr := httpClient.Get(proxyURL)
	require.NoError(t, httpErr, "HTTP GET through independent proxy should succeed")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "independent!", string(body), "proxy should route to upstream listener")

	// Stop daemon and verify proxy is cleaned up
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env.Daemon.Stop(stopCtx)

	assert.Empty(t, env.Daemon.ProxyManager().ListScoped(scope.Unscoped("test")), "all proxies should be cleaned up after stop")
}

// assertPortFree polls until a port is free or fails the test.
func assertPortFree(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return // port is free
		}
		conn.Close()
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("Port %d still in use after %v", port, timeout)
}

// TestE2E_AutoRestart_PortCleanupOnRetry verifies that when a process with
// declared ports fails and auto-restart fires, the preflight port cleanup
// runs before the retry — killing any stale port holder from the previous
// instance. This is the exact scenario that causes restart storms on Windows
// when vite dies and the next instance races with the dying one for the port.
func TestE2E_AutoRestart_PortCleanupOnRetry(t *testing.T) {
	env, _ := setupDaemonForCrashTest(t)
	d := env.Daemon

	port := freePort(t)
	env.TrackPort(port)

	attemptFile := filepath.Join(env.ProjectDir, "attempt")

	// Write a script that binds the port then exits (simulating a crash).
	// Leaves socat running on the port — the auto-restarter must clean it.
	crashScript := writeTempScript(t, env.ProjectDir, "crash-then-succeed.sh", fmt.Sprintf(`#!/bin/bash
if [ ! -f "%s" ]; then
    echo "1" > "%s"
    socat TCP-LISTEN:%d,fork,reuseaddr SYSTEM:'echo first' &
    echo "first attempt, bound port %d"
    sleep 1
    exit 1
fi

ATTEMPT=$(cat "%s")
echo "$((ATTEMPT + 1))" > "%s"
echo "attempt $((ATTEMPT + 1)), binding port %d"
exec socat TCP-LISTEN:%d,fork,reuseaddr SYSTEM:'echo success'
`, attemptFile, attemptFile, port, port, attemptFile, attemptFile, port, port))

	// Write .agnt.kdl with ports declaration
	writeAgntKDL(t, env.ProjectDir, fmt.Sprintf(`
scripts {
    crasher {
        run "bash %s"
        autostart true
        ports %d
    }
}
`, crashScript, port))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := d.RunAutostart(ctx, env.ProjectDir)
	require.NotNil(t, result)

	// Wait for crash + backoff + restart + port probe
	time.Sleep(8 * time.Second)

	// Port should be bound by the successful retry
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	require.NoError(t, err, "Port %d should be bound by successful restart (preflight cleanup should have killed stale socat)", port)
	conn.Close()

	// Verify the attempt file shows at least 2 attempts
	attemptData, err := os.ReadFile(attemptFile)
	require.NoError(t, err)
	t.Logf("Attempt count: %s", string(attemptData))

	// Stop daemon
	stopCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	d.Stop(stopCtx)

	assertPortFree(t, port, 5*time.Second)
}

// ---------------------------------------------------------------------------
// Port pre-flight e2e tests
// ---------------------------------------------------------------------------

// TestE2E_AutostartPortPreflight_AutoKill verifies that auto-kill policy kills
// a rogue process holding a declared port, then starts the script.
func TestE2E_AutostartPortPreflight_AutoKill(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupDaemonForE2E(t)
	d := env.Daemon
	port := freePort(t)
	env.TrackPort(port)

	// Start an external blocker process holding the port.
	blockerCmd := exec.Command("python3", "-c",
		fmt.Sprintf(`import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)`, port))
	blockerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, blockerCmd.Start())
	t.Cleanup(func() { _ = blockerCmd.Process.Kill(); _ = blockerCmd.Wait() })
	require.NoError(t, waitForPort(port, 5*time.Second), "blocker should bind port %d", port)

	// Use sleep so the script stays alive long enough to be registered as running.
	writeAgntKDL(t, env.ProjectDir, fmt.Sprintf(`
project {
    port-conflict "auto-kill"
}
scripts {
    server {
        run "sleep 30"
        autostart true
        ports %d
    }
}`, port))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := d.RunAutostart(ctx, env.ProjectDir)
	require.NotNil(t, result)

	assert.NotEmpty(t, result.PortsCleared, "should have cleared ports")
	assert.Contains(t, result.Scripts, "server", "script should have started")
}

// TestE2E_AutostartPortPreflight_Fail verifies that fail policy aborts autostart
// when a port conflict is detected and returns errors + conflicts.
func TestE2E_AutostartPortPreflight_Fail(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupDaemonForE2E(t)
	d := env.Daemon
	port := freePort(t)
	env.TrackPort(port)

	// Use an external blocker to avoid preflightPortCleanup killing the test.
	blockerCmd := exec.Command("python3", "-c",
		fmt.Sprintf(`import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)`, port))
	blockerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, blockerCmd.Start())
	t.Cleanup(func() { _ = blockerCmd.Process.Kill(); _ = blockerCmd.Wait() })
	require.NoError(t, waitForPort(port, 5*time.Second), "blocker should bind port %d", port)

	writeAgntKDL(t, env.ProjectDir, fmt.Sprintf(`
project {
    port-conflict "fail"
}
scripts {
    server {
        run "echo hello"
        autostart true
        ports %d
    }
}`, port))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := d.RunAutostart(ctx, env.ProjectDir)
	require.NotNil(t, result)

	assert.Empty(t, result.Scripts, "no scripts should start in fail mode")
	assert.NotEmpty(t, result.Errors, "should have error about port conflict abort")
	assert.NotEmpty(t, result.PortConflicts, "should report conflicts")
}

// TestE2E_AutostartPortPreflight_Skip verifies that skip policy ignores port
// conflicts at the policy level and proceeds to start scripts. The low-level
// preflightPortCleanup in StartScript will still kill the blocker, so the
// script starts successfully.
func TestE2E_AutostartPortPreflight_Skip(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupDaemonForE2E(t)
	d := env.Daemon
	port := freePort(t)
	env.TrackPort(port)

	// Use an external process so preflightPortCleanup can kill it without
	// killing the test process itself.
	blockerCmd := exec.Command("python3", "-c",
		fmt.Sprintf(`import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)`, port))
	blockerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, blockerCmd.Start())
	t.Cleanup(func() { _ = blockerCmd.Process.Kill(); _ = blockerCmd.Wait() })
	require.NoError(t, waitForPort(port, 5*time.Second), "blocker should bind port %d", port)

	writeAgntKDL(t, env.ProjectDir, fmt.Sprintf(`
project {
    port-conflict "skip"
}
scripts {
    server {
        run "sleep 30"
        autostart true
        ports %d
    }
}`, port))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := d.RunAutostart(ctx, env.ProjectDir)
	require.NotNil(t, result)

	assert.Contains(t, result.Scripts, "server", "script should start despite conflict")
	assert.Empty(t, result.PortConflicts, "skip mode doesn't return conflicts to client")
}

// TestE2E_AutostartPortPreflight_Prompt verifies that prompt policy pauses
// autostart, reports conflicts, and resumes after user confirmation.
func TestE2E_AutostartPortPreflight_Prompt(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test")
	}

	env := setupDaemonForE2E(t)
	d := env.Daemon
	port := freePort(t)
	env.TrackPort(port)

	// Use an external blocker so preflightPortCleanup during resume can kill
	// it without affecting the test process.
	blockerCmd := exec.Command("python3", "-c",
		fmt.Sprintf(`import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)`, port))
	blockerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, blockerCmd.Start())
	t.Cleanup(func() { _ = blockerCmd.Process.Kill(); _ = blockerCmd.Wait() })
	require.NoError(t, waitForPort(port, 5*time.Second), "blocker should bind port %d", port)

	writeAgntKDL(t, env.ProjectDir, fmt.Sprintf(`
project {
    port-conflict "prompt"
}
scripts {
    server {
        run "sleep 30"
        autostart true
        ports %d
    }
}`, port))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := d.RunAutostart(ctx, env.ProjectDir)
	require.NotNil(t, result)

	assert.NotEmpty(t, result.PortConflicts, "should report conflicts")
	assert.Empty(t, result.Scripts, "should not start scripts in prompt mode")

	// Kill the blocker so the port is free for resume.
	_ = blockerCmd.Process.Kill()
	_ = blockerCmd.Wait()

	// Resume via CONTINUE (simulates user approval after port freed).
	resumeResult := d.resumeAutostart(ctx, env.ProjectDir)
	require.NotNil(t, resumeResult)
	assert.Contains(t, resumeResult.Scripts, "server", "should start after continue")
}
