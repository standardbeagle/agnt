//go:build unix

package daemon

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/require"
)

// procStopped reports whether pid is no longer a live, running process — i.e.
// it is gone, or it is a zombie (SIGKILLed and awaiting reap by its parent).
// Because these tests are the child's parent, a killed child lingers as a
// zombie until t.Cleanup's Wait reaps it; kill(pid,0) still succeeds on a
// zombie, so it cannot distinguish "killed" from "still sleeping". The /proc
// state byte can: 'Z' means the process was terminated.
func procStopped(t *testing.T, pid int) bool {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true // entry gone entirely
	}
	if close := strings.LastIndexByte(string(data), ')'); close >= 0 && close+2 < len(data) {
		return data[close+2] == 'Z'
	}
	return false
}

// startBlocker launches a python subprocess that binds an OS-assigned
// 127.0.0.1 port, prints it on stdout, and holds it. When ignoreSigterm is
// true the child installs SIG_IGN for SIGTERM so only SIGKILL can stop it —
// exercising the escalation ceiling. Returns the child PID and bound port; a
// t.Cleanup SIGKILLs the child.
func startBlocker(t *testing.T, ignoreSigterm bool) (pid, port int) {
	t.Helper()
	prog := "import socket,time,signal\n"
	if ignoreSigterm {
		prog += "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
	}
	prog += "s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)\n" +
		"s.bind(('127.0.0.1',0)); s.listen(1)\n" +
		"print(s.getsockname()[1], flush=True)\n" +
		"time.sleep(120)"
	cmd := exec.Command("python3", "-u", "-c", prog)
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

// TestKillProcessByPort_FastEscalationOnPromptExit pins the fix: a process that
// dies promptly on SIGTERM must not be held hostage by the 3s grace ceiling.
// Before the poll-until-dead fix, killProcesses slept a flat 3s between SIGTERM
// and SIGKILL escalation regardless of a prompt exit, so this took >3s.
func TestKillProcessByPort_FastEscalationOnPromptExit(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("don't run as root")
	}
	_, port := startBlocker(t, false) // default SIGTERM disposition: terminate

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())
	start := time.Now()
	killed, err := pm.KillProcessByPort(context.Background(), port)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotEmpty(t, killed, "the blocker holding the port must be signalled")

	// A python process with the default SIGTERM disposition exits within tens of
	// ms. 1.5s is generous headroom yet still fails the old flat-3s wait.
	require.Less(t, elapsed, 1500*time.Millisecond,
		"prompt SIGTERM exit must not wait out the 3s grace ceiling; took %v", elapsed)
}

// TestKillProcessByPort_SigtermIgnorerStillKilledAtCeiling is the mutation guard
// for the fix: a process that genuinely ignores SIGTERM must still be SIGKILLed
// at the 3s ceiling. A fix that got fast by skipping escalation would leave this
// process alive — the assertions below would then fail.
func TestKillProcessByPort_SigtermIgnorerStillKilledAtCeiling(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("don't run as root")
	}
	pid, port := startBlocker(t, true) // ignores SIGTERM; only SIGKILL stops it

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())
	start := time.Now()
	killed, err := pm.KillProcessByPort(context.Background(), port)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotEmpty(t, killed, "the SIGTERM-ignoring blocker must be signalled")

	// Escalation cannot fire before the grace ceiling, so this genuinely takes
	// ~3s. Assert the process is actually dead — proves SIGKILL escalation ran.
	require.GreaterOrEqual(t, elapsed, 3*time.Second,
		"a SIGTERM-ignorer should only die at the escalation ceiling; took %v", elapsed)
	require.True(t, procStopped(t, pid),
		"SIGTERM-ignoring process must be SIGKILLed at the ceiling, but pid %d is still running", pid)
}
