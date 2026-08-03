package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/publish"
)

// Stop gracefully stops the proxy server.
func (ps *ProxyServer) Stop(ctx context.Context) error {
	debug.Log("proxy", "Stop: id=%s", ps.ID)
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.running.Load() {
		debug.Log("proxy", "Stop: proxy %s not running", ps.ID)
		// Still release the page-tracker actor: a proxy that failed to start
		// or was already stopped must not leak its owner goroutine.
		ps.pageTracker.Stop()
		// Deterministically reclaim the async onLogEntry worker. A proxy the
		// daemon registered a callback on (SetOnLogEntry) but that never ran —
		// or that is being stopped a second time — must not leak the dispatcher
		// goroutine to the finalizer backstop. Close() is nil-safe/idempotent.
		ps.logger.Close()
		return fmt.Errorf("proxy server not running")
	}

	// Stop tunnel first
	if ps.tunnel != nil {
		ps.tunnel.Stop()
	}

	if ps.cancelFunc != nil {
		ps.cancelFunc()
	}

	var err error
	if srv := ps.httpServer.Load(); srv != nil {
		err = srv.Shutdown(ctx)
	}
	ps.running.Store(false)

	// Close active WebSocket connections and drop them from the registry so a
	// restart on the same port does not inherit stale conns (broadcasts to a
	// dead conn, or a slow drain goroutine outliving the server).
	ps.wsConns.Range(func(key, value any) bool {
		if c, ok := connFromMap(value).(interface{ Close() error }); ok {
			// Best-effort: closing a conn we are tearing down anyway; an error
			// here only means the peer already went away.
			if err := c.Close(); err != nil {
				debug.Log("proxy", "error closing WS conn during shutdown of proxy %s: %v", ps.ID, err)
			}
		}
		ps.wsConns.Delete(key)
		return true
	})

	// Release idle backend connections so the pool does not keep half-open
	// sockets to a backend that may itself be restarting.
	if ps.baseTransport != nil {
		ps.baseTransport.CloseIdleConnections()
	}

	// Stop the page-tracker actor goroutine after the HTTP server has drained
	// (no more TrackHTTPRequest senders).
	ps.pageTracker.Stop()

	// Mirror the proxy_started diagnostic (server.go Start) so STREAM-EVENTS
	// consumers can tear down a forward event-driven rather than only on the
	// next periodic PROXY LIST poll.
	ps.logger.LogDiagnostic(ProxyDiagnostic{
		Timestamp: time.Now(),
		Level:     DiagnosticInfo,
		Category:  "proxy",
		Event:     "proxy_stopped",
		Message:   fmt.Sprintf("proxy %s stopped", ps.ID),
		Target:    ps.ListenAddr,
		Data: map[string]any{
			"proxy_id":    ps.ID,
			"listen_addr": ps.ListenAddr,
		},
	})

	// Deterministically reclaim the async onLogEntry worker goroutine now that
	// the proxy is fully torn down, rather than waiting on the finalizer
	// backstop (which nothing schedules while the daemon's ProxyManager still
	// references this server). Closed AFTER the proxy_stopped diagnostic above
	// so that event is still handed to the dispatcher. Close() is idempotent
	// and a no-op when no callback was ever registered.
	ps.logger.Close()

	return err
}

// IsRunning returns true if the proxy is running.
func (ps *ProxyServer) IsRunning() bool {
	return ps.running.Load()
}

// TunnelURL returns the public tunnel URL if a tunnel is running.
func (ps *ProxyServer) TunnelURL() string {
	if ps.tunnel == nil {
		return ""
	}
	return ps.tunnel.PublicURL()
}

// HasTunnel returns true if a tunnel is configured.
func (ps *ProxyServer) HasTunnel() bool {
	return ps.tunnel != nil
}

