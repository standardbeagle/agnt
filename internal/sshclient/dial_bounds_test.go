package sshclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// silentListener accepts TCP connections and never writes a byte: the peer
// that completes the TCP handshake and then stalls (a frozen sshd, a captive
// portal). Accepted conns are held open until the test ends.
func silentListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var held []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
		for _, c := range held {
			c.Close()
		}
	})
	return ln.Addr().String()
}

func silentPeerConfig() *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            "tester",
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
	}
}

// dialResult runs dialDirect and fails the test if it does not return at all.
// The 10s here is a liveness ceiling, not the property under test: before the
// fix the call never returned.
func dialResult(t *testing.T, ctx context.Context, addr string) error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		client, err := dialDirect(ctx, addr, silentPeerConfig())
		if client != nil {
			client.Close()
		}
		errCh <- err
	}()
	select {
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("dialDirect did not return: the SSH handshake is unbounded")
		return nil
	}
}

func TestDialDirect_SilentPeerHitsHandshakeDeadline(t *testing.T) {
	addr := silentListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := dialResult(t, ctx, addr)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dialDirect against a silent peer = %v, want context.DeadlineExceeded", err)
	}
}

func TestDialDirect_CancelInterruptsHandshake(t *testing.T) {
	addr := silentListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	err := dialResult(t, ctx, addr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dialDirect after cancel = %v, want context.Canceled", err)
	}
}

// A dial cut short by cancellation ends the reconnect loop at once; it is not
// reported as a failed attempt and retried.
func TestReconnector_CancelDuringDialStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dials := 0
	r := &Reconnector{
		Backoff: BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Microsecond},
		Dial: func(ctx context.Context) (*Client, error) {
			dials++
			cancel()
			return nil, ctx.Err()
		},
		Attach: func(context.Context, *Client) (*PTYSession, error) {
			t.Fatal("attach must not run")
			return nil, nil
		},
	}
	_, _, err := r.Run(ctx)
	if !errors.Is(err, context.Canceled) || dials != 1 {
		t.Fatalf("Run = %v after %d dial(s), want context.Canceled after 1", err, dials)
	}
}

func TestKnownHostKeyAlgorithms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	_, pub, _ := newTestHostKey(t) // ed25519
	if err := appendKnownHost(path, "example.test:22", pub); err != nil {
		t.Fatalf("appendKnownHost: %v", err)
	}

	// Known by ed25519 only: ask for ed25519, so a server that also has an
	// ECDSA key does not present that one and trip the mismatch branch.
	got := knownHostKeyAlgorithms(path, "example.test:22")
	if len(got) != 1 || got[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("algorithms for an ed25519-known host = %v, want [%s]", got, ssh.KeyAlgoED25519)
	}

	// Unknown host: no opinion, the TOFU prompt takes the server's choice.
	if got := knownHostKeyAlgorithms(path, "other.test:22"); got != nil {
		t.Fatalf("algorithms for an unknown host = %v, want nil", got)
	}
	// Missing file: no opinion.
	if got := knownHostKeyAlgorithms(filepath.Join(dir, "absent"), "example.test:22"); got != nil {
		t.Fatalf("algorithms with no known_hosts = %v, want nil", got)
	}

	// A CA line names the CA's key type, not the host certificate's.
	caPath := filepath.Join(dir, "known_hosts_ca")
	line := "@cert-authority *.example.test " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + "\n"
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(caPath, append(data, line...), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := knownHostKeyAlgorithms(caPath, "example.test:22"); got != nil {
		t.Fatalf("algorithms with @cert-authority present = %v, want nil", got)
	}
}

// Building the auth chain must not touch the identity files: they are read
// only if the server gets as far as asking for them.
func TestBuildAuthMethods_IdentityFilesAreLazy(t *testing.T) {
	var out bytes.Buffer
	prompter := Prompter{In: strings.NewReader(""), Out: &out}
	methods := BuildAuthMethods([]string{filepath.Join(t.TempDir(), "no-such-key")}, prompter)
	if len(methods) == 0 {
		t.Fatal("no auth methods built")
	}
	if out.Len() != 0 {
		t.Fatalf("identity file was read while building the auth chain: %q", out.String())
	}
}

// The ssh library tries each auth method NAME once. An agent and the identity
// files used to be two "publickey" methods, so whenever an agent was reachable
// and did not hold the right key the identity file was never offered. macOS
// always has a (usually empty) launchd agent, which is how this surfaced: every
// fixture dial there failed with "attempted methods [none publickey]".
func TestPublicKeyAuthMethod_IdentityFileOfferedAfterAgentKeys(t *testing.T) {
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongSigner, err := ssh.NewSignerFromKey(wrongPriv)
	if err != nil {
		t.Fatal(err)
	}

	agents := map[string]func() ([]ssh.Signer, error){
		"agent with no keys":       func() ([]ssh.Signer, error) { return nil, nil },
		"agent with the wrong key": func() ([]ssh.Signer, error) { return []ssh.Signer{wrongSigner}, nil },
		"agent that cannot list":   func() ([]ssh.Signer, error) { return nil, errors.New("agent refused") },
	}
	for name, agentKeys := range agents {
		t.Run(name, func(t *testing.T) {
			keyPath := filepath.Join(t.TempDir(), "id_test")
			privPEM, pub := generateClientKey(t)
			if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
				t.Fatal(err)
			}
			fixture := newFixtureServer(t)
			fixture.authorizedKey = pub
			defer fixture.serve(t)()

			prompter := Prompter{In: strings.NewReader(""), Out: &discardWriter{}}
			client, err := ssh.Dial("tcp", fixture.addr, &ssh.ClientConfig{
				User:            "tester",
				Auth:            []ssh.AuthMethod{publicKeyAuthMethod(agentKeys, []string{keyPath}, prompter)},
				HostKeyCallback: ssh.InsecureIgnoreHostKey(), // fixture only; auth is what is under test
			})
			if err != nil {
				t.Fatalf("identity file was not offered alongside the agent: %v", err)
			}
			client.Close()
		})
	}
}

