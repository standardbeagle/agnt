package sshclient

import (
	"fmt"
	"os"
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
	c.startKeepalive(20*time.Millisecond, 3)

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
	c.startKeepalive(20*time.Millisecond, 3)
	defer close(c.stopKeepalive)

	select {
	case <-c.Dead():
		t.Fatal("Dead() fired while transport was healthy")
	case <-time.After(200 * time.Millisecond):
		// expected: still alive
	}
}