// IsTunnelRunning returns true if the tunnel is currently running.
func (ps *ProxyServer) IsTunnelRunning() bool {
	if ps.tunnel == nil {
		return false
	}
	return ps.tunnel.IsRunning()
}

// Logger returns the traffic logger.
func (ps *ProxyServer) Logger() *TrafficLogger {
	return ps.logger
}

// PageTracker returns the page tracker for this proxy server.
func (ps *ProxyServer) PageTracker() *PageTracker {
	return ps.pageTracker
}

// ChaosEngine returns the chaos engine for this proxy server.
func (ps *ProxyServer) ChaosEngine() *ChaosEngine {
	return ps.chaosEngine
}

// Ready returns a channel that is closed when the server is ready to accept connections.
// Use this to wait for server readiness instead of polling or sleeping.
func (ps *ProxyServer) Ready() <-chan struct{} {
	return ps.ready
}

// SetOverlayEndpoint configures the overlay endpoint for forwarding events.
// Example: "http://127.0.0.1:19191"
func (ps *ProxyServer) SetOverlayEndpoint(endpoint string) {
	ps.overlayNotifier.SetEndpoint(endpoint)
}

// HasOverlayEndpoint returns true if this proxy has an overlay endpoint bound.
func (ps *ProxyServer) HasOverlayEndpoint() bool {
	return ps.overlayNotifier.IsEnabled()
}

// SetPublicURL sets the public URL for tunnel services.
// This URL is used for URL rewriting when behind a tunnel.
// Example: "https://abc123.trycloudflare.com"
func (ps *ProxyServer) SetPublicURL(publicURL string) {
	ps.publicURL.Store(&publicURL)
}

// GetPublicURL returns the public URL for tunnel services, or "" if unset.
func (ps *ProxyServer) GetPublicURL() string {
	if p := ps.publicURL.Load(); p != nil {
		return *p
	}
	return ""
}

// SetSessionClientFactory sets the factory for creating session clients.
// This is used by the browser session API to communicate with the daemon.
func (ps *ProxyServer) SetSessionClientFactory(factory SessionClientFactory) {
	ps.sessionClientFactory = factory
}

// OverlayNotifier returns the overlay notifier for direct access.
func (ps *ProxyServer) OverlayNotifier() *OverlayNotifier {
	return ps.overlayNotifier
}

// FlushConnections replaces the transport to kill both idle and in-flight
// connections to the backend. Call this when the backend restarts so requests
// don't hang on half-open TCP connections waiting for the OS timeout.
func (ps *ProxyServer) FlushConnections() {
	if ps.baseTransport == nil {
		return
	}
	// Clone the transport to get a fresh connection pool while preserving
	// all dial/TLS/timeout settings. The old transport's connections (including
	// in-flight ones) will be closed when they next attempt I/O.
	newTransport := ps.baseTransport.Clone()
	ps.baseTransport.CloseIdleConnections()
	ps.baseTransport = newTransport

	// Update the chaos transport wrapper to use the new base transport
	if ct, ok := ps.proxy.Transport.(*ChaosTransport); ok {
		ct.underlying = newTransport
	}
	debug.Log("proxy", "Replaced transport for proxy %s (flushed all connections)", ps.ID)
}

// SetDependencies registers the set of dependency names the proxy
// must wait on before it begins forwarding requests. Passing an empty
// or nil slice opens the gate immediately. See readiness.go for the
// full contract.
func (ps *ProxyServer) SetDependencies(deps []string) {
	if ps.readiness == nil {
		return
	}
	ps.readiness.SetDependencies(deps)
}

// MarkDependencyReady removes dep from the pending set. Returns true
// when the call transitions the gate from closed to open, so the
// caller can fire a one-shot proxy_ready event.
func (ps *ProxyServer) MarkDependencyReady(dep string) (openedNow bool) {
	if ps.readiness == nil {
		return false
	}
	return ps.readiness.MarkDependencyReady(dep)
}

