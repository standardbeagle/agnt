//go:build unix

package daemon

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/platform"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startEphemeralPortBlocker starts a python subprocess that binds an
// OS-assigned 127.0.0.1 port, prints it on stdout, and holds it for 60s.
// Returns the child PID and the port it actually bound; a t.Cleanup kills the
// child. Unlike reserve-then-rebind (net.Listen :0 → read port → Close → let
// something else bind the same number), the child owns the port from the
// moment it binds, so there is no TOCTOU window for another parallel test's
// ephemeral :0 allocation to steal it. This is the root-cause fix for the
// suite's intermittent "bind: address already in use" flakes.
func startEphemeralPortBlocker(t *testing.T) (pid int, port int) {
	t.Helper()
	cmd := exec.Command("python3", "-u", "-c",
		"import socket,time\n"+
			"s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)\n"+
			"s.bind(('127.0.0.1',0)); s.listen(1)\n"+
			"print(s.getsockname()[1], flush=True)\n"+
			"time.sleep(60)")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err, "blocker must report its bound port")
	port, err = strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err, "blocker port line %q", line)
	require.Greater(t, port, 0)
	return cmd.Process.Pid, port
}

// linuxProbe returns a portProbe that reports the given Linux-side PIDs for any
// port that has an entry, and no PIDs otherwise. It replaces the real /proc +
// netstat.exe scan in unit tests so conflict classification is deterministic —
// the real scan intermittently misses our own listener on WSL and falls back to
// netstat.exe, which returned a foreign Windows PID and flaked these tests.
func linuxProbe(byPort map[int][]int) portProbe {
	return func(_ context.Context, port int) (linuxPIDs, windowsPIDs []int) {
		return byPort[port], nil
	}
}

// emptyProbe reports no PID on any port.
func emptyProbe() portProbe {
	return func(_ context.Context, _ int) (linuxPIDs, windowsPIDs []int) { return nil, nil }
}

func TestDetectPortConflicts_NoConflicts(t *testing.T) {
	t.Parallel()
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{19876}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil, emptyProbe())
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_UnmanagedPIDReported(t *testing.T) {
	t.Parallel()
	const port = 19876
	const ownerPID = 4242
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil,
		linuxProbe(map[int][]int{port: {ownerPID}}))
	require.Len(t, conflicts, 1)
	assert.Equal(t, "api", conflicts[0].ScriptName)
	assert.Equal(t, port, conflicts[0].Port)
	assert.Contains(t, conflicts[0].PIDs, ownerPID)
	assert.Equal(t, []int{ownerPID}, conflicts[0].LinuxPIDs)
}

func TestDetectPortConflicts_ManagedPIDSkipped(t *testing.T) {
	t.Parallel()
	const port = 19876
	const managedPID = 4242
	managedPIDs := map[int]bool{managedPID: true}
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, managedPIDs,
		linuxProbe(map[int][]int{port: {managedPID}}))
	assert.Empty(t, conflicts, "should skip managed PIDs")
}

