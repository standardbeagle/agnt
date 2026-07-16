package testenv_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/sshclient"
	"github.com/standardbeagle/agnt/internal/sshclient/testenv"
	"go.uber.org/goleak"
	"golang.org/x/crypto/ssh"
)

// This file covers task 07's dynamic port forwarding
// (sshclient.PortForwardManager) at the testenv in-process-harness tier
// (untagged, so it runs under the project's `go test -p 1 ./...` gate — see
// package doc note in auth_matrix_test.go for why an untagged tier exists
// alongside the sshe2e-tagged container tier).
//
// testenv.Server (server.go) only accepts "session" channels — it has no
// direct-tcpip support, which PortForwardManager's relay requires (see
// forward.go/portforward.go: "one direct-tcpip channel per accepted local
// connection"). Rather than editing server.go (out of this task's declared
// 3-file scope), this file defines its own minimal direct-tcpip-capable
// server, following the exact precedent auth_matrix_test.go already set with
// startJumpServer/handleJumpConn for the same reason (ProxyJump needs
// direct-tcpip too, and testenv.Server doesn't support it there either).

// directTCPIPServer is a minimal in-process SSH server that accepts any
// public key from a single generated identity and relays direct-tcpip
// channels to their requested destination — exactly what
// sshclient.PortForwardManager needs on the "remote" side of a forward.
type directTCPIPServer struct {
	listener net.Listener
	auth     *testenv.Auth
}

type directTCPIPMsg struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func startDirectTCPIPServer(t *testing.T, auth *testenv.Auth) *directTCPIPServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating host key: %v", err)
	}
	hostKey, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("wrapping host key: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	s := &directTCPIPServer{listener: listener, auth: auth}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go s.handleConn(conn, hostKey)
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return s
}

func (s *directTCPIPServer) Addr() string { return s.listener.Addr().String() }

func (s *directTCPIPServer) handleConn(conn net.Conn, hostKey ssh.Signer) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, s.auth.ServerConfig(hostKey))
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "direct-tcpip" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "direct-tcpip required")
			continue
		}
		var req directTCPIPMsg
		if err := ssh.Unmarshal(newChannel.ExtraData(), &req); err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, "bad destination")
			continue
		}
		target, dialErr := net.Dial("tcp", net.JoinHostPort(req.Host, fmt.Sprint(req.Port)))
		if dialErr != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, dialErr.Error())
			continue
		}
		channel, channelRequests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			target.Close()
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		go func() {
			done := make(chan struct{}, 2)
			go func() { io.Copy(target, channel); done <- struct{}{} }()
			go func() { io.Copy(channel, target); done <- struct{}{} }()
			// A half-closed HTTP or WebSocket peer can leave the opposite
			// copy blocked forever (matches portforward.go's own relay
			// close discipline): close both directions after the first
			// copy finishes, then join the second.
			<-done
			channel.Close()
			target.Close()
			<-done
		}()
	}
}