// IsReadyForForwarding returns true when the readiness gate is open
// (no pending dependencies). This is separate from IsRunning, which
// only reports whether the underlying HTTP server is serving
// connections — a running proxy may still be gated.
func (ps *ProxyServer) IsReadyForForwarding() bool {
	if ps.readiness == nil {
		return true
	}
	return ps.readiness.IsReady()
}

// PendingDependencies returns the sorted list of dependencies the
// proxy is still waiting on. Returns an empty slice when the gate is
// open. Callers may modify the returned slice safely.
func (ps *ProxyServer) PendingDependencies() []string {
	if ps.readiness == nil {
		return nil
	}
	return ps.readiness.PendingDependencies()
}

// formatPendingJSON renders a JSON array literal for the pending list.
// Kept separate from encoding/json.Marshal so the logging branch in
// handleProxy can build the body without allocating a map.
func formatPendingJSON(pending []string) string {
	if len(pending) == 0 {
		return "[]"
	}
	out := "["
	for i, p := range pending {
		if i > 0 {
			out += ","
		}
		// Minimal JSON string escape for the characters that can legally
		// appear in a process ID (letters, digits, colon, dash, dot,
		// underscore). Any escape-requiring char is replaced with a
		// placeholder so the JSON stays valid without pulling in
		// encoding/json here.
		out += `"` + escapePendingName(p) + `"`
	}
	out += "]"
	return out
}

