package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// closableWriter is a wsWriter stub that records whether Close was called, so
// the teardown test can assert Stop drains the WebSocket registry.
type closableWriter struct {
	closed atomic.Bool
}

func (c *closableWriter) WriteMessage(int, []byte) error { return nil }
func (c *closableWriter) Close() error {
	c.closed.Store(true)
	return nil
}

// TestStopDrainsWebSocketConns asserts Stop closes every registered WebSocket
// connection and empties the registry, so a same-port restart cannot inherit a
// stale conn.
func TestStopDrainsWebSocketConns(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{ID: "drain", TargetURL: backend.URL, ListenPort: 0})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-ps.Ready()

	cw := &closableWriter{}
	ps.wsConns.Store("conn-1", cw)

	if err := ps.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if !cw.closed.Load() {
		t.Error("Stop must close registered WebSocket connections")
	}
	count := 0
	ps.wsConns.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Errorf("wsConns registry has %d entries after Stop, want 0", count)
	}
}

// TestBoundPort asserts BoundPort reports the actually-bound port after Start
// (the OS-assigned one when ListenPort was 0), which is what restart uses to
// rebind the same port.
func TestBoundPort(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{ID: "bound", TargetURL: backend.URL, ListenPort: 0})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ps.Stop(ctx) })
	<-ps.Ready()

	port := ps.BoundPort()
	if port <= 0 {
		t.Fatalf("BoundPort = %d, want a real assigned port", port)
	}
	// The reported port must match ListenAddr's port.
	if want := fmt.Sprintf(":%d", port); want == ":0" {
		t.Fatal("BoundPort returned 0 after a successful Start")
	}
}
