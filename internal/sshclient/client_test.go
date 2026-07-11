package sshclient

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestDialWithJumps_TwoHopProxyJumpSucceeds(t *testing.T) {
	dir := t.TempDir()

	// Target fixture: the real destination.
	target := newFixtureServer(t)
	stopTarget := target.serve(t)
	defer stopTarget()

	// Jump fixture: relays direct-tcpip channels to the target.
	jump := newFixtureServer(t)
	jump.jumpTarget = target.addr
	stopJump := jump.serve(t)
	defer stopJump()

	// Fixtures accept any public key when no authorizedKey is configured,
	// but the client must still OFFER a publickey method (interactive
	// keyboard-interactive alone won't satisfy a fixture with no
	// KeyboardInteractiveCallback) — so give each hop its own identity
	// file via ssh_config, matching the requirement that jump hosts get
	// independently resolved auth, not the target's config.
	jumpKeyPath := filepath.Join(dir, "id_jump")
	jumpPriv, _ := generateClientKey(t)
	if err := os.WriteFile(jumpKeyPath, jumpPriv, 0o600); err != nil {
		t.Fatalf("writing jump identity file: %v", err)
	}
	targetKeyPath := filepath.Join(dir, "id_target")
	targetPriv, _ := generateClientKey(t)
	if err := os.WriteFile(targetKeyPath, targetPriv, 0o600); err != nil {
		t.Fatalf("writing target identity file: %v", err)
	}

	jumpHost, jumpPort := splitAddr(t, jump.addr)
	targetHost, targetPort := splitAddr(t, target.addr)

	configContent := fmt.Sprintf(`
Host jumphost
    HostName %s
    Port %s
    IdentityFile %s

Host targethost
    HostName %s
    Port %s
    IdentityFile %s
    ProxyJump jumphost
`, jumpHost, jumpPort, jumpKeyPath, targetHost, targetPort, targetKeyPath)

	configPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("writing ssh_config: %v", err)
	}

	cfg, err := ResolveHost(configPath, "targethost")
	if err != nil {
		t.Fatalf("ResolveHost: %v", err)
	}
	if cfg.ProxyJump != "jumphost" {
		t.Fatalf("ProxyJump = %q, want jumphost", cfg.ProxyJump)
	}

	prompter := Prompter{In: strings.NewReader("yes\nyes\n"), Out: &discardWriter{}}
	sshClient, err := dialWithJumps(cfg, configPath, filepath.Join(dir, "known_hosts"), "tester", prompter)
	if err != nil {
		t.Fatalf("dialWithJumps: %v", err)
	}
	defer sshClient.Close()

	// Prove the multi-hop connection actually reaches the target: open a
	// session channel through it.
	channel, reqs, err := sshClient.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("opening session channel through proxy jump: %v", err)
	}
	go ssh.DiscardRequests(reqs)
	channel.Close()
}

func splitAddr(t *testing.T, addr string) (host, port string) {
	t.Helper()
	idx := strings.LastIndexByte(addr, ':')
	if idx < 0 {
		t.Fatalf("address %q has no port", addr)
	}
	return addr[:idx], addr[idx+1:]
}

func TestKeepalive_DetectsDeadTransportAfterConsecutiveFailures(t *testing.T) {
	fixture := newFixtureServer(t)
	stop := fixture.serve(t)

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	privPEM, _ := generateClientKey(t)
	os.WriteFile(keyPath, privPEM, 0o600)
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	hkCallback, err := HostKeyCallback(filepath.Join(dir, "known_hosts"), prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}
	sshClient, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("dialing fixture: %v", err)
	}

	c := &Client{SSH: sshClient, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}
	c.startKeepalive(20*time.Millisecond, 100*time.Millisecond, 3)

	// Kill the underlying transport so subsequent keepalive sends fail.
	stop()
	sshClient.Close()

	select {
	case <-c.Dead():
		// expected
	case <-time.After(3 * time.Second):
		t.Fatal("expected Dead() to close after consecutive keepalive failures, timed out")
	}
}

func TestKeepalive_DetectsBlackHoledTransportViaPerCallTimeout(t *testing.T) {
	fixture := newFixtureServer(t)
	fixture.blackholeGlobalRequests = true
	stop := fixture.serve(t)
	defer stop()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	privPEM, _ := generateClientKey(t)
	os.WriteFile(keyPath, privPEM, 0o600)
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	hkCallback, err := HostKeyCallback(filepath.Join(dir, "known_hosts"), prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}
	sshClient, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("dialing fixture: %v", err)
	}

	c := &Client{SSH: sshClient, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}
	// interval > sendTimeout, per acceptance criterion 1: each SendRequest
	// call must be bounded by a timeout shorter than the keepalive
	// interval, so a black-holed reply still lets the miss counter
	// advance every tick instead of stalling forever on the first call.
	c.startKeepalive(30*time.Millisecond, 10*time.Millisecond, 3)

	select {
	case <-c.Dead():
		// expected: three black-holed ticks (~90ms) trip the miss
		// counter and close Dead(), well within 3s.
	case <-time.After(3 * time.Second):
		t.Fatal("expected Dead() to close after black-holed keepalive timeouts, timed out")
	}
}

