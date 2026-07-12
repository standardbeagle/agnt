//go:build !windows

package sshclient

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	"golang.org/x/crypto/ssh"
)

// This file covers acceptance criterion 5 (task 09c): a single forced drop,
// followed by a reconnect, replays output the client missed while
// disconnected, end to end, driven through the real Reconnector.
//
// HardCloseHarness (testharness_reconnect.go, task 09a) is reused for its
// documented job — triggering a deterministic hard-TCP-close drop — but its
// channel handler is intentionally a stub that never reads/writes channel
// data (it exists only to prove dial/auth/drop mechanics). Simulating "a
// session-host session keeps producing output while the client is
// disconnected, and replays what was missed on reattach" needs a server
// that actually carries bytes on the exec'd channel, which is specific to
// this task's own criterion, not something the 09a harness anticipated. So
// this file adds a small sibling harness (contentHarness) with the same
// accept/Drop/Stop shape, rather than modifying testharness_reconnect.go.

// sessionSim stands in for a remote session-host session's cumulative
// output: an ever-growing byte buffer that a background goroutine appends
// to continuously (simulating a live remote process), independent of
// however many client TCP connections come and go — exactly the property a
// real session-host's ring buffer has (spec §1.4).
type sessionSim struct {
	mu   sync.Mutex
	data []byte
}

func (s *sessionSim) append(b []byte) {
	s.mu.Lock()
	s.data = append(s.data, b...)
	s.mu.Unlock()
}

func (s *sessionSim) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}

// contentHarness is a HardCloseHarness sibling that actually serves channel
// data: every accepted "session" channel replays the sim's full buffer
// (simulating session-host's "replay-then-live" attach contract) and then
// streams new bytes as sim grows, until the channel or connection dies.
type contentHarness struct {
	listener net.Listener
	hostKey  ssh.Signer
	sim      *sessionSim

	mu     sync.Mutex
	conns  []net.Conn
	frozen bool
}

// Freeze/Resume give the durable-content sibling the same black-hole control
// shape as SSHDFreezeHarness while retaining its replay-capable session.
func (h *contentHarness) Freeze() {
	h.mu.Lock()
	h.frozen = true
	h.mu.Unlock()
}

func (h *contentHarness) Resume() {
	h.mu.Lock()
	h.frozen = false
	h.mu.Unlock()
}

func (h *contentHarness) isFrozen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frozen
}

func newContentHarness(t *testing.T, sim *sessionSim) *contentHarness {
	t.Helper()
	signer, err := newEphemeralHostKey()
	if err != nil {
		t.Fatalf("sshclient: generating content harness host key: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sshclient: listening for content harness: %v", err)
	}
	h := &contentHarness{listener: l, hostKey: signer, sim: sim}
	go h.acceptLoop()
	t.Cleanup(h.Stop)
	return h
}

func (h *contentHarness) Addr() string { return h.listener.Addr().String() }

func (h *contentHarness) Drop() {
	h.mu.Lock()
	conns := h.conns
	h.conns = nil
	h.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (h *contentHarness) Stop() {
	h.listener.Close()
	h.Drop()
}

func (h *contentHarness) acceptLoop() {
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

func (h *contentHarness) handshakeAndServe(conn net.Conn) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(h.hostKey)
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		channel, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go h.serveChannel(channel, requests)
	}
}

func (h *contentHarness) serveChannel(channel ssh.Channel, requests <-chan *ssh.Request) {
	// done is closed when the requests channel closes, which the ssh
	// library does on connection/channel teardown — this is this
	// goroutine's only way to notice the peer is gone when sim has
	// stopped growing (a polling-only loop that just re-checks snapshot
	// length would sleep forever once writes stop being attempted, which
	// is exactly the leaked-goroutine failure mode goleak caught during
	// development of this test).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for req := range requests {
			if req.WantReply {
				req.Reply(true, nil)
			}
		}
	}()

	sent := 0
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	// Replay-then-live: write everything accumulated so far, then poll for
	// growth and stream the delta, mirroring session-host's own
	// "full-ring-then-live replay" contract (daemon-architecture.md
	// Session Containment numbered invariants reference the same shape).
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if h.isFrozen() {
				continue
			}
			snap := h.sim.snapshot()
			if len(snap) > sent {
				if _, err := channel.Write(snap[sent:]); err != nil {
					return
				}
				sent = len(snap)
			}
		}
	}
}