// dialForwardClient dials srv over a real ssh_config + known_hosts round
// trip via sshclient.Dial — the exported entry point — mirroring the
// pattern auth_matrix_test.go already uses to obtain a real
// *sshclient.Client from testenv_test (Client's transport fields are
// unexported, so this package cannot construct one directly).
func dialForwardClient(t *testing.T, auth *testenv.Auth, addr string) *sshclient.Client {
	t.Helper()
	dir := t.TempDir()
	identity := filepath.Join(dir, "id_forward")
	if err := os.WriteFile(identity, auth.PrivateKey, 0o600); err != nil {
		t.Fatalf("writing identity: %v", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("splitting addr: %v", err)
	}
	config := fmt.Sprintf("Host fwd\n HostName %s\n Port %s\n User %s\n IdentityFile %s\n",
		host, port, auth.User, identity)
	configPath := filepath.Join(dir, "ssh_config")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("writing ssh_config: %v", err)
	}
	client, err := sshclient.Dial("fwd", configPath, filepath.Join(dir, "known_hosts"), auth.User,
		sshclient.Prompter{In: strings.NewReader("yes\n"), Out: io.Discard})
	if err != nil {
		t.Fatalf("dialing forward fixture: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// newForwardDaemon spins up a real *daemon.Daemon (NewForTest — see
// daemon-architecture.md "Test startup contract") and returns a connected
// *daemon.Client: PortForwardManager's reconcile loop is driven entirely by
// real PROXY LIST calls against it, matching the "cache is never trusted
// alone" doctrine the manager itself documents.
func newForwardDaemon(t *testing.T) *daemon.Client {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "daemon.sock")
	daemon.NewForTest(t, daemon.DaemonConfig{
		SocketPath:        sockPath,
		MaxClients:        10,
		WriteTimeout:      5 * time.Second,
		OrphanScanEnabled: false,
		StatePath:         t.TempDir(),
	})
	c := daemon.NewClientWithPath(sockPath)
	if err := c.Connect(); err != nil {
		t.Fatalf("connecting to test daemon: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func forwardBackend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><html><body>testenv-forward-backend %s</body></html>", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func waitForwarded(t *testing.T, mgr *sshclient.PortForwardManager, proxyID string) sshclient.Mapping {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range mgr.Status() {
			if m.ProxyID == proxyID {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("proxy %s was never forwarded within the deadline", proxyID)
	return sshclient.Mapping{}
}

// TestDynamicForward_Lifecycle drives the acceptance criterion end to end:
// proxy-started -> local listener created -> HTTP traffic flows -> WS
// upgrade traffic flows -> Stop tears the local listener down.
func TestDynamicForward_Lifecycle(t *testing.T) {
	auth, err := testenv.NewAuth("forward-user")
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	tcpip := startDirectTCPIPServer(t, auth)
	client := dialForwardClient(t, auth, tcpip.Addr())
	dc := newForwardDaemon(t)
	backend := forwardBackend(t)

	result, err := dc.ProxyStart("lifecycle", backend.URL, 0, 0, "")
	if err != nil {
		t.Fatalf("ProxyStart: %v", err)
	}
	listenAddr, _ := result["listen_addr"].(string)
	if listenAddr == "" {
		t.Fatal("ProxyStart result missing listen_addr")
	}

	var notices []string
	var mu sync.Mutex
	mgr := sshclient.NewPortForwardManager(client, dc, func(msg string) {
		mu.Lock()
		notices = append(notices, msg)
		mu.Unlock()
	})
	mgr.Start(context.Background())
	defer mgr.Stop()

	mapping := waitForwarded(t, mgr, "lifecycle")
	if mapping.LocalPort == 0 {
		t.Fatal("forwarded mapping has no local port")
	}

	// HTTP traffic flows through the forwarded local port.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", mapping.LocalPort), time.Second)
	if err != nil {
		t.Fatalf("dialing forwarded local port: %v", err)
	}
	if _, err := conn.Write([]byte("GET /page HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("writing HTTP request: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading HTTP response: %v", err)
	}
	// The proxy always wraps top-level HTML in its chrome shell (see
	// docs/responsive-canonical-target.md's always-wrap frame model), so the
	// backend's own literal content lives inside the iframe fetch rather
	// than the top-level response; the injected proxy instrumentation is the
	// signal that the byte-for-byte relay actually reached the real proxy.
	if !strings.Contains(string(body), "window.__devtool_proxy_id") || !strings.Contains(string(body), "/__devtool/inject.") {
		t.Fatalf("HTTP response through forward missing proxy instrumentation: %q", body)
	}
	conn.Close()

	// WebSocket upgrade traffic flows through the same forwarded port
	// (the reverse proxy's reserved /__devtool_metrics endpoint).
	wsURL := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", mapping.LocalPort), Path: "/__devtool_metrics"}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL.String(), http.Header{"Origin": {"http://" + wsURL.Host}})
	if err != nil {
		t.Fatalf("dialing WS through forward: %v", err)
	}
	if err := ws.WriteJSON(map[string]interface{}{"type": "custom", "level": "info", "message": "forward-ws-check"}); err != nil {
		t.Fatalf("writing WS message through forward: %v", err)
	}
	ws.Close()

	if len(mgr.Status()) != 1 {
		t.Fatalf("expected exactly one active forward, got %d", len(mgr.Status()))
	}

	// Stop tears down the local listener: further dials must fail.
	if err := dc.ProxyStop("lifecycle"); err != nil {
		t.Fatalf("ProxyStop: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(mgr.Status()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(mgr.Status()) != 0 {
		t.Fatal("forward mapping still present after ProxyStop")
	}
	if _, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", mapping.LocalPort), 500*time.Millisecond); dialErr == nil {
		t.Fatal("local listener must be closed after the remote proxy stops")
	}
}

// TestDynamicForward_LocalPortCollision exercises the fallback path: the
// remote proxy's own local listener already occupies the same port number
// startForward tries first (both "remote" and "local" run on this one
// machine in-process), so the manager must remap and emit a visible notice.
func TestDynamicForward_LocalPortCollision(t *testing.T) {
	auth, err := testenv.NewAuth("collision-user")
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	tcpip := startDirectTCPIPServer(t, auth)
	client := dialForwardClient(t, auth, tcpip.Addr())
	dc := newForwardDaemon(t)
	backend := forwardBackend(t)

	result, err := dc.ProxyStart("collision", backend.URL, 0, 0, "")
	if err != nil {
		t.Fatalf("ProxyStart: %v", err)
	}
	listenAddr, _ := result["listen_addr"].(string)
	if listenAddr == "" {
		t.Fatal("ProxyStart result missing listen_addr")
	}

	var notices []string
	var mu sync.Mutex
	mgr := sshclient.NewPortForwardManager(client, dc, func(msg string) {
		mu.Lock()
		notices = append(notices, msg)
		mu.Unlock()
	})
	mgr.Start(context.Background())
	defer mgr.Stop()

	mapping := waitForwarded(t, mgr, "collision")
	if !mapping.Remapped {
		t.Fatal("expected the forward to remap after colliding with the remote proxy's own listener")
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", mapping.LocalPort), time.Second)
	if err != nil {
		t.Fatalf("dialing remapped forwarded port: %v", err)
	}
	conn.Close()

	mu.Lock()
	joined := strings.Join(notices, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "in use locally") {
		t.Fatalf("expected a visible collision notice, got: %q", joined)
	}
}

// TestDynamicForward_ChurnUnderRace_NoGoroutineOrListenerLeak drives 20
// create/destroy cycles of a PortForwardManager (run this test file with
// `go test -race` per the task's acceptance criterion) and asserts goleak
// sees no residual goroutines once every cycle's Stop has returned.
func TestDynamicForward_ChurnUnderRace_NoGoroutineOrListenerLeak(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	defer goleak.VerifyNone(t, baseline,
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
	)

	for i := 0; i < 20; i++ {
		t.Run(fmt.Sprintf("cycle-%d", i), func(t *testing.T) {
			auth, err := testenv.NewAuth("churn-user")
			if err != nil {
				t.Fatalf("NewAuth: %v", err)
			}
			tcpip := startDirectTCPIPServer(t, auth)
			client := dialForwardClient(t, auth, tcpip.Addr())
			dc := newForwardDaemon(t)
			backend := forwardBackend(t)

			result, err := dc.ProxyStart("churn", backend.URL, 0, 0, "")
			if err != nil {
				t.Fatalf("ProxyStart: %v", err)
			}
			if _, ok := result["listen_addr"].(string); !ok {
				t.Fatal("ProxyStart result missing listen_addr")
			}

			mgr := sshclient.NewPortForwardManager(client, dc, func(string) {})
			mgr.Start(context.Background())

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && len(mgr.Status()) != 1 {
				time.Sleep(20 * time.Millisecond)
			}
			if len(mgr.Status()) != 1 {
				t.Fatal("forward never became active before cycle teardown")
			}

			mgr.Stop()
		})
	}
}
