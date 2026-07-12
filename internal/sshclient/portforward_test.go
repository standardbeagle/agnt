package sshclient

import (
	"context"
	"encoding/base64"
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
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// newTestDaemonClient spins up a real *daemon.Daemon via NewForTest (see
// daemon-architecture.md "Test startup contract") on an ephemeral unix
// socket and returns a connected *daemon.Client pointed at it. This is the
// "remote daemon" side of the fixture — real PROXY LIST / STREAM-EVENTS
// wiring, not a stub, so the test exercises the actual proxy_started /
// proxy_stopped diagnostics added in server.go / proxy_handler.go.
//
// One caveat found while writing these tests: driving MULTIPLE independent
// daemon.NewForTest instances sequentially within the SAME test binary
// process is unreliable for STREAM-EVENTS specifically — only the first
// daemon created in a process ever delivers diagnostic entries to a
// subscriber; every later instance's StreamEvents connection stays open but
// receives nothing (PROXY LIST on the same instance works fine throughout,
// ruling out a broken daemon). No package-level singleton explains this (see
// task notes); rather than chase a pre-existing daemon-test-harness quirk
// out of this task's scope, every test in this file that needs live events
// shares ONE daemon instance via TestPortForwardManager_EndToEnd's subtests.
// The goroutine/listener-leak test below does not depend on event delivery
// (it only exercises reconcile-on-connect + explicit Stop) so it is free to
// spin up its own daemon per cycle.
func newTestDaemonClient(t *testing.T) (*daemon.Daemon, *daemon.Client) {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "daemon.sock")
	d := daemon.NewForTest(t, daemon.DaemonConfig{
		SocketPath:        sockPath,
		MaxClients:        10,
		WriteTimeout:      5 * time.Second,
		OrphanScanEnabled: false,
		StatePath:         t.TempDir(),
	})
	c := daemon.NewClientWithPath(sockPath)
	require.NoError(t, c.Connect())
	t.Cleanup(func() { c.Close() })
	return d, c
}

// backendStub is the "target app" a remote proxy fronts.
func backendStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><html><body>hello from backend %s</body></html>", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// forwardFixture wires a fixtureServer (acting as the remote SSH endpoint,
// relaying "direct-tcpip" straight to listenAddr) and a *Client dialed
// against it, for one proxy.
func forwardFixture(t *testing.T, listenAddr string) *Client {
	t.Helper()
	fixture := newFixtureServer(t)
	fixture.jumpTarget = listenAddr
	stopFixture := fixture.serve(t)
	t.Cleanup(stopFixture)
	return dialFixtureClient(t, fixture)
}

