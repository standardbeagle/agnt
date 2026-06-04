//go:build unix

package daemon

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

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

func TestDetectPortConflicts_NoConflicts(t *testing.T) {
	t.Parallel()
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{19876}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_WithBlocker(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	managedPIDs := map[int]bool{os.Getpid(): true}
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, managedPIDs)
	assert.Empty(t, conflicts, "should skip managed PIDs")
}

func TestDetectPortConflicts_NoPorts(t *testing.T) {
	t.Parallel()
	scripts := map[string]*config.ScriptConfig{
		"lib": {Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_MultiplePortsMultipleScripts(t *testing.T) {
	t.Parallel()
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

func TestKillPortBlockers_FreesPort(t *testing.T) {
	t.Parallel()
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

	// Verify port is free
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	assert.NoError(t, err, "port should be free after kill")
	if ln2 != nil {
		ln2.Close()
	}
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
	hub := NewAlertHub()
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

// capturingMCPSink records SendAlert calls verbatim so tests can assert on
// the level + message pair. Distinct from the counter-only stubMCPSink in
// alert_hub_stress_test.go because here we need to verify the Silent
// Failure Prohibition contract requires the warning text reaches the sink.
type capturingMCPSink struct {
	mu    sync.Mutex
	calls []capturedAlert
}

type capturedAlert struct {
	level   string
	message string
}

func (s *capturingMCPSink) SendAlert(level, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, capturedAlert{level, message})
	return nil
}

func (s *capturingMCPSink) snapshot() []capturedAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedAlert, len(s.calls))
	copy(out, s.calls)
	return out
}

// TestKillPortBlockers_WindowsPIDFailure_SurfacesWarning asserts the
// Silent Failure Prohibition contract: when platform.KillWindowsPID
// fails (here: because we're not on WSL OR because the PID is bogus),
// the failure must reach AlertHub.Deliver as a warning so the AI agent
// sees it. Runs everywhere because KillWindowsPID always errors when
// IsWSL() is false (covered by killwindowspid_unix_test.go), and on
// WSL hosts without taskkill.exe the LookPath failure also errors.
func TestKillPortBlockers_WindowsPIDFailure_SurfacesWarning(t *testing.T) {
	t.Parallel()
	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	hub := NewAlertHub()
	sink := &capturingMCPSink{}
	hub.AddMCPSink(sink)

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

	calls := sink.snapshot()
	require.NotEmpty(t, calls, "AlertHub must receive at least one Deliver call for the failure")
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
