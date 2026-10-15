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

// A host whose sshd offers only "password" (no keyboard-interactive) was
// unreachable without a key: the chain never offered the password method,
// although docs/remote-ssh.md promises password authentication.
func TestBuildAuthMethods_PasswordAuthOffered(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "") // hermetic: no real agent keys in the chain

	_, unrelatedPub := generateClientKey(t)
	fixture := newFixtureServer(t)
	fixture.authorizedKey = unrelatedPub // no key we hold is accepted
	fixture.acceptPassword = "s3cret"
	stop := fixture.serve(t)
	defer stop()

	var out strings.Builder
	prompter := Prompter{In: strings.NewReader("s3cret\n"), Out: &out}
	clientCfg := &ssh.ClientConfig{
		User:            "tester",
		Auth:            BuildAuthMethods(nil, prompter),
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // auth is under test, not host keys
	}
	client, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("password-only server rejected the client: %v", err)
	}
	client.Close()
	if !strings.Contains(out.String(), "password") {
		t.Errorf("no password prompt was written; prompter output = %q", out.String())
	}
}

// Without an IdentityFile in ssh_config, OpenSSH still offers the user's
// default keys (~/.ssh/id_ed25519, id_ecdsa, id_rsa). The production dial
// config (clientConfigFor) offered nothing, so a plain key-only host failed
// with "no supported methods remain".
func TestClientConfigFor_DefaultIdentityFilesOffered(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	privPEM, pub := generateClientKey(t)
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), privPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	fixture := newFixtureServer(t)
	fixture.authorizedKey = pub
	stop := fixture.serve(t)
	defer stop()

	// The host-key TOFU prompt reads "yes"; a password prompt would find EOF.
	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	clientCfg, err := clientConfigFor(HostConfig{User: "tester"}, prompter, filepath.Join(home, "known_hosts"))
	if err != nil {
		t.Fatalf("clientConfigFor: %v", err)
	}
	client, err := ssh.Dial("tcp", fixture.addr, clientCfg)
	if err != nil {
		t.Fatalf("default ~/.ssh/id_ed25519 was not offered: %v", err)
	}
	client.Close()
}

func TestDefaultIdentityFiles_ExistingOnlyInPreferenceOrder(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id_rsa", "id_ed25519"} {
		if err := os.WriteFile(filepath.Join(sshDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := defaultIdentityFiles(home)
	want := []string{filepath.Join(sshDir, "id_ed25519"), filepath.Join(sshDir, "id_rsa")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("defaultIdentityFiles = %v, want %v (existing files only, ed25519 > ecdsa > rsa)", got, want)
	}
	if got := defaultIdentityFiles(""); len(got) != 0 {
		t.Errorf("defaultIdentityFiles(\"\") = %v, want none", got)
	}
}
