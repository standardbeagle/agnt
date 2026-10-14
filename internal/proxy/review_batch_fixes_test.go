package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBatchProxy is newGateProxy (listening on 127.0.0.1:8123, never started)
// pointed at target.
func newBatchProxy(t *testing.T, target string) *ProxyServer {
	t.Helper()
	ps, err := NewProxyServer(ProxyConfig{
		ID: "batch", TargetURL: target, ListenPort: 8123,
		MaxLogSize: 100, Path: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })
	return ps
}

func httpEntries(ps *ProxyServer) []HTTPLogEntry {
	var out []HTTPLogEntry
	for _, e := range ps.logger.Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}}) {
		if e.HTTP != nil {
			out = append(out, *e.HTTP)
		}
	}
	return out
}

func diagnostics(ps *ProxyServer) []ProxyDiagnostic {
	var out []ProxyDiagnostic
	for _, e := range ps.logger.Query(LogFilter{Types: []LogEntryType{LogTypeDiagnostic}}) {
		if e.Diagnostic != nil {
			out = append(out, *e.Diagnostic)
		}
	}
	return out
}

// A failed upstream request is one HTTP entry, not two: errorHandler used to
// log a 502 and then write the 502 into handleProxy's recorder, which logged
// it again under a second id. The diagnostic points at the one entry.
func TestProxyError_LoggedOnceUnderTheRequestsID(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler) // upstream drops the connection
	}))
	defer backend.Close()
	ps := newBatchProxy(t, backend.URL)

	r := httptest.NewRequest(http.MethodGet, "http://localhost:8123/api/data", nil)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	ps.handleProxy(w, r)
	require.Equal(t, http.StatusBadGateway, w.Code)

	entries := httpEntries(ps)
	require.Len(t, entries, 1, "a failed request must be logged exactly once")
	assert.Equal(t, http.StatusBadGateway, entries[0].StatusCode)
	assert.NotEmpty(t, entries[0].Error, "the entry carries the proxy error")

	diags := diagnostics(ps)
	require.NotEmpty(t, diags)
	assert.Equal(t, entries[0].ID, diags[len(diags)-1].RequestID, "the diagnostic names the request's entry")
}

// A request the client abandoned is recorded once, as 499 — not a 502 that
// the incident adapter would turn into an error-severity http_5xx incident.
func TestProxyError_ClientCancelRecordedAs499(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer backend.Close()
	defer close(release)
	ps := newBatchProxy(t, backend.URL)

	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "http://localhost:8123/api/slow", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ps.handleProxy(httptest.NewRecorder(), r)
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("backend never reached")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleProxy did not return after the client cancelled")
	}

	entries := httpEntries(ps)
	require.Len(t, entries, 1)
	assert.Equal(t, StatusClientClosedRequest, entries[0].StatusCode)
}

// The proxy-error diagnostic scrubs a share token from the URL, and is sent
// to trusted connections only.
func TestProxyErrorDiagnostic_ScrubbedAndTrustedOnly(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer backend.Close()
	ps := newBatchProxy(t, backend.URL)
	_, pageStub := registerConn(t, ps, "conn-page", true)
	_, visitorStub := registerConn(t, ps, "conn-visitor", false)

	const token = "SECRETTOKENabcdefghijklmnop123456"
	r := httptest.NewRequest(http.MethodGet, "http://localhost:8123/s/"+token+"/page", nil)
	r.Header.Set("Accept", "application/json")
	ps.handleProxy(httptest.NewRecorder(), r)

	diags := diagnostics(ps)
	require.NotEmpty(t, diags)
	assert.NotContains(t, diags[len(diags)-1].URL, token, "the diagnostic URL must be scrubbed")

	require.Eventually(t, func() bool { return len(framesOfType(t, pageStub, "proxy_diagnostic")) == 1 },
		3*time.Second, 5*time.Millisecond, "premise: the trusted page gets the diagnostic")
	for _, f := range pageStub.frames(t) {
		raw, _ := json.Marshal(f)
		assert.NotContains(t, string(raw), token)
	}
	assert.Zero(t, visitorStub.count(), "an untrusted connection must not be sent the diagnostic")

	// Outside handleProxy (a WebSocket upgrade) errorHandler logs the entry
	// itself, from the outbound request whose URL is the absolute target URL.
	out := httptest.NewRequest(http.MethodGet, backend.URL+"/s/"+token+"/ws", nil)
	ps.errorHandler(httptest.NewRecorder(), out, errors.New("EOF"))
	entries := httpEntries(ps)
	require.NotEmpty(t, entries)
	assert.NotContains(t, entries[len(entries)-1].URL, token, "the HTTP entry URL must be scrubbed")
}

