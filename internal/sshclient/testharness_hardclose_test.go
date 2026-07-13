package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// HardCloseHarness is a portable in-process SSH server. Drop closes accepted
// TCP connections without stopping the listener, modeling a network hard
// close while allowing subsequent reconnects on every supported OS.
type HardCloseHarness struct {
	listener net.Listener
	hostKey  ssh.Signer
	mu       sync.Mutex
	conns    []net.Conn
}

func NewHardCloseHarness(t *testing.T) *HardCloseHarness {
	t.Helper()
	signer, err := newEphemeralHostKey()
	if err != nil {
		t.Fatalf("sshclient: generating harness host key: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sshclient: listening for hard-close harness: %v", err)
	}
	h := &HardCloseHarness{listener: listener, hostKey: signer}
	go h.acceptLoop()
	t.Cleanup(h.Stop)
	return h
}
func (h *HardCloseHarness) Addr() string { return h.listener.Addr().String() }
func (h *HardCloseHarness) Drop() {
	h.mu.Lock()
	conns := h.conns
	h.conns = nil
	h.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}
func (h *HardCloseHarness) Stop() { _ = h.listener.Close(); h.Drop() }
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
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return &ssh.Permissions{}, nil }}
	cfg.AddHostKey(h.hostKey)
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		channel, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range requests {
				if req.WantReply {
					_ = req.Reply(true, nil)
				}
			}
		}()
		_ = channel
	}
}
func newEphemeralHostKey() (ssh.Signer, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(privateKey)
}