// escapePendingName replaces JSON-unsafe characters in a dependency
// name. In practice dependency names are script IDs or process IDs,
// which only contain alphanumerics and `:-._/`; the escape is a
// defensive no-op for the common case.
func escapePendingName(s string) string {
	needsEscape := false
	for _, r := range s {
		if r == '"' || r == '\\' || r < 0x20 {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Stats returns proxy statistics.
func (ps *ProxyServer) Stats() ProxyStats {
	stats := ProxyStats{
		ID:            ps.ID,
		TargetURL:     ps.TargetURL.String(),
		ListenAddr:    ps.ListenAddr,
		Path:          ps.Path,
		BindAddress:   ps.BindAddress,
		PublicURL:     ps.GetPublicURL(),
		Running:       ps.running.Load(),
		Uptime:        time.Since(ps.startTime),
		TotalRequests: ps.requestSeq.Load(),
		LoggerStats:   ps.logger.Stats(),
		AutoRestart:   ps.autoRestart,
	}

	// Include last error if server crashed
	if errVal := ps.lastError.Load(); errVal != nil {
		stats.LastError = errVal.(string)
	}

	// Include restart count from current window
	ps.restartsMu.Lock()
	now := time.Now()
	cutoff := now.Add(-ps.restartWindow)
	restartCount := 0
	for _, t := range ps.restarts {
		if t.After(cutoff) {
			restartCount++
		}
	}
	ps.restartsMu.Unlock()
	stats.RestartCount = restartCount

	// Backend health probe state
	stats.BackendHealthy = ps.backendHealthy.Load()
	stats.ConsecutiveFailures = ps.healthFailCount.Load()
	if t, ok := ps.lastHealthCheck.Load().(time.Time); ok && !t.IsZero() {
		stats.LastHealthCheck = t
	}

	// Restart backoff state
	stats.BackoffDelay = time.Duration(ps.backoffDuration.Load())

	// Readiness gate state — distinguishes "waiting for dependencies"
	// from plain "running" in proxy list / proxy status output. When
	// the proxy has no declared `wait-for`, ReadyForForwarding is
	// always true and WaitingFor is empty.
	if ps.readiness != nil {
		stats.ReadyForForwarding = ps.readiness.IsReady()
		stats.WaitingFor = ps.readiness.PendingDependencies()
	} else {
		stats.ReadyForForwarding = true
	}

	return stats
}

// ProxyStats holds proxy statistics.
type ProxyStats struct {
	ID            string        `json:"id"`
	TargetURL     string        `json:"target_url"`
	ListenAddr    string        `json:"listen_addr"`
	Path          string        `json:"path,omitempty"`         // Working directory where proxy was created
	BindAddress   string        `json:"bind_address,omitempty"` // Bind address (127.0.0.1 or 0.0.0.0)
	PublicURL     string        `json:"public_url,omitempty"`   // Public URL for tunnels
	Running       bool          `json:"running"`
	Uptime        time.Duration `json:"uptime"`
	TotalRequests int64         `json:"total_requests"`
	LoggerStats   LoggerStats   `json:"logger_stats"`
	LastError     string        `json:"last_error,omitempty"` // Set if server crashed
	RestartCount  int           `json:"restart_count"`        // Number of restarts in current window
	AutoRestart   bool          `json:"auto_restart"`         // Whether auto-restart is enabled

	// Backend health probe state
	BackendHealthy      bool      `json:"backend_healthy"`                // false after consecutive probe failures
	LastHealthCheck     time.Time `json:"last_health_check,omitempty"`    // time of last probe
	ConsecutiveFailures int32     `json:"consecutive_failures,omitempty"` // current consecutive failure count

	// Restart backoff
	BackoffDelay time.Duration `json:"backoff_delay,omitempty"` // current backoff base duration (0 = no backoff active)

	// Readiness gate (wait-for dependencies). ReadyForForwarding is
	// false while the proxy is gating on declared script dependencies;
	// WaitingFor holds the sorted list of pending dep names. An empty
	// WaitingFor with ReadyForForwarding=true is the normal running case.
	ReadyForForwarding bool     `json:"ready_for_forwarding"`
	WaitingFor         []string `json:"waiting_for,omitempty"`
}

// handleProxy handles HTTP requests and logs traffic.
func (ps *ProxyServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	seq := ps.requestSeq.Add(1)
	reqID := fmt.Sprintf("req-%d", seq)

	// Readiness gate: if the proxy is still waiting on declared
	// dependencies, reject with a sentinel 503 instead of forwarding.
	// The body marker (`agnt_proxy_not_ready`) lets the incident adapter drop
	// these responses from its error feed during the startup race.
	if ps.readiness != nil && !ps.readiness.IsReady() {
		pending := ps.readiness.PendingDependencies()
		writeReadinessNotReady(w, pending)

		// Log the 503 so proxylog query can still surface it on demand,
		// but leave it flagged as a gate response via the Error field.
		reqHeaders := make(map[string]string)
		for k, v := range r.Header {
			reqHeaders[k] = strings.Join(v, ", ")
		}
		ps.logger.LogHTTP(HTTPLogEntry{
			ID:             reqID,
			Timestamp:      startTime,
			Method:         r.Method,
			URL:            r.URL.String(),
			RequestHeaders: reqHeaders,
			StatusCode:     http.StatusServiceUnavailable,
			ResponseBody:   `{"error":"` + ReadinessSentinel + `","pending":` + formatPendingJSON(pending) + `}`,
			Duration:       time.Since(startTime),
			Error:          ReadinessSentinel,
		})
		return
	}

	// Check if this is a WebSocket upgrade request
	isWebSocket := strings.ToLower(r.Header.Get("Upgrade")) == "websocket" &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")

	if isWebSocket {
		debug.Log("proxy", "WebSocket upgrade detected: %s %s", r.Method, r.URL.Path)
	}

	// Capture request
	reqHeaders := make(map[string]string)
	for k, v := range r.Header {
		reqHeaders[k] = strings.Join(v, ", ")
	}

	// Capture the request body when it is small and its length is known. A
	// chunked/unknown-length or oversized body is deliberately NOT read (reading
	// it would require buffering an unbounded stream — the same OOM vector the
	// response recorder guards against), but the reason is recorded as a marker
	// so the agent can tell "no body" apart from "body present but not captured".
	var reqBody string
	switch {
	case isWebSocket || r.Body == nil || r.ContentLength == 0:
		// No body to capture (or a genuinely empty one) — leave reqBody empty.
	case r.ContentLength < 0:
		reqBody = "[request body not captured: chunked or unknown length]"
	case r.ContentLength >= maxRecordedBody:
		reqBody = fmt.Sprintf("[request body not captured: %d bytes exceeds %dKB cap]",
			r.ContentLength, maxRecordedBody/1024)
	default:
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			// The body is partially consumed; forwarding it with the original
			// Content-Length intact would leave the backend waiting for bytes
			// that never come (hang). Fail the request instead.
			reqBody = fmt.Sprintf("[request body not captured: read error: %v]", err)
			ps.logger.LogHTTP(HTTPLogEntry{
				ID:             reqID,
				Timestamp:      startTime,
				Method:         r.Method,
				URL:            publish.ScrubSharePath(r.URL.String()),
				RequestHeaders: reqHeaders,
				StatusCode:     http.StatusBadRequest,
				Duration:       time.Since(startTime),
				Error:          fmt.Sprintf("request body read error: %v", err),
			})
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		reqBody = string(bodyBytes)
		// Restore body for proxy
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	// For WebSocket upgrades, proxy directly without response recording
	if isWebSocket {
		// Log the upgrade request
		ps.logger.LogHTTP(HTTPLogEntry{
			ID:             reqID,
			Timestamp:      startTime,
			Method:         r.Method,
			URL:            r.URL.String(),
			RequestHeaders: reqHeaders,
			StatusCode:     http.StatusSwitchingProtocols,
			Duration:       0,
		})

		// Proxy the WebSocket upgrade directly
		ps.proxy.ServeHTTP(w, r)
		return
	}

	// Check for chaos rules that apply to this request
	chaosRules := ps.chaosEngine.MatchingRules(r)

	// HTTP error injection - return error without calling backend
	if errorCode, errorMsg := ps.chaosEngine.GetHTTPError(chaosRules); errorCode != 0 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Chaos-Injected", "true")
		w.WriteHeader(errorCode)
		if errorMsg == "" {
			errorMsg = http.StatusText(errorCode)
		}
		w.Write([]byte(errorMsg))

		// Log the chaos-injected error
		ps.logger.LogHTTP(HTTPLogEntry{
			ID:                reqID,
			Timestamp:         startTime,
			Method:            r.Method,
			URL:               r.URL.String(),
			RequestHeaders:    reqHeaders,
			RequestBody:       reqBody,
			StatusCode:        errorCode,
			ResponseBody:      errorMsg,
			Duration:          time.Since(startTime),
			Chaos:             true,
			ChaosSwallowWatch: ps.chaosEngine.SwallowDetectEnabled(),
		})
		return
	}

	// Create response recorder to capture response for non-WebSocket requests
	recorder := &responseRecorder{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		body:           &bytes.Buffer{},
	}

	// Wrap with chaos writers if needed
	var chaosWriter http.ResponseWriter = recorder

	// Slow-drip chaos - stream bytes slowly
	if bytesPerMs, chunkSize := ps.chaosEngine.GetSlowDripConfig(chaosRules); bytesPerMs > 0 {
		chaosWriter = NewSlowDripWriter(chaosWriter, bytesPerMs, chunkSize, r.Context())
	}

	// Connection drop chaos - drop connection mid-response
	if afterPercent, afterBytes := ps.chaosEngine.GetDropConfig(chaosRules); afterPercent > 0 || afterBytes > 0 {
		// We need to estimate content length for percentage-based drops
		// This is a best-effort estimate; actual size may vary
		expectedSize := int64(10 * 1024) // Default 10KB estimate
		chaosWriter = NewConnectionDropWriter(chaosWriter, afterPercent, afterBytes, expectedSize)
	}

	// Truncation chaos - truncate response body
	if truncatePercent := ps.chaosEngine.GetTruncateConfig(chaosRules); truncatePercent > 0 {
		expectedSize := int64(10 * 1024) // Default 10KB estimate
		chaosWriter = NewTruncationWriter(chaosWriter, truncatePercent, expectedSize)
	}

	// Update recorder to use chaos writer for actual writes
	if chaosWriter != recorder {
		recorder.ResponseWriter = chaosWriter
	}

	// Proxy the request
	ps.proxy.ServeHTTP(recorder, r)

	duration := time.Since(startTime)

	// Capture response
	respHeaders := make(map[string]string)
	for k, v := range recorder.Header() {
		respHeaders[k] = strings.Join(v, ", ")
	}

	respBody := recorder.body.String()
	if recorder.truncated {
		respBody += "... [truncated]"
	}

	// Log the HTTP transaction. A public walkthrough share path carries the
	// secret token in the path segment (/s/{token}); scrub it to hash[:8] BEFORE
	// it reaches the traffic log so the token never lands in a persisted URL
	// (INV-9). Non-share paths pass through unchanged.
	httpEntry := HTTPLogEntry{
		ID:              reqID,
		Timestamp:       startTime,
		Method:          r.Method,
		URL:             publish.ScrubSharePath(r.URL.String()),
		RequestHeaders:  reqHeaders,
		RequestBody:     reqBody,
		StatusCode:      recorder.statusCode,
		ResponseHeaders: respHeaders,
		ResponseBody:    respBody,
		Duration:        duration,
	}
	ps.logger.LogHTTP(httpEntry)

	// Track page session
	ps.pageTracker.TrackHTTPRequest(httpEntry)

	// Auto-forward HTTP errors (5xx, 4xx) to overlay for PTY injection. The
	// transaction is already in the traffic log (LogHTTP above), so overlay
	// forwarding is supplementary — surface a failure to the debug log.
	if ps.overlayNotifier.IsEnabled() && httpEntry.StatusCode >= 400 {
		if err := ps.overlayNotifier.NotifyHTTPError(ps.ID, httpEntry); err != nil {
			debug.Log("proxy", "failed to forward HTTP error to overlay: %v", err)
		}
	}
}

// maxRecordedBody caps how many response bytes the recorder buffers for the
// traffic log. The full body is still streamed to the client; only this prefix
// is retained, so a large download or an unbounded SSE stream cannot grow the
// per-request buffer without bound (previously the whole body was buffered and
// truncated only afterwards — an OOM vector).
const maxRecordedBody = 10 * 1024

// responseRecorder captures response data for logging.
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	body        *bytes.Buffer
	wroteHeader bool
	truncated   bool
}

func (rr *responseRecorder) WriteHeader(statusCode int) {
	if !rr.wroteHeader {
		rr.statusCode = statusCode
		rr.wroteHeader = true
		rr.ResponseWriter.WriteHeader(statusCode)
	}
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if !rr.wroteHeader {
		rr.WriteHeader(http.StatusOK)
	}
	// Capture only the first maxRecordedBody bytes for logging; never buffer the
	// whole body. The full payload is streamed to the client unchanged below.
	if remaining := maxRecordedBody - rr.body.Len(); remaining > 0 {
		if len(b) <= remaining {
			rr.body.Write(b)
		} else {
			rr.body.Write(b[:remaining])
			rr.truncated = true
		}
	} else if len(b) > 0 {
		rr.truncated = true
	}
	return rr.ResponseWriter.Write(b)
}

// Hijack implements http.Hijacker for WebSocket support.
func (rr *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rr.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hijacker.Hijack()
}

// Flush implements http.Flusher so httputil.ReverseProxy's FlushInterval = -1
// ("flush after every write") actually reaches the client through the
// recorder — without it the type assertion in the copy loop fails silently
// and SSE/streaming responses are buffered whole.
func (rr *responseRecorder) Flush() {
	if flusher, ok := rr.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
