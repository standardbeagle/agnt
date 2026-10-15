package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh/agent"
)

// fakeAgent serves an in-memory keyring on a Unix socket and counts the
// connections it accepts.
type fakeAgent struct {
	sock     string
	accepted atomic.Int32
	mu       sync.Mutex
	conns    []net.Conn
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	keyring := agent.NewKeyring()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	f := &fakeAgent{sock: sock}
	t.Cleanup(func() { ln.Close(); f.closeConns() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.accepted.Add(1)
			f.mu.Lock()
			f.conns = append(f.conns, conn)
			f.mu.Unlock()
			go func() { _ = agent.ServeAgent(keyring, conn); _ = conn.Close() }()
		}
	}()
	return f
}

func (f *fakeAgent) closeConns() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
}

// Every dial and every ProxyJump hop builds auth methods. Each build used to
// open its own agent connection and never close it.
func TestAgentSigners_ReusesOneConnectionAcrossDials(t *testing.T) {
	f := newFakeAgent(t)
	t.Setenv("SSH_AUTH_SOCK", f.sock)

	for i := 0; i < 5; i++ {
		list := agentSigners()
		if list == nil {
			t.Fatal("agentSigners = nil with a reachable agent")
		}
		signers, err := list()
		if err != nil || len(signers) != 1 {
			t.Fatalf("listing agent keys: %d signers, err %v", len(signers), err)
		}
	}
	if n := f.accepted.Load(); n != 1 {
		t.Fatalf("agent accepted %d connections for 5 auth builds, want 1", n)
	}
}

// A shared connection that breaks (agent restarted) is replaced, not kept.
func TestAgentSigners_RedialsAfterConnectionBreaks(t *testing.T) {
	f := newFakeAgent(t)
	t.Setenv("SSH_AUTH_SOCK", f.sock)

	list := agentSigners()
	if list == nil {
		t.Fatal("agentSigners = nil with a reachable agent")
	}
	// The dial returns before the server goroutine records the connection;
	// a round trip guarantees it has been accepted and recorded.
	if _, err := list(); err != nil {
		t.Fatalf("listing agent keys: %v", err)
	}
	f.closeConns()

	signers, err := list()
	if err != nil {
		t.Fatalf("listing agent keys after the connection broke: %v", err)
	}
	if len(signers) != 1 {
		t.Fatalf("got %d signers after the connection broke, want 1 from a fresh connection", len(signers))
	}
	if n := f.accepted.Load(); n != 2 {
		t.Fatalf("agent accepted %d connections, want 2 (original + redial)", n)
	}
}
