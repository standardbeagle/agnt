package proxy

import (
	"context"
	"net"
	"runtime"
	"testing"
)

// TestStop_ReleasesTheListenerEvenBeforeServeRuns pins that Stop frees the
// listen port by the time it returns. Start hands the listener to a runServer
// goroutine; when Stop lands before that goroutine reaches Serve,
// http.Server.Shutdown has no tracked listener to close, so the socket stayed
// bound until the goroutine eventually ran. A Replace on the same declared
// port (StrictListenPort) then failed with "address already in use" — the
// macOS CI failure of TestProxyManager_ReplaceHoldsTheIDAcrossTheRestart.
// GOMAXPROCS(1) keeps the runServer goroutine from running before Stop, which
// makes the window deterministic instead of scheduler-dependent.
func TestStop_ReleasesTheListenerEvenBeforeServeRuns(t *testing.T) {
	prev := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })

	for i := 0; i < 20; i++ {
		ps, err := NewProxyServer(ProxyConfig{
			ID: "stop-early", TargetURL: "http://127.0.0.1:1", MaxLogSize: 10,
		})
		if err != nil {
			t.Fatalf("NewProxyServer: %v", err)
		}
		if err := ps.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		addr := ps.liveAddr()
		if err := ps.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("iteration %d: port %s still bound after Stop returned: %v", i, addr, err)
		}
		_ = l.Close()
	}
}
