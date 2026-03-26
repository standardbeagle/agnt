//go:build e2e

package daemon

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eEnv holds the shared state for an e2e test run.
type e2eEnv struct {
	BinaryPath string // path to built agnt binary
	Daemon     *Daemon
	SocketPath string
	ProjectDir string // temp dir acting as project root
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

// killTree sends SIGKILL to a process group and all descendants.
// Used in test cleanup to ensure nothing leaks.
func killTree(pid int) {
	// Kill the process group first (covers processes started with Setpgid)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// Also walk descendants in case any escaped the group
	descendants := getDescendants(pid)
	for _, d := range descendants {
		if p, err := os.FindProcess(d); err == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGKILL)
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
	goodPidFile := filepath.Join(env.ProjectDir, "good.pid")
	pnpmPath := testdataPath(t, "fake-pnpm-watch.sh")

	badPort := freePort(t)

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

	// Verify ScriptRegistry shows Stopped or Failed (killed processes may exit non-zero)
	sr := env.Daemon.ScriptRegistry()
	entry, ok := sr.Get("api", env.ProjectDir)
	require.True(t, ok)
	state := entry.State()
	assert.True(t, state == script.StateStopped || state == script.StateFailed,
		"script should be Stopped or Failed after last session leaves, got %s", state)

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
