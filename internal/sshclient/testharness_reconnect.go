//go:build !windows

// This build constraint is deliberate, not incidental: a real sshd is an
// inherently Unix-shaped test dependency (host-key perms, fork-per-connection
// process model and POSIX SIGSTOP/SIGCONT). The portable hard-close harness
// lives in testharness_hardclose_test.go; only this real-sshd freeze fixture
// remains behind the Unix boundary.

package sshclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/standardbeagle/agnt/internal/platform"
)

// This file backs the Unix real-sshd freeze simulation. It is intentionally
// NOT a _test.go file: a _test.go file is
// only visible within this package's own test binary, but 09c/09f may need
// the harness from a different package's tests (e.g. a soak test living
// outside internal/sshclient). Production code cannot construct a
// *testing.T, so every constructor here takes one as a compile-time fence —
// the same pattern internal/daemon/test_helpers.go uses for NewForTest, per
// the "Test startup contract" section of .claude/rules/daemon-architecture.md.

// ErrSSHDNotFound is returned by NewSSHDFreezeHarness when no "sshd" (or its
// companion "ssh-keygen") binary is on PATH. Callers must t.Skip on this
// error, per acceptance criterion 1 — the freeze-mode drop simulation is
// skipped, never failed, when the environment lacks a real sshd.
var ErrSSHDNotFound = errors.New("sshclient: sshd (or ssh-keygen) not found on PATH")

// SSHDFreezeHarness manages a real sshd subprocess to simulate drop mode
// (b): a soft black-hole where the TCP connection stays ESTABLISHED (no RST,
// no FIN) but the peer answers nothing. A hard TCP close cannot reproduce
// this — the kernel on the harness side would still ack or reset a genuinely
// dead process. SIGSTOP freezes the real sshd process (and its already-
// forked per-connection children) without touching the socket, which is
// exactly the "frozen but connected" shape a reconnect state machine must
// detect via a bounded liveness probe rather than a transport-level error.
type SSHDFreezeHarness struct {
	cmd     *exec.Cmd
	addr    string
	dir     string
	logPath string
}

// Log returns what the sshd subprocess has logged so far (LogLevel VERBOSE).
// Put it in the failure message of any dial against the harness: the client
// only ever learns "unable to authenticate", the server log says why.
func (h *SSHDFreezeHarness) Log() string {
	data, err := os.ReadFile(h.logPath)
	if err != nil {
		return fmt.Sprintf("(sshd log unavailable: %v)", err)
	}
	if len(data) == 0 {
		return "(sshd logged nothing)"
	}
	return string(data)
}

// NewSSHDFreezeHarness generates a throwaway host key + authorized_keys pair,
// starts a real sshd subprocess listening on an ephemeral localhost port with
// that identity, and returns once it is accepting TCP connections.
//
// Returns ErrSSHDNotFound (not a fatal test failure) when sshd or ssh-keygen
// is absent from PATH — callers must t.Skip on that per acceptance
// criterion 1. clientAuthorizedKey is the public key the harness will accept
// for publickey auth (pass the pub half of generateClientKey's output).
func NewSSHDFreezeHarness(t *testing.T, clientAuthorizedKey ssh.PublicKey) (*SSHDFreezeHarness, error) {
	t.Helper()
	sshdPath, err := exec.LookPath("sshd")
	if err != nil {
		return nil, ErrSSHDNotFound
	}
	keygenPath, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return nil, ErrSSHDNotFound
	}

	dir := t.TempDir()
	hostKeyPath := filepath.Join(dir, "host_key")
	genCmd := exec.Command(keygenPath, "-t", "ed25519", "-f", hostKeyPath, "-N", "", "-q")
	if out, err := genCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sshclient: generating sshd host key: %w (%s)", err, out)
	}

	authorizedKeysPath := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authorizedKeysPath, ssh.MarshalAuthorizedKey(clientAuthorizedKey), 0o600); err != nil {
		return nil, fmt.Errorf("sshclient: writing authorized_keys: %w", err)
	}

	// Reserve a free port ourselves rather than relying on sshd's own
	// "Port 0" support (inconsistent across distros, and doesn't hand the
	// bound port back deterministically without scraping logs).
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sshclient: reserving port: %w", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	configPath := filepath.Join(dir, "sshd_config")
	pidPath := filepath.Join(dir, "sshd.pid")
	configContent := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
