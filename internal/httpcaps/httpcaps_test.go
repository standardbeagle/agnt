package httpcaps

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// serveCapped stands up a real listener + capped server for a test and returns
// its address plus a stop func. LimitListener is applied so the connection cap
// is exercised end to end.
func serveCapped(t *testing.T, caps Caps, h http.Handler) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln = caps.LimitListener(ln)
	srv := caps.NewServer(h)
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// TestReadHeaderTimeout_DropsStalledClient is the slowloris teeth test: a client
// that dribbles a partial header set and never terminates them must have its
// connection dropped at ReadHeaderTimeout, not pinned forever.
//
// MUTATION PROOF: set ReadHeaderTimeout to 0 (remove the cap) and this test
// FAILS — the server never times out the headers, so the client Read below
// blocks until its own 3s deadline, blowing the sub-1s elapsed assertion.
func TestReadHeaderTimeout_DropsStalledClient(t *testing.T) {
	caps := Default()
	caps.ReadHeaderTimeout = 150 * time.Millisecond
	addr, stop := serveCapped(t, caps, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// A request line + one header, but NEVER the terminating blank line: the
	// header phase can never complete, so only ReadHeaderTimeout can end it.
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: example.com\r\nX-Stall: 1"); err != nil {
		t.Fatalf("write partial headers: %v", err)
	}

	// Generous client deadline: far longer than ReadHeaderTimeout, so a working
	// cap returns well under it and a removed cap trips it.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	buf := make([]byte, 512)
	// Read until the server acts (EOF, reset, or a 408 then close). Either way
	// the connection must become unusable promptly.
	_, readErr := conn.Read(buf)
	elapsed := time.Since(start)

	if readErr == nil {
		// A well-behaved cap closes or 408s; a lone successful read of a full
		// response would mean the request completed, which it cannot here.
		t.Fatalf("expected the stalled connection to be dropped, got a clean read")
	}
	if elapsed > time.Second {
		t.Fatalf("stalled connection was not dropped promptly: elapsed=%v (ReadHeaderTimeout=%v). "+
			"With the timeout removed this is where the test fails.", elapsed, caps.ReadHeaderTimeout)
	}
}

// TestLimitListener_RefusesOverCap is the connection-cap teeth test. It does NOT
// merely assert that MaxConns requests succeed; it POSITIVELY asserts the
// (MaxConns+1)th request is refused service (never reaches the handler) while the
// first MaxConns are held open, then that it proceeds once one slot frees.
//
// MUTATION PROOF: set caps.MaxConns to 0 (LimitListener returns the raw
// listener, removing the cap) and this test FAILS — the 3rd request is accepted
// and reaches the handler immediately, so the "did not reach within the window"
// assertion fires.
func TestLimitListener_RefusesOverCap(t *testing.T) {
	const maxConns = 2
	caps := Default()
	caps.MaxConns = maxConns

	reached := make(chan struct{}, 8)
	release := make(chan struct{})
	var once sync.Once
	addr, stop := serveCapped(t, caps, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-release // hold the connection (and its LimitListener slot) open
		w.WriteHeader(http.StatusOK)
	}))
	defer stop()
	defer once.Do(func() { close(release) })

	// Saturate the cap: maxConns requests that each reach the handler and block.
	for i := 0; i < maxConns; i++ {
		go func() {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				return
			}
			defer c.Close()
			fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
			io.Copy(io.Discard, c)
		}()
	}
	for i := 0; i < maxConns; i++ {
		select {
		case <-reached:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d saturating requests reached the handler", i, maxConns)
		}
	}

	// The (maxConns+1)th request. Its TCP connect succeeds (kernel backlog) but
	// LimitListener will not Accept it while the cap is saturated, so it must NOT
	// reach the handler.
	extra, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial extra: %v", err)
	}
	defer extra.Close()
	fmt.Fprintf(extra, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")

	select {
	case <-reached:
		t.Fatalf("over-cap connection was served while %d were held — the connection cap is not enforced "+
			"(this is where the test fails with MaxConns removed)", maxConns)
	case <-time.After(400 * time.Millisecond):
		// Correct: refused (not accepted) while the cap is saturated.
	}

	// Free one slot; the held-over connection must now be accepted and served.
	release <- struct{}{}
	select {
	case <-reached:
		// The extra connection got its slot once one freed — the cap is a
		// throttle, not a permanent drop.
	case <-time.After(2 * time.Second):
		t.Fatalf("freed connection slot was never handed to the waiting connection")
	}

	// Drain the response so the goroutine/handler can exit cleanly.
	_ = extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = bufio.NewReader(extra).ReadString('\n')
}

// TestStreaming_DropsBodyDeadlines documents the streaming carve-out: a
// streaming listener keeps the header/idle/connection bounds but drops the
// whole-request read/write deadlines that would sever a hijacked or streamed
// connection.
func TestStreaming_DropsBodyDeadlines(t *testing.T) {
	s := Streaming().NewServer(nil)
	if s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Fatalf("streaming server must drop Read/Write timeouts, got read=%v write=%v", s.ReadTimeout, s.WriteTimeout)
	}
	if s.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Fatalf("streaming server must keep ReadHeaderTimeout, got %v", s.ReadHeaderTimeout)
	}
	if s.IdleTimeout != DefaultIdleTimeout {
		t.Fatalf("streaming server must keep IdleTimeout, got %v", s.IdleTimeout)
	}
	if s.MaxHeaderBytes != DefaultMaxHeaderBytes {
		t.Fatalf("streaming server must keep MaxHeaderBytes, got %d", s.MaxHeaderBytes)
	}

	d := Default().NewServer(nil)
	if d.ReadTimeout == 0 || d.WriteTimeout == 0 {
		t.Fatalf("default (non-streaming) server must set Read/Write timeouts, got read=%v write=%v", d.ReadTimeout, d.WriteTimeout)
	}
}