// The HTTP-error auto-forward types the request's method and path into the
// agent's terminal. A tunnel visitor and a cross-site page choose that path,
// so neither is forwarded; the local developer's request is, without its
// headers or body.
func TestHTTPErrorForward_OnlyTrustedCallersAndNoHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not here", http.StatusNotFound)
	}))
	defer backend.Close()
	ps := newBatchProxy(t, backend.URL)
	oc, sock := startOverlayCapture(t)
	ps.overlayNotifier.SetEndpoint(sock)
	startedTunnel(t, ps, gateTunnelURL, TunnelKindCloudflareQuick)

	send := func(shape upgradeShape, path string, mutate func(*http.Request)) {
		r := httptest.NewRequest(http.MethodPost, "http://"+shape.host+path, strings.NewReader("secret-request-body"))
		r.Host = shape.host
		r.RemoteAddr = shape.peer
		for k, v := range shape.headers {
			r.Header.Set(k, v)
		}
		if mutate != nil {
			mutate(r)
		}
		ps.handleProxy(httptest.NewRecorder(), r)
	}

	send(tunnelVisitor(), "/visitor-chosen-path", nil)
	local := localBrowser()
	local.origin = "" // a navigation or same-origin GET carries no Origin
	send(local, "/cross-site-chosen-path", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	send(local, "/api/missing", func(r *http.Request) {
		r.Header.Set("Cookie", "session=secret-cookie")
		r.Header.Set("Authorization", "Bearer secret-bearer")
	})

	// One sender drains the queue in order: once the local request's event
	// has arrived, any earlier event would have arrived before it.
	require.Eventually(t, func() bool { return len(oc.all()) >= 1 }, 5*time.Second, 5*time.Millisecond,
		"the local developer's 404 must be forwarded")
	bodies := oc.all()
	require.Len(t, bodies, 1, "only the trusted caller's error is forwarded: %v", bodies)
	got := bodies[0]
	assert.Contains(t, got, "/api/missing")
	assert.Contains(t, got, `"status_code":404`)
	for _, leak := range []string{"secret-cookie", "secret-bearer", "secret-request-body", "request_headers", "response_headers"} {
		assert.NotContains(t, got, leak)
	}

	// The traffic log records the trust of each error response's caller, on
	// the envelope and the payload, so the incident adapter can label it.
	marked := map[string]bool{}
	for _, e := range ps.logger.Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}}) {
		require.Equal(t, e.Untrusted, e.HTTP.Untrusted, "envelope and payload must agree for %s", e.HTTP.URL)
		u, err := url.Parse(e.HTTP.URL)
		require.NoError(t, err)
		marked[u.Path] = e.Untrusted
	}
	require.Len(t, marked, 3, "all three requests must be logged: %v", marked)
	assert.True(t, marked["/visitor-chosen-path"], "a tunnel visitor's error must be marked untrusted")
	assert.True(t, marked["/cross-site-chosen-path"], "a cross-site request's error must be marked untrusted")
	assert.False(t, marked["/api/missing"], "the local developer's error must not be marked")
}

// EnqueueHTTPError and EnqueueBrowserError never block their caller (the
// request goroutine, the control socket's read loop), even when the overlay
// accepts connections and never answers: excess events are dropped and
// counted.
func TestEnqueueErrors_NonBlockingWithDropCount(t *testing.T) {
	ln, sock := listenUnix(t)
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(release); _ = srv.Close() })

	n := NewOverlayNotifier()
	n.SetEndpoint(sock)
	const events = errorQueueSize * 4
	start := time.Now()
	for i := 0; i < events; i++ {
		if i%2 == 0 {
			n.EnqueueHTTPError("p", HTTPLogEntry{Method: "GET", URL: "/x" + strconv.Itoa(i), StatusCode: 500})
		} else {
			n.EnqueueBrowserError("p", FrontendError{Message: "TypeError " + strconv.Itoa(i)})
		}
	}
	assert.Less(t, time.Since(start), time.Second, "enqueueing must not wait on the overlay")
	assert.Positive(t, n.ErrorsDropped(), "a full queue drops and counts")
	assert.LessOrEqual(t, n.ErrorsDropped(), int64(events))
}

func listenUnix(t *testing.T) (net.Listener, string) {
	t.Helper()
	sock := filepath.Join(sockdir.Short(t), "overlay.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	return ln, sock
}

// closeTrackingBody fails every read after Close, as a real request body
// does once the transport has closed it.
type closeTrackingBody struct {
	r      io.Reader
	mu     sync.Mutex
	closed bool
}

func (b *closeTrackingBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errors.New("read on closed body")
	}
	return b.r.Read(p)
}

func (b *closeTrackingBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

// The insecure retry after a certificate error replays the request body
// through GetBody: the failed secure attempt has already closed the original.
// Without GetBody the request fails with the certificate error rather than
// being sent with a dead body.
func TestTLSFallback_RetryReplaysBody(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, strconv.Itoa(len(b)))
	}))
	defer backend.Close()
	payload := bytes.Repeat([]byte("x"), 64*1024) // past the 10KB capture cap

	newReq := func(withGetBody bool) *http.Request {
		req, err := http.NewRequest(http.MethodPost, backend.URL, nil)
		require.NoError(t, err)
		req.Body = &closeTrackingBody{r: bytes.NewReader(payload)}
		req.ContentLength = int64(len(payload))
		if withGetBody {
			req.GetBody = func() (io.ReadCloser, error) {
				return &closeTrackingBody{r: bytes.NewReader(payload)}, nil
			}
		}
		return req
	}

	t.Run("replayable body is resent whole", func(t *testing.T) {
		tr := NewTLSFallbackTransport(&http.Transport{TLSClientConfig: &tls.Config{}}, nil)
		resp, err := tr.RoundTrip(newReq(true))
		require.NoError(t, err)
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		assert.Equal(t, strconv.Itoa(len(payload)), string(got))
	})

	t.Run("non-replayable body fails with the certificate error", func(t *testing.T) {
		tr := NewTLSFallbackTransport(&http.Transport{TLSClientConfig: &tls.Config{}}, nil)
		_, err := tr.RoundTrip(newReq(false))
		require.Error(t, err)
		assert.True(t, isCertError(err), "the certificate error is kept: %v", err)
		assert.True(t, tr.skipped.Load(), "the bypass is active for the next request")
	})
}