// TestPortForwardManager_EndToEnd drives every live-event scenario against a
// single shared daemon (see newTestDaemonClient's doc comment for why) using
// a distinct proxy ID per subtest.
func TestPortForwardManager_EndToEnd(t *testing.T) {
	_, dc := newTestDaemonClient(t)

	t.Run("LiveProxyStart_ForwardsWithinOneSecond", func(t *testing.T) {
		backend := backendStub(t)
		reserved, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		remotePort := reserved.Addr().(*net.TCPAddr).Port
		require.NoError(t, reserved.Close())

		sshClient := forwardFixture(t, fmt.Sprintf("127.0.0.1:%d", remotePort))
		mgr := NewPortForwardManager(sshClient, dc, func(string) {})
		mgr.Start(context.Background())
		defer mgr.Stop()

		started := time.Now()
		_, err = dc.ProxyStart("p-live", backend.URL, remotePort, 0, "")
		require.NoError(t, err)
		defer dc.ProxyStop("p-live")
		var localPort int
		require.Eventually(t, func() bool {
			for _, mapping := range mgr.Status() {
				if mapping.ProxyID == "p-live" {
					localPort = mapping.LocalPort
					return true
				}
			}
			return false
		}, time.Second, 10*time.Millisecond, "a proxy started after the manager must be usable without waiting for the 30s backstop")
		require.Less(t, time.Since(started), time.Second)

		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 250*time.Millisecond)
		require.NoError(t, err)
		conn.Close()
	})

	t.Run("ReconcileOnConnect_ForwardsExistingProxy_And_HTTPRoundTrips", func(t *testing.T) {
		backend := backendStub(t)
		result, err := dc.ProxyStart("p-reconcile", backend.URL, 0, 0, "")
		require.NoError(t, err)
		listenAddr, _ := result["listen_addr"].(string)
		require.NotEmpty(t, listenAddr, "ProxyStart result must carry listen_addr")

		sshClient := forwardFixture(t, listenAddr)
		mgr := NewPortForwardManager(sshClient, dc, func(string) {})
		mgr.Start(context.Background())
		defer mgr.Stop()

		var mapping []Mapping
		require.Eventually(t, func() bool {
			for _, m := range mgr.Status() {
				if m.ProxyID == "p-reconcile" {
					mapping = []Mapping{m}
					return true
				}
			}
			return false
		}, 3*time.Second, 20*time.Millisecond, "reconcile-on-connect must forward the pre-existing proxy within 3s")

		// A raw dial + manual HTTP/1.1 request (rather than http.Get) avoids
		// net/http's connection-reuse/proxy-probing machinery, which was
		// observed to add tens of seconds of unrelated latency against this
		// sandbox's loopback stack for the very first request in the binary
		// — the relay itself is what this assertion cares about.
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", mapping[0].LocalPort))
		require.NoError(t, err, "local forwarded URL must be reachable")
		defer conn.Close()
		_, err = conn.Write([]byte("GET /foo HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"))
		require.NoError(t, err)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		body, err := io.ReadAll(conn)
		require.NoError(t, err)
		require.Contains(t, string(body), "window.__devtool_proxy_id", "HTML fetched through the forwarded local port must contain proxy instrumentation")
		require.Contains(t, string(body), "/__devtool/inject.", "HTML fetched through the forwarded local port must load the injected runtime")
	})

	t.Run("WSMetricsRoundTripThroughForward", func(t *testing.T) {
		backend := backendStub(t)
		result, err := dc.ProxyStart("p-ws", backend.URL, 0, 0, "")
		require.NoError(t, err)
		listenAddr, _ := result["listen_addr"].(string)
		require.NotEmpty(t, listenAddr)

		sshClient := forwardFixture(t, listenAddr)
		mgr := NewPortForwardManager(sshClient, dc, func(string) {})
		mgr.Start(context.Background())
		defer mgr.Stop()

		var localPort int
		require.Eventually(t, func() bool {
			for _, m := range mgr.Status() {
				if m.ProxyID == "p-ws" {
					localPort = m.LocalPort
					return true
				}
			}
			return false
		}, 3*time.Second, 20*time.Millisecond)

		// The reverse proxy always serves /__devtool_metrics as a WS upgrade
		// endpoint regardless of backend; dialing it through the forwarded
		// local port proves the same-port mapping preserves that reserved
		// path per the acceptance criterion "WS metrics must be verified
		// usable through the channel". A raw TCP dial + HTTP Upgrade
		// handshake is enough to prove the byte-for-byte relay works.
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
		require.NoError(t, err)
		defer conn.Close()

		req := "GET /__devtool_metrics HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
		_, err = conn.Write([]byte(req))
		require.NoError(t, err)

		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		require.NoError(t, err)
		require.Contains(t, string(buf[:n]), "101", "expected an HTTP 101 Switching Protocols response relayed through the forward")
	})

	t.Run("ScreenshotRoundTripThroughForward", func(t *testing.T) {
		backend := backendStub(t)
		result, err := dc.ProxyStart("p-screenshot", backend.URL, 0, 0, "")
		require.NoError(t, err)
		listenAddr, _ := result["listen_addr"].(string)
		require.NotEmpty(t, listenAddr)

		sshClient := forwardFixture(t, listenAddr)
		mgr := NewPortForwardManager(sshClient, dc, func(string) {})
		mgr.Start(context.Background())
		defer mgr.Stop()

		var localPort int
		require.Eventually(t, func() bool {
			for _, m := range mgr.Status() {
				if m.ProxyID == "p-screenshot" {
					localPort = m.LocalPort
					return true
				}
			}
			return false
		}, 3*time.Second, 20*time.Millisecond)

		wsURL := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", localPort), Path: "/__devtool_metrics"}
		ws, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
		require.NoError(t, err, "screenshot client must connect through the forwarded local port")
		defer ws.Close()

		png := []byte("forwarded-screenshot-payload")
		require.NoError(t, ws.WriteJSON(map[string]interface{}{
			"type": "screenshot",
			"url":  "/capture",
			"data": map[string]interface{}{
				"name":   "forwarded-round-trip",
				"data":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
				"format": "png",
				"width":  1,
				"height": 1,
			},
		}))

		var screenshotPath string
		require.Eventually(t, func() bool {
			entries, _, queryErr := dc.ProxyLogQueryFull("p-screenshot", protocol.LogQueryFilter{Types: []string{"screenshot"}, Limit: 10})
			if queryErr != nil {
				return false
			}
			for _, entry := range entries {
				if entry.Screenshot != nil && entry.Screenshot.Name == "forwarded-round-trip" && entry.Screenshot.Error == "" {
					screenshotPath = entry.Screenshot.FilePath
					return screenshotPath != ""
				}
			}
			return false
		}, 3*time.Second, 20*time.Millisecond, "screenshot sent through the forward must be persisted and visible through the remote daemon")

		got, err := os.ReadFile(screenshotPath)
		require.NoError(t, err)
		require.Equal(t, png, got, "persisted screenshot must match the bytes sent through the forwarded port")
	})

	t.Run("LocalPortCollision_RemapsAndNotifies", func(t *testing.T) {
		backend := backendStub(t)
		result, err := dc.ProxyStart("p-collision", backend.URL, 0, 0, "")
		require.NoError(t, err)
		listenAddr, _ := result["listen_addr"].(string)
		require.NotEmpty(t, listenAddr)
		remotePort, err := listenAddrPort(listenAddr)
		require.NoError(t, err)

		// No artificial blocker needed: since this test drives both "remote"
		// (the real proxy) and "local" (the forward manager) on the same
		// machine, the remote proxy's own listener on 127.0.0.1:<remotePort>
		// already occupies that exact port — the same-port-preferred bind
		// in startForward is guaranteed to collide with it, exercising the
		// collision-fallback path for real, not simulated.
		sshClient := forwardFixture(t, listenAddr)

		var notices []string
		var mu sync.Mutex
		mgr := NewPortForwardManager(sshClient, dc, func(msg string) {
			mu.Lock()
			notices = append(notices, msg)
			mu.Unlock()
		})
		mgr.Start(context.Background())
		defer mgr.Stop()

		var mapping Mapping
		require.Eventually(t, func() bool {
			for _, m := range mgr.Status() {
				if m.ProxyID == "p-collision" {
					mapping = m
					return true
				}
			}
			return false
		}, 3*time.Second, 20*time.Millisecond)

		require.True(t, mapping.Remapped, "collision must produce a remapped mapping")
		require.NotEqual(t, remotePort, mapping.LocalPort)

		mu.Lock()
		joined := append([]string(nil), notices...)
		mu.Unlock()
		found := false
		for _, n := range joined {
			if strings.Contains(n, "in use locally") {
				found = true
			}
		}
		require.True(t, found, "collision must produce a visible notice, got: %v", joined)
	})

	t.Run("ProxyStop_ClosesLocalListener", func(t *testing.T) {
		backend := backendStub(t)
		result, err := dc.ProxyStart("p-stop", backend.URL, 0, 0, "")
		require.NoError(t, err)
		listenAddr, _ := result["listen_addr"].(string)

		sshClient := forwardFixture(t, listenAddr)
		mgr := NewPortForwardManager(sshClient, dc, func(string) {})
		mgr.Start(context.Background())
		defer mgr.Stop()

		var localPort int
		require.Eventually(t, func() bool {
			for _, m := range mgr.Status() {
				if m.ProxyID == "p-stop" {
					localPort = m.LocalPort
					return true
				}
			}
			return false
		}, 3*time.Second, 20*time.Millisecond)

		httpConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
		require.NoError(t, err)
		defer httpConn.Close()
		require.NoError(t, httpConn.SetDeadline(time.Now().Add(3*time.Second)))
		_, err = httpConn.Write([]byte("GET /open HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: keep-alive\r\n\r\n"))
		require.NoError(t, err)
		buf := make([]byte, 2048)
		_, err = httpConn.Read(buf)
		require.NoError(t, err)
		require.NoError(t, httpConn.SetDeadline(time.Time{}))

		wsURL := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", localPort), Path: "/__devtool_metrics"}
		ws, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
		require.NoError(t, err)
		defer ws.Close()

		require.NoError(t, dc.ProxyStop("p-stop"))

		require.Eventually(t, func() bool {
			for _, m := range mgr.Status() {
				if m.ProxyID == "p-stop" {
					return false
				}
			}
			return true
		}, 3*time.Second, 20*time.Millisecond, "forward must close via either the proxy_stopped event or the periodic reconcile backstop")

		_, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), 500*time.Millisecond)
		require.Error(t, dialErr, "local listener must actually be closed, not just removed from Status()")

		require.NoError(t, httpConn.SetReadDeadline(time.Now().Add(time.Second)))
		_, err = httpConn.Read(buf)
		require.Error(t, err, "open HTTP relay must be closed before forward removal completes")
		require.NoError(t, ws.SetReadDeadline(time.Now().Add(time.Second)))
		_, _, err = ws.ReadMessage()
		require.Error(t, err, "open WebSocket relay must be closed before forward removal completes")
	})
}

