package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// marshalED25519PrivateKeyPEM encodes priv as a PKCS8 "BEGIN PRIVATE KEY"
// PEM block, which ssh.ParsePrivateKey/ParseRawPrivateKey both understand.
func marshalED25519PrivateKeyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return pem.EncodeToMemory(block), nil
}

// fixtureServer is an in-process SSH server fixture, per the design spec's
// prescribed test approach: "in-process ssh server fixture
// (ssh.NewServerConn)". It listens on a real localhost TCP port (not
// net.Pipe, since ProxyJump needs a genuine second hop dial) but is never
// long-lived across tests, so it does not need t.Parallel() safety beyond
// what's already provided by using an ephemeral :0 port per fixture.
type fixtureServer struct {
	listener net.Listener
	hostKey  ssh.Signer
	addr     string

	// authorizedKey, if set, is the only public key accepted for
	// publickey auth; nil accepts any key (used for password-auth-only
	// fixtures).
	authorizedKey ssh.PublicKey

	// acceptPassword, if non-empty, is the only accepted password for
	// password auth.
	acceptPassword string

	// onSession, if set, is invoked for each accepted session channel with
	// requests so tests can assert on pty-req/window-change/exec payloads.
	onSession func(channel ssh.Channel, requests <-chan *ssh.Request)

	// jumpTarget, if set, makes the fixture also handle direct-tcpip
	// channel-open requests by dialing jumpTarget and relaying — this is
	// what turns a fixture into a "jump host" for ProxyJump tests.
	jumpTarget string
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("wrapping host key signer: %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	return &fixtureServer{listener: l, hostKey: signer, addr: l.Addr().String()}
}

// generateClientKey returns a fresh ed25519 private key PEM and its public
// key, for use as an identity file / authorized key pair in tests.
func generateClientKey(t *testing.T) (privPEM []byte, pub ssh.PublicKey) {
	t.Helper()
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating client key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("wrapping client key signer: %v", err)
	}
	block, err := marshalED25519PrivateKeyPEM(privKey)
	if err != nil {
		t.Fatalf("marshaling private key: %v", err)
	}
	pubSSH, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("wrapping public key: %v", err)
	}
	_ = signer
	return block, pubSSH
}

// serve runs the fixture's accept loop in a goroutine and returns
// immediately; call stop() (returned) to shut it down.
func (f *fixtureServer) serve(t *testing.T) (stop func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for {
			conn, err := f.listener.Accept()
			if err != nil {
				close(done)
				return
			}
			go f.handleConn(t, conn)
		}
	}()
	return func() {
		f.listener.Close()
		<-done
	}
}

func (f *fixtureServer) serverConfig() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if f.authorizedKey == nil {
				return &ssh.Permissions{}, nil
			}
			if string(key.Marshal()) == string(f.authorizedKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errAuthFailed
		},
	}
	if f.acceptPassword != "" {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == f.acceptPassword {
				return &ssh.Permissions{}, nil
			}
			return nil, errAuthFailed
		}
	}
	if f.authorizedKey == nil && f.acceptPassword == "" {
		// No auth configured at all: accept any public key so tests that
		// don't care about auth (e.g. pure channel/PTY plumbing tests)
		// still work.
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		}
	}
	cfg.AddHostKey(f.hostKey)
	return cfg
}

func (f *fixtureServer) handleConn(t *testing.T, conn net.Conn) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, f.serverConfig())
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			channel, requests, err := newChan.Accept()
			if err != nil {
				continue
			}
			if f.onSession != nil {
				go f.onSession(channel, requests)
			} else {
				go func() {
					for req := range requests {
						if req.WantReply {
							req.Reply(true, nil)
						}
					}
				}()
			}
		case "direct-tcpip":
			if f.jumpTarget == "" {
				newChan.Reject(ssh.Prohibited, "no jump target configured")
				continue
			}
			channel, requests, err := newChan.Accept()
			if err != nil {
				continue
			}
			go ssh.DiscardRequests(requests)
			go f.relayToJumpTarget(channel)
		default:
			newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

func (f *fixtureServer) relayToJumpTarget(channel ssh.Channel) {
	defer channel.Close()
	target, err := net.Dial("tcp", f.jumpTarget)
	if err != nil {
		return
	}
	defer target.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(target, channel)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(channel, target)
		done <- struct{}{}
	}()
	<-done
}

var errAuthFailed = &authFailedError{}

type authFailedError struct{}

func (e *authFailedError) Error() string { return "authentication failed" }
