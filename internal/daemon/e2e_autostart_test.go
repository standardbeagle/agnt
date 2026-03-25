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
