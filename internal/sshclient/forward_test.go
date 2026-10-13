package sshclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
)

// dialFixtureClient dials fixture over plain ssh.ClientConfig (no
// ssh_config/known_hosts machinery) and wraps it in a *Client, mirroring
// the pattern already used by TestKeepalive_* in client_test.go.
func dialFixtureClient(t *testing.T, fixture *fixtureServer) *Client {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	privPEM, _ := generateClientKey(t)
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatalf("writing identity file: %v", err)
	}
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
	t.Cleanup(func() { sshClient.Close() })
	return &Client{SSH: sshClient, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}
}

// shortSocketDir returns a fresh directory whose path is short enough to hold
// a unix-domain socket. It is sockdir.Short, the one implementation shared by
// every package's tests; see that package for the sun_path limit.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	return sockdir.Short(t)
}