// An encrypted identity file is offered by its public key; the passphrase is
// asked for only when a signature is needed.
func TestLoadIdentityFile_EncryptedKeyIsNotUnlockedUntilItSigns(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_enc")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	prompter := Prompter{In: strings.NewReader("s3cret\n"), Out: &out}
	signer, err := cachedIdentity(keyPath, prompter)
	if err != nil {
		t.Fatalf("cachedIdentity: %v", err)
	}
	if _, lazy := signer.(*lazyIdentity); !lazy {
		t.Fatalf("encrypted key came back as %T, want *lazyIdentity", signer)
	}
	if out.Len() != 0 {
		t.Fatalf("passphrase was asked for before any signature: %q", out.String())
	}
	if _, isAlgo := signer.(ssh.AlgorithmSigner); !isAlgo {
		t.Fatal("lazy identity must be an AlgorithmSigner (RSA keys need SHA-2 signatures)")
	}

	sig, err := signer.Sign(rand.Reader, []byte("payload"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := signer.PublicKey().Verify([]byte("payload"), sig); err != nil {
		t.Fatalf("signature does not verify against the advertised public key: %v", err)
	}
	if !strings.Contains(out.String(), "Enter passphrase") {
		t.Fatalf("no passphrase prompt on first signature; output %q", out.String())
	}
	// Unlocked once: the next dial finds the decrypted signer in the cache.
	if _, cached := identityCache.Load(keyPath); !cached {
		t.Fatal("unlocked identity was not cached")
	}
}

func TestCachedIdentity_LoadsOncePerPath(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "id_test")
	privPEM, _ := generateClientKey(t)
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	prompter := Prompter{In: strings.NewReader(""), Out: &discardWriter{}}

	first, err := cachedIdentity(keyPath, prompter)
	if err != nil {
		t.Fatalf("cachedIdentity: %v", err)
	}
	// With the file gone, only the cache can answer: a reconnect must not
	// re-read (and for an encrypted key, re-prompt for) the identity.
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	second, err := cachedIdentity(keyPath, prompter)
	if err != nil {
		t.Fatalf("cachedIdentity after the file was removed: %v", err)
	}
	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Fatal("cache returned a different signer")
	}

	// A failed load is not cached.
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := cachedIdentity(missing, prompter); err == nil {
		t.Fatal("expected an error for a missing identity file")
	}
	if _, cached := identityCache.Load(missing); cached {
		t.Fatal("a failed load was cached")
	}
}

func TestResolveHost_WarnsOnSkippedConstructs(t *testing.T) {
	const cfg = `User global-user
Include ~/.orbstack/ssh/config

Host box
    HostName 10.0.0.5

Match host box
    User from-match
    Port 2200

Host other
    User other-user
`
	got, err := ResolveHostFromReader(strings.NewReader(cfg), "box")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Warnings, "\n")
	for _, want := range []string{"Include", "Match", "before the first Host"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q do not mention %q", joined, want)
		}
	}
	if len(got.Warnings) != 3 {
		t.Errorf("got %d warnings, want one per kind: %q", len(got.Warnings), got.Warnings)
	}
	// The Match block's options must not be applied to the Host block above.
	if got.User != "" || got.Port != 22 || got.HostName != "10.0.0.5" {
		t.Errorf("Match options leaked into Host box: %+v", got)
	}

	clean, err := ResolveHostFromReader(strings.NewReader("Host box\n    User me\n"), "box")
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Warnings) != 0 {
		t.Errorf("unexpected warnings for a plain config: %q", clean.Warnings)
	}
}

func TestListenAddrTarget(t *testing.T) {
	cases := map[string]forwardTarget{
		"127.0.0.1:5173":     {Host: "127.0.0.1", Port: 5173},
		"0.0.0.0:5173":       {Host: "127.0.0.1", Port: 5173},
		"[::]:5173":          {Host: "127.0.0.1", Port: 5173},
		"100.101.102.3:8080": {Host: "100.101.102.3", Port: 8080}, // bind "tailscale"
		"[fd7a::1]:8080":     {Host: "fd7a::1", Port: 8080},
	}
	for addr, want := range cases {
		got, err := listenAddrTarget(addr)
		if err != nil || got != want {
			t.Errorf("listenAddrTarget(%q) = %+v, %v; want %+v", addr, got, err, want)
		}
	}
	for _, bad := range []string{"", "5173", "host:notaport", "host:0"} {
		if _, err := listenAddrTarget(bad); err == nil {
			t.Errorf("listenAddrTarget(%q) accepted a malformed address", bad)
		}
	}
}