// modifyResponse leaves responses that may not carry a body alone.
func TestModifyResponse_NoInjectionWithoutABody(t *testing.T) {
	ps := newGateProxy(t)
	const doc = "<!DOCTYPE html><html><head><title>t</title></head><body>x</body></html>"

	cases := []struct {
		name   string
		method string
		status int
	}{
		{"HEAD", http.MethodHead, http.StatusOK},
		{"204", http.MethodGet, http.StatusNoContent},
		{"304", http.MethodGet, http.StatusNotModified},
		{"103", http.MethodGet, http.StatusEarlyHints},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://localhost:8123/page?__agnt_frame=x", nil)
			resp := &http.Response{
				StatusCode:    tc.status,
				Header:        http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Content-Length": {strconv.Itoa(len(doc))}},
				Body:          io.NopCloser(strings.NewReader(doc)),
				ContentLength: int64(len(doc)),
				Request:       req,
			}
			require.NoError(t, ps.modifyResponse(resp))
			got, _ := io.ReadAll(resp.Body)
			assert.Equal(t, doc, string(got), "the body must not be rewritten")
			assert.Equal(t, strconv.Itoa(len(doc)), resp.Header.Get("Content-Length"))
		})
	}

	t.Run("premise: a GET 200 is injected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8123/page", nil)
		resp := &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}},
			Body: io.NopCloser(strings.NewReader(doc)), ContentLength: int64(len(doc)), Request: req,
		}
		require.NoError(t, ps.modifyResponse(resp))
		got, _ := io.ReadAll(resp.Body)
		assert.NotEqual(t, doc, string(got))
	})
}

// A page session's errors and navigations are bounded like its other
// histories; error frames (accepted from untrusted connections as telemetry)
// have their text capped at parse.
func TestPageSession_ErrorsAndNavigationsBounded(t *testing.T) {
	pt := NewPageTracker(100, time.Hour)
	defer pt.Stop()
	const sid = "tab-1"
	cookie := map[string]string{"Cookie": "__devtool_sid=" + sid}
	html := map[string]string{"Content-Type": "text/html"}
	for i := 0; i < MaxNavigationsPerSession+50; i++ {
		pt.TrackHTTPRequest(HTTPLogEntry{
			ID: "n" + strconv.Itoa(i), Method: "GET", URL: "http://localhost:8123/p" + strconv.Itoa(i),
			StatusCode: 200, RequestHeaders: cookie, ResponseHeaders: html,
		})
	}
	for i := 0; i < MaxErrorsPerSession+50; i++ {
		pt.TrackError(FrontendError{Message: "e" + strconv.Itoa(i), URL: "http://localhost:8123/p1"}, sid)
	}
	sessions := pt.GetActiveSessions()
	require.Len(t, sessions, 1)
	s := sessions[0]
	assert.Len(t, s.Navigations, MaxNavigationsPerSession)
	assert.Len(t, s.Errors, MaxErrorsPerSession)
	assert.Equal(t, "e"+strconv.Itoa(MaxErrorsPerSession+49), s.Errors[len(s.Errors)-1].Message, "the newest are kept")
}

func TestErrorFrame_TextCappedAtParse(t *testing.T) {
	ps := newGateProxy(t)
	c, _ := newGateConn(t, ps, false)
	huge := strings.Repeat("é", 1<<20) // 2MB, multi-byte
	dispatch(c, &wsMessage{Type: "error", URL: "http://localhost:8123/", Data: map[string]interface{}{
		"message": huge, "stack": huge, "source": huge, "error": huge,
	}})
	entries := ps.logger.Query(LogFilter{Types: []LogEntryType{LogTypeError}})
	require.Len(t, entries, 1)
	e := entries[0].Error
	assert.LessOrEqual(t, len(e.Message), maxFrontendErrorFieldBytes)
	assert.LessOrEqual(t, len(e.Source), maxFrontendErrorFieldBytes)
	assert.LessOrEqual(t, len(e.Error), maxFrontendErrorFieldBytes)
	assert.LessOrEqual(t, len(e.Stack), maxFrontendErrorStackBytes)
	assert.True(t, strings.HasSuffix(e.Message, "…"))
	assert.True(t, utf8.ValidString(e.Message) && utf8.ValidString(e.Stack), "a cut never splits a rune")
}