AuthorizedKeysFile %s
PidFile %s
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
LogLevel VERBOSE
`, port, hostKeyPath, authorizedKeysPath, pidPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		return nil, fmt.Errorf("sshclient: writing sshd_config: %w", err)
	}

	// -e sends sshd's log to stderr; keep it in a file so a failed dial can
	// say why the server refused (see Log) instead of only what the client saw.
	logPath := filepath.Join(dir, "sshd.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("sshclient: creating sshd log: %w", err)
	}
	cmd := exec.Command(sshdPath, "-f", configPath, "-D", "-e")
	cmd.Stderr = logFile
	err = cmd.Start()
	logFile.Close() // the child holds its own descriptor
	if err != nil {
		return nil, fmt.Errorf("sshclient: starting sshd: %w", err)
	}

	h := &SSHDFreezeHarness{
		cmd:     cmd,
		addr:    net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		dir:     dir,
		logPath: logPath,
	}
	if err := h.waitForListen(5 * time.Second); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	t.Cleanup(func() { h.Kill() })
	return h, nil
}

// Addr returns the "host:port" dial target for the sshd subprocess.
func (h *SSHDFreezeHarness) Addr() string { return h.addr }

// Freeze sends SIGSTOP to the sshd process AND every descendant it has
// forked so far (its privilege-separation monitor child, and the per-session
// child once a connection is authenticated), deterministically producing the
// "frozen but connected" black-hole: TCP stays established, nothing answers.
//
// Freezing only the main listener process is not sufficient: sshd forks a
// child per connection immediately after accept, and that child calls
// setpgid on itself (its pgid differs from the main process's), so it keeps
// answering an already-established connection's requests even while the
// parent is stopped. Every descendant must be stopped individually.
//
// sshd's post-auth privilege-drop fork can still be in flight for a brief
// window right after a client's handshake returns (the client sees auth
// success before the server has necessarily finished forking its
// unprivileged worker for the connection). A single /proc snapshot taken
// immediately after Dial() can therefore miss that worker, stop everything
// it *did* find, and then watch the late-arriving worker answer requests
// completely unfrozen. Freeze re-scans and stops any newly-discovered
// descendant across a short, bounded settle window, converging as soon as
// two consecutive scans agree — so callers get a deterministic call with no
// sleep of their own, and the settle window is capped, not open-ended.
func (h *SSHDFreezeHarness) Freeze() error {
	stopped := make(map[int]bool)
	var lastSeen map[int]bool
	stableRuns := 0
	deadline := time.Now().Add(freezeSettleWindow)
	for {
		pids, err := descendantPIDs(h.cmd.Process.Pid)
		if err != nil {
			return fmt.Errorf("sshclient: enumerating sshd descendants: %w", err)
		}
		seen := make(map[int]bool, len(pids))
		for _, pid := range pids {
			seen[pid] = true
			if !stopped[pid] {
				if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil && !errors.Is(err, syscall.ESRCH) {
					return fmt.Errorf("sshclient: stopping pid %d: %w", pid, err)
				}
				stopped[pid] = true
			}
		}
		if mapsEqual(seen, lastSeen) {
			stableRuns++
		} else {
			stableRuns = 0
		}
		lastSeen = seen
		// Require several consecutive agreeing scans, not just one: a
		// single match can still land in the gap right before a
		// still-in-flight fork completes. Multiple agreeing scans across
		// the poll interval make that gap require an implausibly precise
		// hit to slip through undetected.
		if stableRuns >= freezeStableScansRequired || time.Now().After(deadline) {
			return nil
		}
		time.Sleep(freezeSettlePoll)
	}
}

// freezeSettleWindow bounds how long Freeze will keep re-scanning for
// late-forked descendants; freezeSettlePoll is the interval between scans;
// freezeStableScansRequired is how many consecutive scans must agree before
// Freeze treats the descendant set as settled.
const (
	freezeSettleWindow        = 500 * time.Millisecond
	freezeSettlePoll          = 25 * time.Millisecond
	freezeStableScansRequired = 3
)

func mapsEqual(a, b map[int]bool) bool {
	if b == nil || len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// Resume sends SIGCONT to the same process set Freeze() would target,
// letting a frozen sshd (and its children) answer again.
func (h *SSHDFreezeHarness) Resume() error {
	pids, err := descendantPIDs(h.cmd.Process.Pid)
	if err != nil {
		return fmt.Errorf("sshclient: enumerating sshd descendants: %w", err)
	}
	return signalAll(pids, syscall.SIGCONT)
}

// Kill terminates the sshd subprocess and reaps it. Safe to call multiple
// times and safe to call while frozen (SIGCONT is delivered first — on
// Linux SIGKILL alone is sufficient to terminate a stopped process, but
// continuing first keeps behavior identical on platforms where that isn't
// guaranteed, and lets forked children exit on their own before the parent
// disappears).
func (h *SSHDFreezeHarness) Kill() error {
	if h.cmd.Process == nil {
		return nil
	}
	if pids, err := descendantPIDs(h.cmd.Process.Pid); err == nil {
		signalAll(pids, syscall.SIGCONT)
	}
	if err := h.cmd.Process.Kill(); err != nil {
		return err
	}
	h.cmd.Wait()
	return nil
}

// descendantPIDs returns rootPID plus every process transitively forked from
// it, from one process-table snapshot (sshd's fork-per-connection model is
// what makes a listener-only SIGSTOP insufficient in the first place).
//
// The snapshot is platform.Scan: /proc on Linux, sysctl on macOS. This used to
// read /proc directly, so on macOS Freeze failed with "open /proc: no such
// file or directory". On a Unix with no Scan implementation the error is
// returned and the caller's test fails loudly rather than freezing nothing.
func descendantPIDs(rootPID int) ([]int, error) {
	procs, err := platform.Scan()
	if err != nil {
		return nil, err
	}
	children := make(map[int][]int)
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p.PID)
	}
	result := []int{rootPID}
	queue := []int{rootPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			result = append(result, child)
			queue = append(queue, child)
		}
	}
	return result, nil
}

// signalAll sends sig to every pid, collecting the first non-ESRCH error but
// continuing to signal the rest. ESRCH ("no such process") is expected, not
// exceptional: a short-lived per-connection child can legitimately exit
// between enumeration and signaling (e.g. the privileged monitor after its
// unprivileged worker takes over), and that race is not a harness failure.
func signalAll(pids []int, sig syscall.Signal) error {
	var firstErr error
	for _, pid := range pids {
		if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// waitForListen blocks until sshd is not just accepting TCP connections but
// genuinely servicing them: it reads the "SSH-2.0-..." version banner off a
// probe connection rather than merely connecting and closing. A bare
// connect-then-close only proves the listen backlog exists; sshd forks a
// handler per accepted connection, and there is a real (if brief) window
// between the listener accepting and that handler being scheduled to write
// its banner. A caller that dials before the banner would actually flow
// intermittently sees a reset/refused connection despite this check having
// "passed" moments earlier — reading the banner closes that window.
func (h *SSHDFreezeHarness) waitForListen(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := probeBanner(h.addr); err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		return nil
	}
	return fmt.Errorf("sshclient: sshd did not start servicing connections on %s within %s: %w", h.addr, timeout, lastErr)
}

func probeBanner(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != "SSH-" {
		return fmt.Errorf("sshclient: unexpected banner prefix %q", buf)
	}
	return nil
}
