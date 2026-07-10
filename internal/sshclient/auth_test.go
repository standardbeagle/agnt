package sshclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestBuildAuthMethods_IdentityFileSucceedsAgainstFixture(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")

	privPEM, pub := generateClientKey(t)
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatalf("writing identity file: %v", err)
	}

	fixture := newFixtureServer(t)
	fixture.authorizedKey = pub
	stop := fixture.serve(t)
	defer stop()

	knownHostsPath := filepath.Join(dir, "known_hosts")
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	hkCallback, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}

	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}

	client, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("dialing fixture with identity-file auth: %v", err)
	}
	defer client.Close()

	// Prove the connection actually works end-to-end: open a session
	// channel and exchange a request/reply.
	channel, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("opening session channel: %v", err)
	}
	go ssh.DiscardRequests(reqs)
	defer channel.Close()
}

func TestBuildAuthMethods_WrongIdentityFileRejected(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_wrong")

	privPEM, _ := generateClientKey(t)
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatalf("writing identity file: %v", err)
	}
	// authorizedKey is a DIFFERENT key than the one at keyPath.
	_, authorizedPub := generateClientKey(t)

	fixture := newFixtureServer(t)
	fixture.authorizedKey = authorizedPub
	stop := fixture.serve(t)
	defer stop()

	knownHostsPath := filepath.Join(dir, "known_hosts")
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	hkCallback, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}

	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods([]string{keyPath}, prompter),
		HostKeyCallback: hkCallback,
	}

	_, err = ssh.Dial("tcp", fixture.addr, clientCfg)
	if err == nil {
		t.Fatal("expected auth failure with mismatched identity file, got success")
	}
}