func TestDetectPortConflicts_NoPorts(t *testing.T) {
	t.Parallel()
	scripts := map[string]*config.ScriptConfig{
		"lib": {Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil, emptyProbe())
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_MultiplePortsMultipleScripts(t *testing.T) {
	t.Parallel()
	scripts := map[string]*config.ScriptConfig{
		"api":      {Ports: []int{19801}, Autostart: true},
		"frontend": {Ports: []int{19802}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil,
		linuxProbe(map[int][]int{19801: {5001}, 19802: {5002}}))
	assert.Len(t, conflicts, 2)
}

func TestKillPortHoldersGuarded_ProtectsSelf(t *testing.T) {
	// No t.Parallel(): exercises the real port scan + kill path.
	// The guard's whole job: a port held by THIS process (stand-in for the
	// daemon's own proxy listeners) must never be killed, because
	// KillProcessByPort re-discovers holders at fire time with no exclusion
	// list. Without the guard this test would SIGTERM the test binary.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	killed, protected, err := killPortHoldersGuarded(context.Background(), pm, port)
	require.NoError(t, err)
	assert.Empty(t, killed, "must not kill anything when self holds the port")
	assert.Contains(t, protected, os.Getpid(), "self PID must be reported as protected")

	// Listener must still be alive — a successful dial proves the socket survived.
	conn, dialErr := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	assert.NoError(t, dialErr, "self-held listener must survive the guarded kill")
	if conn != nil {
		conn.Close()
	}
}

func TestKillPortBlockers_FreesPort(t *testing.T) {
	// No t.Parallel(): starts a real process and kills by port; PID-reuse
	// under high concurrency kills unrelated processes.
	if os.Getuid() == 0 {
		t.Skip("don't run as root")
	}

	// The blocker self-assigns its port (binds :0 and reports it back), so it
	// owns the port from the moment it binds — no reserve-then-rebind TOCTOU for
	// another parallel test's ephemeral allocation to steal.
	blockerPID, port := startEphemeralPortBlocker(t)

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	conflicts := []PortConflict{{
		ScriptName: "test", Port: port, PIDs: []int{blockerPID},
	}}

	results := killPortBlockers(context.Background(), pm, nil, conflicts)
	require.Len(t, results, 1)
	assert.True(t, results[0].Killed)
	assert.Empty(t, results[0].Error)

	// Verify the blocker released the port.
	//
	// Do NOT assert this by binding the port: it is an ephemeral port, so the
	// instant the blocker frees it any other parallel test calling net.Listen on
	// :0 may be handed it, and our bind fails with EADDRINUSE through no fault of
	// the kill. That is what made this test flaky under load. Ask the question we
	// actually mean — is the blocker still holding it? — instead.
	assert.Eventually(t, func() bool {
		for _, pid := range config.FindPIDsByPort(context.Background(), port) {
			if pid == blockerPID {
				return false
			}
		}
		return true
	}, 30*time.Second, 10*time.Millisecond, "blocker still holds the port after kill")
}

func TestKillPortBlockers_NonExistentPID(t *testing.T) {
	t.Parallel()
	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	conflicts := []PortConflict{{
		ScriptName: "test", Port: ephemeralPort(t), PIDs: []int{1 << 29}, // non-existent PID
	}}
	results := killPortBlockers(context.Background(), pm, nil, conflicts)
	require.Len(t, results, 1)
	assert.True(t, results[0].Killed)
}

// TestKillPortBlockers_WindowsPID_RoutesToTaskkill asserts that a conflict
// tagged with WindowsPIDs routes the kill through platform.KillWindowsPID
// (not syscall.Kill). The PID is non-existent so taskkill.exe will fail;
// the kill error must surface in KillResult.Error and as a warning event
// — never silently. Skipped on non-WSL hosts where taskkill.exe is
// unavailable (the routing branch is unreachable in production there).
func TestKillPortBlockers_WindowsPID_RoutesToTaskkill(t *testing.T) {
	t.Parallel()
	if !platform.IsWSL() {
		t.Skip("WSL-only routing test")
	}
	if _, err := exec.LookPath("taskkill.exe"); err != nil {
		t.Skip("taskkill.exe not on PATH (WSL interop disabled?)")
	}

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	port := ephemeralPort(t)
	hub := NewEventHub()
	conflicts := []PortConflict{{
		ScriptName:  "test",
		Port:        port,
		PIDs:        []int{2147483646},
		WindowsPIDs: []int{2147483646}, // explicit Windows tag forces taskkill route
	}}
	results := killPortBlockers(context.Background(), pm, hub, conflicts)
	require.Len(t, results, 1)
	// Port is unbound, so waitPortFree returns true → Killed=true is OK.
	// The contract being tested is that the kill error surfaces in Error,
	// not silently swallowed.
	assert.NotEmpty(t, results[0].Error, "Windows-side kill failure must surface, never silent")
	assert.Contains(t, results[0].Error, "windows pid", "error must identify the Windows PID branch")
}

// capturingBroadcaster records BroadcastAlertToast calls verbatim so tests can
// assert the Silent Failure Prohibition contract: a kill failure must surface as
// a warning toast (the EventHub delivery surface) rather than being swallowed.
type capturingBroadcaster struct {
	mu    sync.Mutex
	calls []capturedAlert
}

type capturedAlert struct {
	level   string
	message string
}

func (s *capturingBroadcaster) BroadcastAlertToast(toastType, _ string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, capturedAlert{toastType, message})
}

func (s *capturingBroadcaster) snapshot() []capturedAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedAlert, len(s.calls))
	copy(out, s.calls)
	return out
}

// TestKillPortBlockers_WindowsPIDFailure_SurfacesWarning asserts the
// Silent Failure Prohibition contract: when platform.KillWindowsPID
// fails (here: because we're not on WSL OR because the PID is bogus),
// the failure must reach EventHub.Deliver and surface as a warning toast
// so it is not silently swallowed. Runs everywhere because KillWindowsPID
// always errors when IsWSL() is false (covered by killwindowspid_unix_test.go),
// and on WSL hosts without taskkill.exe the LookPath failure also errors.
func TestKillPortBlockers_WindowsPIDFailure_SurfacesWarning(t *testing.T) {
	t.Parallel()
	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	hub := NewEventHub()
	bc := &capturingBroadcaster{}
	hub.SetProxyBroadcaster(bc)

	port := ephemeralPort(t)
	conflicts := []PortConflict{{
		ScriptName:  "test",
		Port:        port,
		PIDs:        []int{2147483646},
		WindowsPIDs: []int{2147483646}, // forces taskkill route
	}}
	results := killPortBlockers(context.Background(), pm, hub, conflicts)
	require.Len(t, results, 1)
	assert.NotEmpty(t, results[0].Error, "kill failure must populate KillResult.Error")

	calls := bc.snapshot()
	require.NotEmpty(t, calls, "EventHub must surface at least one toast for the failure")
	assert.Equal(t, "warning", calls[0].level, "kill failure must be warning-severity")
	assert.Contains(t, calls[0].message, "Windows-side", "alert must identify the source")
}

// TestKillPortBlockers_LegacyPIDsTreatedAsLinux asserts back-compat: a
// PortConflict built without LinuxPIDs / WindowsPIDs (the pre-tagged
// shape) routes through the Linux kill path. Guards against silent
// regressions for callers that construct PortConflict literally without
// using detectPortConflicts. Runs everywhere — uses non-existent PID
// and unbound port.
func TestKillPortBlockers_LegacyPIDsTreatedAsLinux(t *testing.T) {
	t.Parallel()
	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	conflicts := []PortConflict{{
		ScriptName: "test",
		Port:       ephemeralPort(t),
		PIDs:       []int{1 << 29}, // legacy shape: PIDs only
	}}
	results := killPortBlockers(context.Background(), pm, nil, conflicts)
	require.Len(t, results, 1)
	assert.True(t, results[0].Killed)
	assert.Empty(t, results[0].Error)
}
