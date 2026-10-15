package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newChaosTestProxy builds an unstarted proxy in front of a backend that
// answers every request with body, and enables chaos with rules.
func newChaosTestProxy(t *testing.T, body string, rules ...*ChaosRule) *ProxyServer {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{ID: "chaos-handler", TargetURL: backend.URL, ListenPort: 0})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	t.Cleanup(func() {
		ps.StopChaos()
		ps.pageTracker.Stop()
	})
	if err := ps.chaosEngine.SetConfig(&ChaosConfig{Enabled: true, Rules: rules}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	return ps
}

// serveThroughProxy runs one request through handleProxy on a goroutine, so
// a handler that never returns fails the test instead of hanging it.
func serveThroughProxy(t *testing.T, ps *ProxyServer, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ps.handleProxy(rr, req)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handleProxy did not return")
	}
	return rr
}

// The body-shaping chaos writers (truncate, slow_drip, disconnect) wrap the
// response recorder. handleProxy then pointed the recorder at the writer
// that wraps it, so every Header/Write call recursed between the two until
// the goroutine stack overflowed — a fatal error that takes the daemon down,
// on the first request a truncate rule matched.
func TestHandleProxy_TruncateRuleShapesTheBody(t *testing.T) {
	body := strings.Repeat("x", 20*1024)
	ps := newChaosTestProxy(t, body, &ChaosRule{ID: "t", Type: ChaosTruncate, Enabled: true, TruncatePercent: 0.25})

	rr := serveThroughProxy(t, ps, "/data.json")

	got := rr.Body.Len()
	if got == 0 || got >= len(body) {
		t.Fatalf("truncate rule wrote %d of %d bytes; want a non-empty prefix", got, len(body))
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestHandleProxy_SlowDripRuleDeliversTheWholeBody(t *testing.T) {
	body := strings.Repeat("y", 4096)
	ps := newChaosTestProxy(t, body, &ChaosRule{ID: "s", Type: ChaosSlowDrip, Enabled: true, BytesPerMs: 4096, ChunkSize: 512})

	rr := serveThroughProxy(t, ps, "/data.json")

	if rr.Body.String() != body {
		t.Fatalf("slow_drip delivered %d of %d bytes", rr.Body.Len(), len(body))
	}
}

// Under a real server ReverseProxy aborts a response whose body copy fails by
// panicking with http.ErrAbortHandler, so the logging after ServeHTTP never
// ran: a dropped connection (chaos disconnect here; a client leaving mid-body
// in practice) left no traffic-log entry for the request at all.
func TestHandleProxy_AbortedResponseIsStillLogged(t *testing.T) {
	body := strings.Repeat("z", 20*1024)
	ps := newChaosTestProxy(t, body, &ChaosRule{ID: "d", Type: ChaosDisconnect, Enabled: true, DropAfterBytes: 1000})
	front := httptest.NewServer(http.HandlerFunc(ps.handleProxy))
	t.Cleanup(front.Close)

	resp, err := http.Get(front.URL + "/aborted.json")
	if err == nil {
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var found *HTTPLogEntry
		for _, e := range ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}}) {
			if e.HTTP != nil && strings.Contains(e.HTTP.URL, "/aborted.json") {
				found = e.HTTP
			}
		}
		if found != nil {
			if found.Error == "" {
				t.Fatalf("aborted response logged without an error: %+v", found)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the aborted request has no traffic-log entry")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHandleProxy_DisconnectRuleStopsTheBody(t *testing.T) {
	body := strings.Repeat("z", 20*1024)
	ps := newChaosTestProxy(t, body, &ChaosRule{ID: "d", Type: ChaosDisconnect, Enabled: true, DropAfterBytes: 1000})

	rr := serveThroughProxy(t, ps, "/data.json")

	if got := rr.Body.Len(); got != 1000 {
		t.Fatalf("disconnect rule let %d bytes through, want exactly 1000", got)
	}
}