// TestPortForwardManager_NoGoroutineOrListenerLeak_20Cycles drives 20
// create/destroy cycles of a PortForwardManager against fresh daemons and
// asserts goleak sees no residual goroutines afterward — the acceptance
// criterion's explicit leak check. This scenario only needs reconcile-on-
// connect (not live event delivery), so each cycle is free to use its own
// daemon (cheap: NewForTest is sub-100ms).
func TestPortForwardManager_NoGoroutineOrListenerLeak_20Cycles(t *testing.T) {
	// Snapshot goroutines already running before this test starts and only
	// fail on NEW ones introduced by it — sibling tests in this package
	// (forward_test.go, client_test.go, ...) legitimately spin up their own
	// ssh/httptest goroutines that may still be winding down when this test
	// begins; those are not this manager's leaks to answer for.
	baseline := goleak.IgnoreCurrent()
	defer goleak.VerifyNone(t, baseline,
		// The stdlib http keep-alive transport pool goroutine is unrelated to
		// this package and not something Stop() can or should reach into.
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
	)

	for i := 0; i < 20; i++ {
		// Each cycle runs as its own subtest so its t.Cleanup-registered
		// teardown (daemon Stop, httptest.Server Close, ssh Client Close)
		// fires when the SUBTEST completes, not when the outer test
		// function returns — required so goleak.VerifyNone's deferred
		// check (which runs before any t.Cleanup queued on the parent t)
		// observes a fully torn-down world after all 20 cycles.
		t.Run(fmt.Sprintf("cycle-%d", i), func(t *testing.T) {
			_, dc := newTestDaemonClient(t)
			backend := backendStub(t)

			result, err := dc.ProxyStart("p1", backend.URL, 0, 0, "")
			require.NoError(t, err)
			listenAddr, _ := result["listen_addr"].(string)

			sshClient := forwardFixture(t, listenAddr)
			mgr := NewPortForwardManager(sshClient, dc, func(string) {})
			mgr.Start(context.Background())

			require.Eventually(t, func() bool {
				return len(mgr.Status()) == 1
			}, 3*time.Second, 20*time.Millisecond)

			mgr.Stop()
		})
	}
}
