package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestHostKeyCallback_UnknownHost_AcceptedPromptAppendsKnownHosts(t *testing.T) {
	dir := t.TempDir()
	knownHostsPath := filepath.Join(dir, "known_hosts")
	// File doesn't exist yet — HostKeyCallback must create it.

	_, hostPub, hostKey := newTestHostKey(t)

	prompter := Prompter{In: strings.NewReader("yes\n"), Out: &discardWriter{}}
	cb, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}

	fakeAddr := &fakeAddr{s: "example.com:22"}
	if err := cb("example.com:22", fakeAddr, hostPub); err != nil {
		t.Fatalf("expected accept on 'yes' prompt, got error: %v", err)
	}

	data, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("reading known_hosts after accept: %v", err)
	}
	if !strings.Contains(string(data), "example.com") {
		t.Errorf("known_hosts does not contain accepted host entry: %q", string(data))
	}
	_ = hostKey
}

func TestHostKeyCallback_UnknownHost_RejectedPromptFails(t *testing.T) {
	dir := t.TempDir()
	knownHostsPath := filepath.Join(dir, "known_hosts")

	_, hostPub, _ := newTestHostKey(t)

	prompter := Prompter{In: strings.NewReader("no\n"), Out: &discardWriter{}}
	cb, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}

	err = cb("example.com:22", &fakeAddr{s: "example.com:22"}, hostPub)
	if err != ErrHostKeyRejected {
		t.Fatalf("expected ErrHostKeyRejected, got %v", err)
	}

	if data, readErr := os.ReadFile(knownHostsPath); readErr == nil && strings.Contains(string(data), "example.com") {
		t.Errorf("known_hosts should NOT contain a rejected host entry: %q", string(data))
	}
}

func TestHostKeyCallback_KnownHostMismatch_HardFailsWithoutPrompting(t *testing.T) {
	dir := t.TempDir()
	knownHostsPath := filepath.Join(dir, "known_hosts")

	_, originalPub, _ := newTestHostKey(t)
	_, differentPub, _ := newTestHostKey(t)

	// Pre-populate known_hosts with the ORIGINAL key for the host.
	line := knownhosts.Line([]string{knownhosts.Normalize("example.com:22")}, originalPub)
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("seeding known_hosts: %v", err)
	}

	// Prompter would answer "yes" if consulted — but it must NEVER be
	// consulted for a mismatch, per spec §5.1.
	promptCalled := false
	prompter := Prompter{In: &trackingReader{answer: "yes\n", called: &promptCalled}, Out: &discardWriter{}}

	cb, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}

	err = cb("example.com:22", &fakeAddr{s: "example.com:22"}, differentPub)
	if err == nil {
		t.Fatal("expected hard failure on host key mismatch, got nil error")
	}
	if !strings.Contains(err.Error(), "HOST KEY MISMATCH") {
		t.Errorf("error message should clearly name a mismatch: %v", err)
	}
	if promptCalled {
		t.Error("prompter must NEVER be consulted on a key-changed mismatch")
	}
}

func TestHostKeyCallback_MatchingKnownHost_AcceptsSilently(t *testing.T) {
	dir := t.TempDir()
	knownHostsPath := filepath.Join(dir, "known_hosts")

	_, pub, _ := newTestHostKey(t)
	line := knownhosts.Line([]string{knownhosts.Normalize("example.com:22")}, pub)
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("seeding known_hosts: %v", err)
	}

	promptCalled := false
	prompter := Prompter{In: &trackingReader{answer: "no\n", called: &promptCalled}, Out: &discardWriter{}}
	cb, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		t.Fatalf("HostKeyCallback: %v", err)
	}

	if err := cb("example.com:22", &fakeAddr{s: "example.com:22"}, pub); err != nil {
		t.Fatalf("expected silent accept for matching known key, got: %v", err)
	}
	if promptCalled {
		t.Error("prompter should not be consulted for an already-trusted key")
	}
}

// newTestHostKey generates a fresh ed25519 keypair and returns the raw
// private key, its ssh.PublicKey wrapper, and an ssh.Signer.
func newTestHostKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey, ssh.Signer) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrapping public key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("wrapping signer: %v", err)
	}
	return priv, sshPub, signer
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type fakeAddr struct{ s string }

func (a *fakeAddr) Network() string { return "tcp" }
func (a *fakeAddr) String() string  { return a.s }

// trackingReader records whether it was ever read from, so tests can
// assert the interactive prompt path was never entered.
type trackingReader struct {
	answer string
	called *bool
	read   bool
}

func (r *trackingReader) Read(p []byte) (int, error) {
	*r.called = true
	if r.read {
		return 0, os.ErrClosed
	}
	r.read = true
	return strings.NewReader(r.answer).Read(p)
}