// TestReconnector_HardDrop_ReplaysMissedOutput drives criterion 5: a single
// forced hard-TCP-close drop, a real Reconnector.Run redial+reattach, and an
// assertion that output produced strictly during the disconnected window is
// not lost — it shows up once the reattached channel starts streaming.
func TestReconnector_HardDrop_ReplaysMissedOutput(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	sim := &sessionSim{}
	stopLive := make(chan struct{})
	defer close(stopLive)
	go func() {
		n := 0
		for {
			select {
			case <-stopLive:
				return
			case <-time.After(2 * time.Millisecond):
				sim.append([]byte(fmt.Sprintf("live-%d;", n)))
				n++
			}
		}
	}()

	harness := newContentHarness(t, sim)
	defer harness.Stop()

	dial := func(ctx context.Context) (*Client, error) {
		privPEM, _ := generateClientKey(t)
		signer, err := ssh.ParsePrivateKey(privPEM)
		if err != nil {
			return nil, err
		}
		cfg := &ssh.ClientConfig{
			User:            "test",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         2 * time.Second,
		}
		sshClient, err := ssh.Dial("tcp", harness.Addr(), cfg)
		if err != nil {
			return nil, err
		}
		return &Client{SSH: sshClient, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}, nil
	}
	attach := func(ctx context.Context, c *Client) (*PTYSession, error) {
		return OpenPTYSessionWithCommand(c.SSH, RemoteReattachCommand("integration-test-session"), TermSize{Cols: 80, Rows: 24})
	}

	client, err := dial(context.Background())
	if err != nil {
		t.Fatalf("initial dial: %v", err)
	}
	session, err := attach(context.Background(), client)
	if err != nil {
		t.Fatalf("initial attach: %v", err)
	}

	// A single dedicated reader goroutine per channel (not one per read
	// attempt): concurrent Read calls on the same ssh.Channel would race
	// for whichever bytes arrive next, exactly the anti-pattern
	// InputPump (reconnect.go) exists to avoid — so this test observes
	// the same discipline it is testing.
	chunks1 := startChannelReader(session.channel)

	// Read some initial live bytes to confirm streaming is actually
	// flowing before we force the drop.
	buf1 := collectAtLeast(chunks1, 20, 2*time.Second)
	if len(buf1) == 0 {
		t.Fatal("no bytes read before drop")
	}

	// Force the drop (criterion 5: "single forced drop").
	harness.Drop()
	session.Close()
	client.Close()

	// Simulate output produced strictly while the client is disconnected —
	// the bytes a correct reconnect must not lose.
	const missedMarker = "MISSED-WHILE-DISCONNECTED;"
	sim.append([]byte(missedMarker))

	r := &Reconnector{
		Backoff: BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Millisecond},
		Dial:    dial,
		Attach:  attach,
	}
	newClient, newSession, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Reconnector.Run: %v", err)
	}
	defer newClient.Close()
	defer newSession.Close()

	chunks2 := startChannelReader(newSession.channel)

	replayed := collectAtLeast(chunks2, len(missedMarker)+40, 3*time.Second)
	if !bytes.Contains(replayed, []byte(missedMarker)) {
		t.Fatalf("reattached session did not replay the missed marker; got %q", replayed)
	}

	// And confirm live streaming continues past the replay (not stuck
	// re-sending only the historical buffer).
	more := collectAtLeast(chunks2, len(replayed)+20, 3*time.Second)
	if !strings.Contains(string(more), "live-") {
		t.Fatalf("no continued live output after reattach; got %q", more)
	}
}

// startChannelReader spawns exactly one goroutine reading r continuously,
// forwarding each chunk on the returned channel, which is closed when Read
// returns an error (e.g. after the session/channel is closed) — the single-
// owner-per-source discipline this test itself is verifying.
func startChannelReader(r interface{ Read([]byte) (int, error) }) <-chan []byte {
	ch := make(chan []byte, 256)
	go func() {
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				ch <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// collectAtLeast drains ch until at least n bytes have been accumulated or
// timeout elapses, returning whatever was collected (may be < n on
// timeout, which the caller treats as a failure via its own assertions).
func collectAtLeast(ch <-chan []byte, n int, timeout time.Duration) []byte {
	deadline := time.After(timeout)
	var out []byte
	for len(out) < n {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, chunk...)
		case <-deadline:
			return out
		}
	}
	return out
}