func TestKeepalive_DoesNotFireWhileTransportHealthy(t *testing.T) {
	fixture := newFixtureServer(t)
	stop := fixture.serve(t)
	defer stop()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	privPEM, _ := generateClientKey(t)
	os.WriteFile(keyPath, privPEM, 0o600)
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	hkCallback, err := HostKeyCallback(filepath.Join(dir, "known_hosts"), prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}
	sshClient, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("dialing fixture: %v", err)
	}
	defer sshClient.Close()

	c := &Client{SSH: sshClient, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}
	c.startKeepalive(20*time.Millisecond, 100*time.Millisecond, 3)
	defer close(c.stopKeepalive)

	select {
	case <-c.Dead():
		t.Fatal("Dead() fired while transport was healthy")
	case <-time.After(200 * time.Millisecond):
		// expected: still alive
	}
}

// TestReconnectHarness_HardCloseDropsConnection exercises drop mode (a) from
// task 09a's harness: an explicit, non-sleep-based Drop() call hard-closes
// the underlying TCP connection, which a client blocked in a channel
// request must observe as a transport error (not a hang).
func TestReconnectHarness_HardCloseDropsConnection(t *testing.T) {
	harness := NewHardCloseHarness(t)

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	privPEM, _ := generateClientKey(t)
	os.WriteFile(keyPath, privPEM, 0o600)
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	hkCallback, err := HostKeyCallback(filepath.Join(dir, "known_hosts"), prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}
	sshClient, err := ssh.Dial("tcp", harness.Addr(), clientCfg)
	if err != nil {
		t.Fatalf("dialing harness: %v", err)
	}
	defer sshClient.Close()

	// Deterministic trigger, not a sleep: the drop happens exactly when we
	// call it.
	harness.Drop()

	// A hard TCP close must surface as an error on the next use of the
	// connection, within a bound well short of the test timeout — proving
	// this isn't a hang.
	done := make(chan error, 1)
	go func() {
		_, _, err := sshClient.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected SendRequest to fail after harness.Drop(), got nil error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendRequest did not fail after harness.Drop(); hard TCP close was not observed")
	}
}

// TestReconnectHarness_SSHDFreezeBlocksResponses exercises drop mode (b): a
// real sshd subprocess frozen with SIGSTOP answers nothing while the TCP
// connection stays established — the black-hole case a hard TCP close can't
// reproduce. Per acceptance criterion 1, this is SKIPPED (not failed) when
// no sshd/ssh-keygen is on PATH. Per acceptance criterion 3, this test is
// NOT t.Parallel(): it drives a real OS subprocess, and this repo's
// convention (AGENTS.md, pre-commit hook notes) is that real-process tests
// race under concurrency via PID reuse.
func TestReconnectHarness_SSHDFreezeBlocksResponses(t *testing.T) {
	privPEM, clientPub := generateClientKey(t)
	harness, err := NewSSHDFreezeHarness(t, clientPub)
	if errors.Is(err, ErrSSHDNotFound) {
		t.Skip("sshd/ssh-keygen not found on PATH; skipping real-sshd freeze harness test")
	}
	if err != nil {
		t.Fatalf("NewSSHDFreezeHarness: %v", err)
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	os.WriteFile(keyPath, privPEM, 0o600)
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	hkCallback, err := HostKeyCallback(filepath.Join(dir, "known_hosts"), prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("looking up current OS user: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		// sshd runs unprivileged (as this test process's own user) and can
		// only authenticate connections for that same OS account — it has
		// no setuid path to an arbitrary "tester" user.
		User:            currentUser.Username,
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
		Timeout:         5 * time.Second,
	}
	sshClient, err := ssh.Dial("tcp", harness.Addr(), clientCfg)
	if err != nil {
		t.Fatalf("dialing real sshd harness: %v", err)
	}
	defer sshClient.Close()

	// Deterministic trigger: freeze the real sshd process.
	if err := harness.Freeze(); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	defer harness.Resume()

	// A frozen-but-connected peer must NOT reply within a short bound — the
	// request is expected to time out, not error immediately (that would be
	// indistinguishable from a hard close, defeating the point of this
	// drop mode).
	resultCh := make(chan error, 1)
	go func() {
		_, _, err := sshClient.SendRequest("keepalive@openssh.com", true, nil)
		resultCh <- err
	}()
	select {
	case err := <-resultCh:
		t.Fatalf("expected SendRequest to hang against a frozen sshd, got reply/error: %v", err)
	case <-time.After(500 * time.Millisecond):
		// expected: still silent while frozen
	}

	// Resume and confirm the connection is genuinely alive again, not just
	// left in some permanently wedged state.
	if err := harness.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("expected SendRequest to succeed after Resume, got error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendRequest did not complete after Resume(); sshd did not come back")
	}
}
