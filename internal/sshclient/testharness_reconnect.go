//go:build !windows

// This build constraint is deliberate, not incidental: a real sshd is an
// inherently Unix-shaped test dependency (host-key perms, fork-per-connection
// process model, POSIX SIGSTOP/SIGCONT), and `agnt ssh` itself is already
// unsupported on Windows in v1 (see .claude/rules/daemon-architecture.md
// § Cross-Platform Mandate and lesson 8 in
// .claude/rules/lessons-ssh-transport.md). A Windows equivalent of this
// harness is out of scope until remote-ssh grows Windows support.

package sshclient

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// This file backs the reconnect state machine (task 09c) and its soak test
// (09f) with two independent, deterministically-triggered connection-drop
// simulations. It is intentionally NOT a _test.go file: a _test.go file is
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

// HardCloseHarness is an in-process SSH server that simulates drop mode (a):
// a hard TCP close of the underlying connection, as if the peer process
// died, an interface flapped, or a NAT/firewall reset the stream. Unlike
// fixtureServer's stop() (which only closes the listener and leaves
// already-accepted connections alone), Drop() severs every connection
// currently in flight, so tests trigger the drop explicitly rather than
// racing a sleep against the accept loop.
type HardCloseHarness struct {
	listener net.Listener
	hostKey  ssh.Signer

	mu    sync.Mutex
	conns []net.Conn
}

// NewHardCloseHarness starts the in-process server on an ephemeral localhost
// port and registers cleanup via t.Cleanup.
func NewHardCloseHarness(t *testing.T) *HardCloseHarness {
	t.Helper()
	signer, err := newEphemeralHostKey()
	if err != nil {
		t.Fatalf("sshclient: generating harness host key: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sshclient: listening for hard-close harness: %v", err)
	}
	h := &HardCloseHarness{listener: l, hostKey: signer}
	go h.acceptLoop()
	t.Cleanup(h.Stop)
	return h
}

// Addr returns the "host:port" dial target for this harness.
func (h *HardCloseHarness) Addr() string { return h.listener.Addr().String() }

// Drop hard-closes every connection accepted so far — the deterministic
// trigger for drop mode (a). It does not stop the listener, so a caller that
// reconnects after Drop will be accepted again, exactly like a real sshd
// coming back up on the same address.
func (h *HardCloseHarness) Drop() {
	h.mu.Lock()
	conns := h.conns
	h.conns = nil
	h.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// Stop shuts down the listener and closes any remaining connections.
func (h *HardCloseHarness) Stop() {
	h.listener.Close()
	h.Drop()
}

func (h *HardCloseHarness) acceptLoop() {
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}
		h.mu.Lock()
		h.conns = append(h.conns, conn)
		h.mu.Unlock()
		go h.handshakeAndServe(conn)
	}
}

func (h *HardCloseHarness) handshakeAndServe(conn net.Conn) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(h.hostKey)
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		channel, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range requests {
				if req.WantReply {
					req.Reply(true, nil)
				}
			}
		}()
		_ = channel
	}
}

// SSHDFreezeHarness manages a real sshd subprocess to simulate drop mode
// (b): a soft black-hole where the TCP connection stays ESTABLISHED (no RST,
// no FIN) but the peer answers nothing. A hard TCP close cannot reproduce
// this — the kernel on the harness side would still ack or reset a genuinely
// dead process. SIGSTOP freezes the real sshd process (and its already-
// forked per-connection children) without touching the socket, which is
// exactly the "frozen but connected" shape a reconnect state machine must
// detect via a bounded liveness probe rather than a transport-level error.
type SSHDFreezeHarness struct {
	cmd  *exec.Cmd
	addr string
	dir  string
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
LogLevel QUIET
`, port, hostKeyPath, authorizedKeysPath, pidPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		return nil, fmt.Errorf("sshclient: writing sshd_config: %w", err)
	}

	cmd := exec.Command(sshdPath, "-f", configPath, "-D", "-e")
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sshclient: starting sshd: %w", err)
	}

	h := &SSHDFreezeHarness{
		cmd:  cmd,
		addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		dir:  dir,
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
// it, discovered via a single /proc walk (Linux-specific, matching this
// file's build tag — sshd's fork-per-connection model is what makes a
// listener-only SIGSTOP insufficient in the first place).
func descendantPIDs(rootPID int) ([]int, error) {
	children, err := procChildrenByParent()
	if err != nil {
		return nil, err
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

// procChildrenByParent scans /proc/<pid>/stat for every process on the host
// and returns a parent-pid -> child-pids map.
func procChildrenByParent() (map[int][]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	children := make(map[int][]int)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		ppid, ok := readStatPPID(pid)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	return children, nil
}

// readStatPPID reads the parent PID out of /proc/<pid>/stat. Field 4 is
// PPID, but field 2 (comm) is parenthesized and may itself contain spaces or
// closing parens, so the split point is the *last* ")" in the line, per the
// documented proc(5) parsing approach.
func readStatPPID(pid int) (int, bool) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0, false
	}
	line := scanner.Text()
	closeParen := strings.LastIndexByte(line, ')')
	if closeParen < 0 || closeParen+2 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[closeParen+2:])
	// fields[0] is state (field 3); fields[1] is ppid (field 4).
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
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

func newEphemeralHostKey() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}
