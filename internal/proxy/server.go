package proxy

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/gorilla/websocket"
)

// ProxyServer is a reverse proxy that logs traffic and injects instrumentation.
type ProxyServer struct {
	ID          string
	TargetURL   *url.URL
	ListenAddr  string
	Path        string
	BindAddress string // Bind address used (127.0.0.1 or 0.0.0.0)
	PublicURL   string // Optional public URL for tunnel services
	logger      *TrafficLogger
	pageTracker *PageTracker
	httpServer  *http.Server
	wsUpgrader  websocket.Upgrader
	proxy       *httputil.ReverseProxy
	running     atomic.Bool
	startTime   time.Time
	requestSeq  atomic.Int64
	mu          sync.Mutex
	cancelFunc  context.CancelFunc
	wsConns     sync.Map     // Active WebSocket connections
	lastError   atomic.Value // stores last error (string) if server crashed

	// Ready signal - closed when server is ready to accept connections
	ready     chan struct{}
	readyOnce sync.Once

	// Auto-restart configuration
	autoRestart   bool
	maxRestarts   int
	restartWindow time.Duration
	restarts      []time.Time // timestamps of recent restarts
	restartsMu    sync.Mutex

	// Pending executions for async results
	pendingExecs sync.Map // map[string]chan *ExecutionResult

	// Overlay notifier for sending events to agent overlay
	overlayNotifier *OverlayNotifier

	// Voice sessions for speech-to-text (map[connID]*VoiceSession)
	voiceSessions sync.Map

	// Tunnel manager for ngrok/cloudflared integration
	tunnel *TunnelManager

	// Chaos engine for failure injection
	chaosEngine *ChaosEngine

	// Session client factory for handling session API requests from browser
	sessionClientFactory SessionClientFactory
}

// ProxyConfig holds configuration for creating a proxy server.
type ProxyConfig struct {
	ID          string
	TargetURL   string
	ListenPort  int
	MaxLogSize  int
	AutoRestart bool   // Enable automatic restart on crash (default: true)
	Path        string // Working directory where proxy was created
	BindAddress string // Bind address: "127.0.0.1" (default, localhost only) or "0.0.0.0" (all interfaces)
	PublicURL   string // Optional public URL for tunnel services (e.g., "https://abc123.trycloudflare.com")
	VerifyTLS   bool   // Verify TLS certificates (default: false, accepts self-signed/expired certs for dev)
	Tunnel      *protocol.TunnelConfig
}

// DefaultPortForURL computes a stable default port based on the target URL.
// The port is derived from a hash of the URL, mapped to the range 10000-60000.
// This ensures the same URL always gets the same default port while avoiding
// conflicts with common ports and ephemeral port ranges.
func DefaultPortForURL(targetURL string) int {
	h := fnv.New32a()
	h.Write([]byte(targetURL))
	hash := h.Sum32()

	// Map to range 10000-60000 (50000 ports)
	// This avoids: well-known ports (0-1023), registered ports (1024-9999),
	// and ephemeral port range (typically 32768-60999 on Linux, 49152-65535 on Windows)
	return 10000 + int(hash%50000)
}

// NewProxyServer creates a new reverse proxy server.
func NewProxyServer(config ProxyConfig) (*ProxyServer, error) {
	// Log the incoming URL for debugging HTTPS issues
	log.Printf("[Proxy] Creating proxy with target URL: %s (scheme will be: %s)", config.TargetURL, extractScheme(config.TargetURL))

	targetURL, err := url.Parse(config.TargetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	// Verify scheme was preserved
	log.Printf("[Proxy] Parsed URL scheme: %s, host: %s", targetURL.Scheme, targetURL.Host)

	// Only set default port if not specified (negative values use default, 0 means auto-assign)
	if config.ListenPort < 0 {
		config.ListenPort = DefaultPortForURL(config.TargetURL)
	}

	if config.MaxLogSize <= 0 {
		config.MaxLogSize = 1000
	}

	// Default bind address to localhost for security
	bindAddress := config.BindAddress
	if bindAddress == "" {
		bindAddress = "127.0.0.1"
	}

	// Validate public URL if provided
	if config.PublicURL != "" {
		if _, err := url.Parse(config.PublicURL); err != nil {
			return nil, fmt.Errorf("invalid public URL: %w", err)
		}
	}

	logger := NewTrafficLogger(config.MaxLogSize)
	ps := &ProxyServer{
		ID:              config.ID,
		TargetURL:       targetURL,
		ListenAddr:      fmt.Sprintf("%s:%d", bindAddress, config.ListenPort),
		Path:            config.Path,
		BindAddress:     bindAddress,
		PublicURL:       config.PublicURL,
		logger:          logger,
		pageTracker:     NewPageTracker(100, 5*time.Minute),
		ready:           make(chan struct{}),
		autoRestart:     config.AutoRestart,
		maxRestarts:     5,               // Max 5 restarts
		restartWindow:   1 * time.Minute, // Within 1 minute window
		restarts:        make([]time.Time, 0, 5),
		overlayNotifier: NewOverlayNotifier(),
		chaosEngine:     NewChaosEngine(logger),
		wsUpgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for development
			},
		},
	}

	// Create reverse proxy with custom Director for proper Host handling
	ps.proxy = httputil.NewSingleHostReverseProxy(targetURL)

	// Configure base transport
	// By default, skip TLS verification to support self-signed and expired certs in dev
	baseTransport := ps.proxy.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}

	// If TLS verification is disabled (default), create transport that accepts any cert
	if !config.VerifyTLS {
		// Clone the default transport and disable TLS verification
		if defaultTransport, ok := baseTransport.(*http.Transport); ok {
			clonedTransport := defaultTransport.Clone()
			if clonedTransport.TLSClientConfig == nil {
				clonedTransport.TLSClientConfig = &tls.Config{}
			}
			clonedTransport.TLSClientConfig.InsecureSkipVerify = true
			baseTransport = clonedTransport
		} else {
			// Fallback: create a new transport with TLS skip
			baseTransport = &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			}
		}
	}

	// Configure custom dialer to prefer IPv4 for localhost connections.
	// On Windows, "localhost" often resolves to [::1] (IPv6) first, but many
	// development servers only listen on 127.0.0.1 (IPv4), causing connection
	// failures. This ensures we try IPv4 first for localhost/127.0.0.1.
	if transport, ok := baseTransport.(*http.Transport); ok {
		originalDialContext := transport.DialContext
		if originalDialContext == nil {
			var d net.Dialer
			originalDialContext = d.DialContext
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err == nil && isLocalhost(host) {
				// Try IPv4 first for localhost
				conn, err := originalDialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
				if err == nil {
					return conn, nil
				}
				// Fall back to original behavior if IPv4 fails
			}
			return originalDialContext(ctx, network, addr)
		}
	}

	// Wrap the transport with chaos transport for failure injection
	ps.proxy.Transport = NewChaosTransport(baseTransport, ps.chaosEngine)

	// Customize Director to handle Host header and X-Forwarded-* headers
	originalDirector := ps.proxy.Director
	ps.proxy.Director = func(req *http.Request) {
		// Capture original Host BEFORE director modifies it
		// This is the proxy's host (e.g., localhost:8080)
		originalHost := req.Host

		// Call original director (sets URL, Host to target, etc.)
		originalDirector(req)

		// Ensure Host header matches target (critical for WordPress and other apps)
		req.Host = targetURL.Host

		// Add/update X-Forwarded headers for applications that need them
		// These help apps know the original request came through a proxy
		if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			if prior := req.Header.Get("X-Forwarded-For"); prior != "" {
				req.Header.Set("X-Forwarded-For", prior+", "+clientIP)
			} else {
				req.Header.Set("X-Forwarded-For", clientIP)
			}
		}

		// Set X-Forwarded-Host to the proxy's host (original request host)
		// This tells backend apps the host the client originally connected to
		req.Header.Set("X-Forwarded-Host", originalHost)

		// Set protocol - proxy is HTTP
		req.Header.Set("X-Forwarded-Proto", "http")
	}

	ps.proxy.ErrorHandler = ps.errorHandler
	ps.proxy.ModifyResponse = ps.modifyResponse

	// Initialize tunnel manager if configured
	if config.Tunnel != nil && config.Tunnel.Provider != "" {
		ps.tunnel = NewTunnelManager(config.Tunnel, config.ListenPort)
	}

	return ps, nil
}

// Start begins the proxy server.
func (ps *ProxyServer) Start(ctx context.Context) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.running.Load() {
		return fmt.Errorf("proxy server already running")
	}

	ctx, cancel := context.WithCancel(ctx)
	ps.cancelFunc = cancel

	mux := http.NewServeMux()
	mux.HandleFunc("/__devtool_metrics", ps.handleWebSocket)
	mux.HandleFunc("/", ps.handleProxy)

	// Try to bind to requested port first
	listener, err := net.Listen("tcp", ps.ListenAddr)
	if err != nil {
		// If port is in use, try to find an available port
		if isAddressInUse(err) {
			// Try port 0 to get an auto-assigned port
			listener, err = net.Listen("tcp", ":0")
			if err != nil {
				cancel()
				return fmt.Errorf("failed to find available port: %w", err)
			}
		} else {
			cancel()
			return fmt.Errorf("failed to listen on %s: %w", ps.ListenAddr, err)
		}
	}

	// Update ListenAddr with actual bound address
	ps.ListenAddr = listener.Addr().String()

	ps.httpServer = &http.Server{
		Addr:    ps.ListenAddr,
		Handler: mux,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	ps.startTime = time.Now()
	ps.running.Store(true)

	// Signal that server is ready to accept connections
	// This is safe because the listener is already bound
	ps.readyOnce.Do(func() {
		close(ps.ready)
	})

	// Start server in goroutine using existing listener
	go ps.runServer(ctx, listener)

	// Start tunnel if configured
	if ps.tunnel != nil {
		if err := ps.tunnel.Start(ctx); err != nil {
			// Log but don't fail - proxy can work without tunnel
			ps.logger.LogError(FrontendError{
				Message: fmt.Sprintf("failed to start tunnel: %v", err),
				Source:  "tunnel",
			})
		}
	}

	return nil
}

// runServer runs the HTTP server with automatic restart on crash
func (ps *ProxyServer) runServer(ctx context.Context, listener net.Listener) {
	for {
		err := ps.httpServer.Serve(listener)

		// Normal shutdown, exit
		if err == http.ErrServerClosed || ctx.Err() != nil {
			return
		}

		// Server crashed unexpectedly
		if err != nil {
			ps.running.Store(false)
			ps.lastError.Store(err.Error())

			// Check if auto-restart is enabled
			if !ps.autoRestart {
				return
			}

			// Check restart limits
			if !ps.shouldRestart() {
				ps.lastError.Store(fmt.Sprintf("max restarts exceeded: %v", err.Error()))
				return
			}

			// Record restart
			ps.recordRestart()

			// Try to create new listener on same address
			newListener, restartErr := net.Listen("tcp", ps.ListenAddr)
			if restartErr != nil {
				ps.lastError.Store(fmt.Sprintf("restart failed: %v (original: %v)", restartErr.Error(), err.Error()))
				return
			}

			// Update listener and server state
			listener = newListener
			ps.httpServer = &http.Server{
				Addr:    ps.ListenAddr,
				Handler: ps.httpServer.Handler,
				BaseContext: func(l net.Listener) context.Context {
					return ctx
				},
			}
			ps.running.Store(true)

			// Continue loop to restart server
			continue
		}

		// Normal exit
		return
	}
}

// shouldRestart checks if we should attempt another restart based on rate limits
func (ps *ProxyServer) shouldRestart() bool {
	ps.restartsMu.Lock()
	defer ps.restartsMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-ps.restartWindow)

	// Remove old restarts outside the window
	validRestarts := make([]time.Time, 0, len(ps.restarts))
	for _, t := range ps.restarts {
		if t.After(cutoff) {
			validRestarts = append(validRestarts, t)
		}
	}
	ps.restarts = validRestarts

	// Check if we've hit the limit
	return len(ps.restarts) < ps.maxRestarts
}

// recordRestart records a restart timestamp
func (ps *ProxyServer) recordRestart() {
	ps.restartsMu.Lock()
	defer ps.restartsMu.Unlock()
	ps.restarts = append(ps.restarts, time.Now())
}

// isAddressInUse checks if the error is due to address already in use.
func isAddressInUse(err error) bool {
	if err == nil {
		return false
	}
	// Check for "bind: address already in use" error
	return strings.Contains(err.Error(), "address already in use") ||
		strings.Contains(err.Error(), "bind") && strings.Contains(err.Error(), "in use")
}

// Stop gracefully stops the proxy server.
func (ps *ProxyServer) Stop(ctx context.Context) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.running.Load() {
		return fmt.Errorf("proxy server not running")
	}

	// Stop tunnel first
	if ps.tunnel != nil {
		ps.tunnel.Stop()
	}

	if ps.cancelFunc != nil {
		ps.cancelFunc()
	}

	err := ps.httpServer.Shutdown(ctx)
	ps.running.Store(false)
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

// SetPublicURL sets the public URL for tunnel services.
// This URL is used for URL rewriting when behind a tunnel.
// Example: "https://abc123.trycloudflare.com"
func (ps *ProxyServer) SetPublicURL(publicURL string) {
	ps.PublicURL = publicURL
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

// Stats returns proxy statistics.
func (ps *ProxyServer) Stats() ProxyStats {
	stats := ProxyStats{
		ID:            ps.ID,
		TargetURL:     ps.TargetURL.String(),
		ListenAddr:    ps.ListenAddr,
		Path:          ps.Path,
		BindAddress:   ps.BindAddress,
		PublicURL:     ps.PublicURL,
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
}

// handleProxy handles HTTP requests and logs traffic.
func (ps *ProxyServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	seq := ps.requestSeq.Add(1)
	reqID := fmt.Sprintf("req-%d", seq)

	// Check if this is a WebSocket upgrade request
	isWebSocket := strings.ToLower(r.Header.Get("Upgrade")) == "websocket" &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")

	// Capture request
	reqHeaders := make(map[string]string)
	for k, v := range r.Header {
		reqHeaders[k] = strings.Join(v, ", ")
	}

	var reqBody string
	if !isWebSocket && r.Body != nil && r.ContentLength > 0 && r.ContentLength < 10*1024 { // Limit to 10KB
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			reqBody = string(bodyBytes)
			// Restore body for proxy
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
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
			ID:             reqID,
			Timestamp:      startTime,
			Method:         r.Method,
			URL:            r.URL.String(),
			RequestHeaders: reqHeaders,
			RequestBody:    reqBody,
			StatusCode:     errorCode,
			ResponseBody:   errorMsg,
			Duration:       time.Since(startTime),
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
	if len(respBody) > 10*1024 { // Truncate large responses
		respBody = respBody[:10*1024] + "... [truncated]"
	}

	// Log the HTTP transaction
	httpEntry := HTTPLogEntry{
		ID:              reqID,
		Timestamp:       startTime,
		Method:          r.Method,
		URL:             r.URL.String(),
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
}

// modifyResponse rewrites URLs and injects JavaScript into HTML responses.
func (ps *ProxyServer) modifyResponse(resp *http.Response) error {
	// Rewrite Location header for redirects
	ps.rewriteLocationHeader(resp)

	// Rewrite Set-Cookie headers for domain/path
	ps.rewriteSetCookieHeaders(resp)

	contentType := resp.Header.Get("Content-Type")
	if !ShouldInject(contentType) {
		return nil
	}

	// Check if response is compressed
	encoding := strings.ToLower(resp.Header.Get("Content-Encoding"))
	var bodyReader io.ReadCloser = resp.Body

	// Decompress if needed
	if strings.Contains(encoding, "gzip") {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			// If decompression fails, skip injection and pass through original
			return nil
		}
		defer gzReader.Close()
		bodyReader = gzReader
	} else if strings.Contains(encoding, "deflate") {
		bodyReader = flate.NewReader(resp.Body)
		defer bodyReader.Close()
	}

	// Read decompressed response body
	bodyBytes, err := io.ReadAll(bodyReader)
	if err != nil {
		return err
	}
	resp.Body.Close()

	// Extract port from ListenAddr (handles both :port and [::]:port formats)
	port := 8080
	if lastColon := strings.LastIndex(ps.ListenAddr, ":"); lastColon != -1 {
		if p, err := strconv.Atoi(ps.ListenAddr[lastColon+1:]); err == nil {
			port = p
		}
	}

	// Rewrite absolute URLs in HTML content pointing to target back to proxy
	modifiedBody := ps.rewriteURLsInBody(bodyBytes)

	// Inject instrumentation
	modifiedBody = InjectInstrumentation(modifiedBody, port)

	// Update response with uncompressed modified content
	resp.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	resp.ContentLength = int64(len(modifiedBody))
	resp.Header.Set("Content-Length", strconv.Itoa(len(modifiedBody)))

	// Remove encoding headers since we're returning uncompressed content
	resp.Header.Del("Content-Encoding")

	return nil
}

// rewriteLocationHeader rewrites Location headers to point to the proxy instead of the target.
func (ps *ProxyServer) rewriteLocationHeader(resp *http.Response) {
	location := resp.Header.Get("Location")
	if location == "" {
		return
	}

	rewritten := ps.rewriteURL(location)
	if rewritten != location {
		resp.Header.Set("Location", rewritten)
	}
}

// rewriteSetCookieHeaders rewrites Set-Cookie headers to work with the proxy domain.
func (ps *ProxyServer) rewriteSetCookieHeaders(resp *http.Response) {
	cookies := resp.Header["Set-Cookie"]
	if len(cookies) == 0 {
		return
	}

	targetHost := ps.TargetURL.Hostname()

	for i, cookie := range cookies {
		// Remove or rewrite Domain attribute if it matches target
		// This allows cookies to work on localhost proxy
		if strings.Contains(strings.ToLower(cookie), "domain=") {
			// Parse and rebuild cookie without domain restriction
			// or with proxy domain
			cookies[i] = ps.rewriteCookieDomain(cookie, targetHost)
		}
	}

	resp.Header["Set-Cookie"] = cookies
}

// rewriteCookieDomain removes or rewrites the Domain attribute in a Set-Cookie header.
func (ps *ProxyServer) rewriteCookieDomain(cookie string, targetHost string) string {
	// Split cookie into parts
	parts := strings.Split(cookie, ";")
	var newParts []string

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		lower := strings.ToLower(trimmed)

		// Skip domain attributes that match target host
		if strings.HasPrefix(lower, "domain=") {
			domainValue := strings.TrimPrefix(lower, "domain=")
			domainValue = strings.TrimPrefix(domainValue, ".") // Remove leading dot

			// If domain matches target, remove it entirely (allows cookie on any domain)
			if strings.Contains(targetHost, domainValue) || strings.Contains(domainValue, targetHost) {
				continue
			}
		}

		newParts = append(newParts, part)
	}

	return strings.Join(newParts, ";")
}

// rewriteURL rewrites a URL from the target server to the proxy server.
func (ps *ProxyServer) rewriteURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// Only rewrite absolute URLs that point to the target
	if parsed.Host == "" {
		// Relative URL, no rewriting needed
		return rawURL
	}

	// Check if this URL points to our target
	targetHost := ps.TargetURL.Host
	if parsed.Host != targetHost {
		// Different host, don't rewrite
		return rawURL
	}

	// Rewrite to proxy URL
	// Extract proxy host from ListenAddr
	proxyHost := ps.getProxyHost()
	proxyScheme := ps.getProxyScheme()

	parsed.Scheme = proxyScheme
	parsed.Host = proxyHost

	return parsed.String()
}

// getProxyHost returns the host:port for the proxy server.
// If a public URL is configured (for tunnels), returns that host.
// Otherwise returns localhost:port for local development.
func (ps *ProxyServer) getProxyHost() string {
	// If a public URL is configured (for tunnels), use its host
	if ps.PublicURL != "" {
		if parsed, err := url.Parse(ps.PublicURL); err == nil && parsed.Host != "" {
			return parsed.Host
		}
	}

	// ListenAddr is in format "addr:port" or "[::]:port"
	// We need to return "localhost:port" for redirect purposes
	port := "8080"
	if lastColon := strings.LastIndex(ps.ListenAddr, ":"); lastColon != -1 {
		port = ps.ListenAddr[lastColon+1:]
	}
	return "localhost:" + port
}

// getProxyScheme returns the scheme (http/https) for the proxy server.
// If a public URL is configured with HTTPS (common for tunnels), returns https.
func (ps *ProxyServer) getProxyScheme() string {
	if ps.PublicURL != "" {
		if parsed, err := url.Parse(ps.PublicURL); err == nil && parsed.Scheme != "" {
			return parsed.Scheme
		}
	}
	return "http"
}

// rewriteURLsInBody rewrites absolute URLs in HTML/JS content from target to proxy.
func (ps *ProxyServer) rewriteURLsInBody(body []byte) []byte {
	// Guard against nil TargetURL (can happen in tests with partial setup)
	if ps.TargetURL == nil {
		return body
	}

	targetHost := ps.TargetURL.Host
	if targetHost == "" {
		return body
	}

	proxyHost := ps.getProxyHost()
	proxyScheme := ps.getProxyScheme()

	// Rewrite common URL patterns pointing to target
	// http://target:port -> scheme://proxyhost
	// https://target:port -> scheme://proxyhost

	// Build replacement patterns
	targetHTTP := "http://" + targetHost
	targetHTTPS := "https://" + targetHost
	proxyURL := proxyScheme + "://" + proxyHost

	// Replace URLs (simple byte replacement for performance)
	result := bytes.ReplaceAll(body, []byte(targetHTTPS), []byte(proxyURL))
	result = bytes.ReplaceAll(result, []byte(targetHTTP), []byte(proxyURL))

	// Also handle URLs with escaped slashes (common in JSON)
	targetHTTPEscaped := strings.ReplaceAll(targetHTTP, "/", "\\/")
	targetHTTPSEscaped := strings.ReplaceAll(targetHTTPS, "/", "\\/")
	proxyURLEscaped := strings.ReplaceAll(proxyURL, "/", "\\/")

	result = bytes.ReplaceAll(result, []byte(targetHTTPSEscaped), []byte(proxyURLEscaped))
	result = bytes.ReplaceAll(result, []byte(targetHTTPEscaped), []byte(proxyURLEscaped))

	return result
}

// errorHandler handles proxy errors.
func (ps *ProxyServer) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	seq := ps.requestSeq.Add(1)
	reqID := fmt.Sprintf("req-%d", seq)

	errStr := err.Error()

	// Check if this is a transient connection error (common during development)
	// These happen when dev servers restart, connections timeout, etc.
	isTransient := isTransientConnectionError(errStr)

	ps.logger.LogHTTP(HTTPLogEntry{
		ID:         reqID,
		Timestamp:  time.Now(),
		Method:     r.Method,
		URL:        r.URL.String(),
		StatusCode: http.StatusBadGateway,
		Error:      errStr,
	})

	// Provide helpful error message based on error type
	var userMsg string

	if strings.Contains(errStr, "context canceled") {
		userMsg = fmt.Sprintf("Proxy Error: Request canceled. The proxy may be shutting down, or the target server (%s) is unavailable.", ps.TargetURL.String())
	} else if strings.Contains(errStr, "connection refused") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot connect to target server %s. Make sure the server is running.", ps.TargetURL.String())
	} else if strings.Contains(errStr, "no such host") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot resolve target host %s. Check the target URL.", ps.TargetURL.String())
	} else if isTransient {
		// Friendly message for transient errors - these are normal during development
		userMsg = fmt.Sprintf("Connection to %s was interrupted. This often happens when the dev server restarts. Refresh to retry.", ps.TargetURL.Host)
	} else {
		userMsg = fmt.Sprintf("Proxy Error: %s (target: %s)", errStr, ps.TargetURL.String())
	}

	http.Error(w, userMsg, http.StatusBadGateway)
}

// isTransientConnectionError checks if an error is a transient connection error
// that commonly occurs during development (server restarts, connection resets, etc.)
func isTransientConnectionError(errStr string) bool {
	transientPatterns := []string{
		// Windows-specific connection reset
		"wsarecv: An existing connection was forcibly closed",
		"wsasend: An existing connection was forcibly closed",
		// Unix connection reset
		"connection reset by peer",
		"read: connection reset",
		"write: connection reset",
		// Connection closed
		"EOF",
		"broken pipe",
		"use of closed network connection",
		// Timeout errors
		"i/o timeout",
		"connection timed out",
	}

	errLower := strings.ToLower(errStr)
	for _, pattern := range transientPatterns {
		if strings.Contains(errLower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// handleWebSocket handles WebSocket connections for frontend metrics.
func (ps *ProxyServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := ps.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Store connection for sending messages
	connID := fmt.Sprintf("conn-%d", time.Now().UnixNano())
	ps.wsConns.Store(connID, conn)
	defer ps.wsConns.Delete(connID)

	// Cleanup voice session on disconnect
	defer func() {
		if session, ok := ps.voiceSessions.LoadAndDelete(connID); ok {
			session.(*VoiceSession).Close()
		}
	}()

	// Read messages from frontend
	for {
		messageType, rawMessage, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Handle binary audio data for voice sessions
		if messageType == websocket.BinaryMessage {
			if session, ok := ps.voiceSessions.Load(connID); ok {
				session.(*VoiceSession).SendAudio(rawMessage)
			}
			continue
		}

		// Parse JSON message
		var msg struct {
			Type      string                 `json:"type"`
			Data      map[string]interface{} `json:"data"`
			URL       string                 `json:"url"`
			SessionID string                 `json:"session_id"`
		}

		if err := json.Unmarshal(rawMessage, &msg); err != nil {
			continue
		}

		seq := ps.requestSeq.Add(1)
		id := fmt.Sprintf("metric-%d", seq)
		timestamp := time.Now()

		switch msg.Type {
		case "error":
			errEntry := FrontendError{
				ID:        id,
				Timestamp: timestamp,
				Message:   getStringField(msg.Data, "message"),
				Source:    getStringField(msg.Data, "source"),
				LineNo:    getIntField(msg.Data, "lineno"),
				ColNo:     getIntField(msg.Data, "colno"),
				Error:     getStringField(msg.Data, "error"),
				Stack:     getStringField(msg.Data, "stack"),
				URL:       msg.URL,
			}
			ps.logger.LogError(errEntry)
			ps.pageTracker.TrackError(errEntry, msg.SessionID)

		case "performance":
			metric := PerformanceMetric{
				ID:                   id,
				Timestamp:            timestamp,
				URL:                  msg.URL,
				NavigationStart:      getInt64Field(msg.Data, "navigation_start"),
				LoadEventEnd:         getInt64Field(msg.Data, "load_event_end"),
				DOMContentLoaded:     getInt64Field(msg.Data, "dom_content_loaded"),
				FirstPaint:           getInt64Field(msg.Data, "first_paint"),
				FirstContentfulPaint: getInt64Field(msg.Data, "first_contentful_paint"),
				Custom:               msg.Data,
			}

			// Extract resources if present
			if resourcesData, ok := msg.Data["resources"].([]interface{}); ok {
				for _, r := range resourcesData {
					if rm, ok := r.(map[string]interface{}); ok {
						metric.Resources = append(metric.Resources, ResourceTiming{
							Name:     getStringField(rm, "name"),
							Duration: getInt64Field(rm, "duration"),
							Size:     getInt64Field(rm, "size"),
						})
					}
				}
			}

			ps.logger.LogPerformance(metric)
			ps.pageTracker.TrackPerformance(metric, msg.SessionID)

		case "custom_log":
			ps.logger.LogCustom(CustomLog{
				ID:        id,
				Timestamp: timestamp,
				Level:     getStringField(msg.Data, "level"),
				Message:   getStringField(msg.Data, "message"),
				Data:      msg.Data,
				URL:       msg.URL,
			})

		case "screenshot":
			// Save screenshot to temp file
			dataURL := getStringField(msg.Data, "data")
			name := getStringField(msg.Data, "name")
			if name == "" {
				name = fmt.Sprintf("screenshot-%d", timestamp.Unix())
			}

			filePath, err := ps.saveScreenshot(name, dataURL)
			if err != nil {
				// Log error but continue
				continue
			}

			selector := getStringField(msg.Data, "selector")
			if selector == "" {
				selector = "body"
			}

			ps.logger.LogScreenshot(Screenshot{
				ID:        id,
				Timestamp: timestamp,
				Name:      name,
				FilePath:  filePath,
				URL:       msg.URL,
				Width:     getIntField(msg.Data, "width"),
				Height:    getIntField(msg.Data, "height"),
				Format:    getStringField(msg.Data, "format"),
				Selector:  selector,
			})

		case "execution":
			// Log JavaScript execution result
			execID := getStringField(msg.Data, "exec_id")
			duration := time.Duration(getInt64Field(msg.Data, "duration")) * time.Millisecond

			execResult := ExecutionResult{
				ID:        id,
				Timestamp: timestamp,
				Code:      execID, // Will be filled in by the tool
				Result:    getStringField(msg.Data, "result"),
				Error:     getStringField(msg.Data, "error"),
				Duration:  duration,
				URL:       msg.URL,
				Data:      msg.Data,
			}

			ps.logger.LogExecution(execResult)

			// Send result to waiting channel if one exists
			if ch, ok := ps.pendingExecs.LoadAndDelete(execID); ok {
				resultChan := ch.(chan *ExecutionResult)
				select {
				case resultChan <- &execResult:
					close(resultChan)
				default:
					close(resultChan)
				}
			}

		case "interactions":
			// Handle batched interaction events from frontend
			events := getArrayField(msg.Data, "events")
			for _, eventData := range events {
				if em, ok := eventData.(map[string]interface{}); ok {
					interaction := parseInteractionEvent(em, id, timestamp, msg.URL)
					ps.logger.LogInteraction(interaction)
					ps.pageTracker.TrackInteraction(interaction, msg.SessionID)
				}
			}

		case "mutations":
			// Handle batched mutation events from frontend
			events := getArrayField(msg.Data, "events")
			for _, eventData := range events {
				if em, ok := eventData.(map[string]interface{}); ok {
					mutation := parseMutationEvent(em, id, timestamp, msg.URL)
					ps.logger.LogMutation(mutation)
					ps.pageTracker.TrackMutation(mutation, msg.SessionID)
				}
			}

		case "panel_message":
			// Handle message from floating indicator panel
			panelMsg := parsePanelMessage(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogPanelMessage(panelMsg)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				_ = ps.overlayNotifier.NotifyPanelMessage(ps.ID, &panelMsg)
			}

		case "sketch":
			// Handle sketch/wireframe from sketch mode
			sketchEntry := parseSketchEntry(msg.Data, id, timestamp, msg.URL)

			// Save sketch image to temp file
			if sketchEntry.ImageData != "" {
				filePath, err := ps.saveScreenshot("sketch-"+id, sketchEntry.ImageData)
				if err == nil {
					sketchEntry.FilePath = filePath
				}
			}

			ps.logger.LogSketch(sketchEntry)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				_ = ps.overlayNotifier.NotifySketch(ps.ID, &sketchEntry)
			}

		case "screenshot_capture":
			// Handle area capture from panel with reference ID
			capture := parseScreenshotCapture(msg.Data, timestamp, msg.URL)
			ps.logger.LogScreenshotCapture(capture)

		case "element_capture":
			// Handle element capture from panel with reference ID
			capture := parseElementCapture(msg.Data, timestamp, msg.URL)
			ps.logger.LogElementCapture(capture)

		case "sketch_capture":
			// Handle sketch capture from panel with reference ID
			capture := parseSketchCapture(msg.Data, timestamp, msg.URL)

			// Save sketch image to temp file if present
			if capture.ImageData != "" {
				filePath, err := ps.saveScreenshot("sketch-"+capture.ID, capture.ImageData)
				if err == nil {
					capture.FilePath = filePath
				}
			}

			ps.logger.LogSketchCapture(capture)

		case "design_state":
			// Handle design state when element is selected for iteration
			designState := parseDesignState(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogDesignState(designState)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				_ = ps.overlayNotifier.NotifyDesignState(ps.ID, &designState)
			}

		case "design_request":
			// Handle request for new design alternatives
			designRequest := parseDesignRequest(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogDesignRequest(designRequest)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				_ = ps.overlayNotifier.NotifyDesignRequest(ps.ID, &designRequest)
			}

		case "design_chat":
			// Handle chat message about selected element
			designChat := parseDesignChat(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogDesignChat(designChat)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				_ = ps.overlayNotifier.NotifyDesignChat(ps.ID, &designChat)
			}

		case "session_request":
			// Handle session API requests from browser
			go ps.handleSessionRequest(conn, msg.Data)

		case "voice_start":
			// Start voice transcription session
			config := DefaultDeepgramConfig()

			// Apply any config from message
			if lang := getStringField(msg.Data, "language"); lang != "" {
				config.Language = lang
			}
			if model := getStringField(msg.Data, "model"); model != "" {
				config.Model = model
			}

			session, err := NewVoiceSession(connID, conn, config)
			if err != nil {
				conn.WriteJSON(map[string]interface{}{
					"type":  "voice_error",
					"error": err.Error(),
				})
				continue
			}

			ps.voiceSessions.Store(connID, session)

			// Log voice start
			ps.logger.LogCustom(CustomLog{
				ID:        id,
				Timestamp: timestamp,
				Level:     "info",
				Message:   "[Voice] Transcription session started",
				Data:      map[string]interface{}{"model": config.Model, "language": config.Language},
				URL:       msg.URL,
			})

		case "voice_stop":
			// Stop voice transcription session
			if session, ok := ps.voiceSessions.LoadAndDelete(connID); ok {
				session.(*VoiceSession).Close()

				conn.WriteJSON(map[string]interface{}{
					"type":    "voice_stopped",
					"message": "Transcription session ended",
				})

				// Log voice stop
				ps.logger.LogCustom(CustomLog{
					ID:        id,
					Timestamp: timestamp,
					Level:     "info",
					Message:   "[Voice] Transcription session stopped",
					URL:       msg.URL,
				})
			}
		}
	}
}

// handleSessionRequest processes session API requests from the browser.
// It creates a daemon client, executes the session operation, and sends the response back.
func (ps *ProxyServer) handleSessionRequest(conn *websocket.Conn, data map[string]interface{}) {
	requestID := getStringField(data, "request_id")
	action := getStringField(data, "action")
	params := getMapField(data, "params")

	// Helper to send response
	sendResponse := func(result interface{}, errMsg string) {
		resp := map[string]interface{}{
			"type":       "session_response",
			"request_id": requestID,
		}
		if errMsg != "" {
			resp["error"] = errMsg
		} else {
			resp["result"] = result
		}
		conn.WriteJSON(resp)
	}

	// Check if session client factory is configured
	if ps.sessionClientFactory == nil {
		sendResponse(nil, "session API not available: no session client factory configured")
		return
	}

	// Create session client for this request
	client, err := ps.sessionClientFactory()
	if err != nil {
		sendResponse(nil, fmt.Sprintf("failed to connect to daemon: %v", err))
		return
	}
	defer client.Close()

	// Execute the appropriate session action
	switch action {
	case "list":
		global := getBoolField(params, "global")
		dirFilter := protocol.DirectoryFilter{
			Directory: ps.Path,
			Global:    global,
		}
		result, err := client.SessionList(dirFilter)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "get":
		code := getStringField(params, "code")
		if code == "" {
			sendResponse(nil, "code is required")
			return
		}
		result, err := client.SessionGet(code)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "send":
		code := getStringField(params, "code")
		message := getStringField(params, "message")
		if code == "" {
			sendResponse(nil, "code is required")
			return
		}
		if message == "" {
			sendResponse(nil, "message is required")
			return
		}
		result, err := client.SessionSend(code, message)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "schedule":
		code := getStringField(params, "code")
		duration := getStringField(params, "duration")
		message := getStringField(params, "message")
		if code == "" {
			sendResponse(nil, "code is required")
			return
		}
		if duration == "" {
			sendResponse(nil, "duration is required")
			return
		}
		if message == "" {
			sendResponse(nil, "message is required")
			return
		}
		result, err := client.SessionSchedule(code, duration, message)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "tasks":
		global := getBoolField(params, "global")
		dirFilter := protocol.DirectoryFilter{
			Directory: ps.Path,
			Global:    global,
		}
		result, err := client.SessionTasks(dirFilter)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "cancel":
		taskID := getStringField(params, "task_id")
		if taskID == "" {
			sendResponse(nil, "task_id is required")
			return
		}
		err := client.SessionCancel(taskID)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(map[string]interface{}{"success": true}, "")
		}

	default:
		sendResponse(nil, fmt.Sprintf("unknown session action: %s", action))
	}
}

// responseRecorder captures response data for logging.
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	body        *bytes.Buffer
	wroteHeader bool
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
	rr.body.Write(b) // Capture for logging
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

// Helper functions for extracting fields from JSON data

func getStringField(data map[string]interface{}, key string) string {
	if v, ok := data[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntField(data map[string]interface{}, key string) int {
	if v, ok := data[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return 0
}

func getInt64Field(data map[string]interface{}, key string) int64 {
	if v, ok := data[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		case int:
			return int64(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return i
			}
		}
	}
	return 0
}

func getFloatField(data map[string]interface{}, key string) float64 {
	if v, ok := data[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		case json.Number:
			if f, err := n.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}

// saveScreenshot saves a base64 data URL to a temp file.
func (ps *ProxyServer) saveScreenshot(name string, dataURL string) (string, error) {
	// Parse data URL (format: data:image/png;base64,...)
	if !strings.HasPrefix(dataURL, "data:") {
		return "", fmt.Errorf("invalid data URL")
	}

	// Find base64 data after comma
	commaIdx := strings.Index(dataURL, ",")
	if commaIdx == -1 {
		return "", fmt.Errorf("invalid data URL format")
	}

	// Decode base64 data
	base64Data := dataURL[commaIdx+1:]
	imageData, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	// Create temp file
	tempDir := os.TempDir()
	filename := fmt.Sprintf("%s-%s.png", ps.ID, name)
	filePath := filepath.Join(tempDir, filename)

	// Write to file
	err = os.WriteFile(filePath, imageData, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filePath, nil
}

// ExecuteJavaScript sends JavaScript code to all connected clients for execution.
// Returns the execution ID and a channel that will receive the result.
func (ps *ProxyServer) ExecuteJavaScript(code string) (string, <-chan *ExecutionResult, error) {
	execID := fmt.Sprintf("exec-%d", time.Now().UnixNano())

	// Create result channel for this execution
	resultChan := make(chan *ExecutionResult, 1)
	ps.pendingExecs.Store(execID, resultChan)

	message := map[string]interface{}{
		"type": "execute",
		"id":   execID,
		"code": code,
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		ps.pendingExecs.Delete(execID)
		close(resultChan)
		return "", nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	// Send to all connected clients
	sentCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
		}
		return true
	})

	if sentCount == 0 {
		ps.pendingExecs.Delete(execID)
		close(resultChan)
		return execID, nil, fmt.Errorf("no connected clients")
	}

	return execID, resultChan, nil
}

// BroadcastActivityState sends an activity state update to all connected browser clients.
// Returns the number of clients that received the update.
func (ps *ProxyServer) BroadcastActivityState(active bool) int {
	message := map[string]interface{}{
		"type": "activity",
		"payload": map[string]interface{}{
			"active": active,
		},
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		return 0
	}

	sentCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
		}
		return true
	})

	return sentCount
}

// BroadcastToast sends a toast notification to all connected browser clients.
// Returns the number of clients that received the toast.
func (ps *ProxyServer) BroadcastToast(toastType, title, message string, duration int) (int, error) {
	// Build toast message
	toast := map[string]interface{}{
		"type": "toast",
		"payload": map[string]interface{}{
			"type":    toastType,
			"title":   title,
			"message": message,
		},
	}

	// Only include duration if non-zero
	if duration > 0 {
		toast["payload"].(map[string]interface{})["duration"] = duration
	}

	messageBytes, err := json.Marshal(toast)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal toast: %w", err)
	}

	// Send to all connected clients
	sentCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
		}
		return true
	})

	if sentCount == 0 {
		return 0, fmt.Errorf("no connected clients")
	}

	return sentCount, nil
}

// getArrayField extracts an array from a map field.
func getArrayField(data map[string]interface{}, key string) []interface{} {
	if v, ok := data[key]; ok {
		if arr, ok := v.([]interface{}); ok {
			return arr
		}
	}
	return nil
}

// parseInteractionEvent parses an interaction event from JSON data.
func parseInteractionEvent(data map[string]interface{}, id string, timestamp time.Time, url string) InteractionEvent {
	event := InteractionEvent{
		ID:        id,
		Timestamp: timestamp,
		EventType: getStringField(data, "event_type"),
		URL:       url,
	}

	// Parse target info
	if targetData, ok := data["target"].(map[string]interface{}); ok {
		event.Target = InteractionTarget{
			Selector: getStringField(targetData, "selector"),
			Tag:      getStringField(targetData, "tag"),
			ID:       getStringField(targetData, "id"),
			Text:     getStringField(targetData, "text"),
		}

		// Parse classes
		if classes, ok := targetData["classes"].([]interface{}); ok {
			for _, c := range classes {
				if s, ok := c.(string); ok {
					event.Target.Classes = append(event.Target.Classes, s)
				}
			}
		}

		// Parse attributes
		if attrs, ok := targetData["attributes"].(map[string]interface{}); ok {
			event.Target.Attributes = make(map[string]string)
			for k, v := range attrs {
				if s, ok := v.(string); ok {
					event.Target.Attributes[k] = s
				}
			}
		}
	}

	// Parse position
	if posData, ok := data["position"].(map[string]interface{}); ok {
		event.Position = &InteractionPosition{
			ClientX: getIntField(posData, "client_x"),
			ClientY: getIntField(posData, "client_y"),
			PageX:   getIntField(posData, "page_x"),
			PageY:   getIntField(posData, "page_y"),
		}
	}

	// Parse keyboard info
	if keyData, ok := data["key"].(map[string]interface{}); ok {
		event.Key = &KeyboardInfo{
			Key:   getStringField(keyData, "key"),
			Code:  getStringField(keyData, "code"),
			Ctrl:  getBoolField(keyData, "ctrl"),
			Alt:   getBoolField(keyData, "alt"),
			Shift: getBoolField(keyData, "shift"),
			Meta:  getBoolField(keyData, "meta"),
		}
	}

	// Parse value (for input events)
	event.Value = getStringField(data, "value")

	// Parse extra data
	if extraData, ok := data["data"].(map[string]interface{}); ok {
		event.Data = extraData
	}

	return event
}

// parseMutationEvent parses a mutation event from JSON data.
func parseMutationEvent(data map[string]interface{}, id string, timestamp time.Time, url string) MutationEvent {
	event := MutationEvent{
		ID:           id,
		Timestamp:    timestamp,
		MutationType: getStringField(data, "mutation_type"),
		URL:          url,
	}

	// Parse target info
	if targetData, ok := data["target"].(map[string]interface{}); ok {
		event.Target = MutationTarget{
			Selector: getStringField(targetData, "selector"),
			Tag:      getStringField(targetData, "tag"),
			ID:       getStringField(targetData, "id"),
		}
	}

	// Parse added nodes
	if added, ok := data["added"].([]interface{}); ok {
		for _, nodeData := range added {
			if nm, ok := nodeData.(map[string]interface{}); ok {
				event.Added = append(event.Added, MutationNode{
					Selector: getStringField(nm, "selector"),
					Tag:      getStringField(nm, "tag"),
					ID:       getStringField(nm, "id"),
					HTML:     getStringField(nm, "html"),
				})
			}
		}
	}

	// Parse removed nodes
	if removed, ok := data["removed"].([]interface{}); ok {
		for _, nodeData := range removed {
			if nm, ok := nodeData.(map[string]interface{}); ok {
				event.Removed = append(event.Removed, MutationNode{
					Selector: getStringField(nm, "selector"),
					Tag:      getStringField(nm, "tag"),
					ID:       getStringField(nm, "id"),
					HTML:     getStringField(nm, "html"),
				})
			}
		}
	}

	// Parse attribute change
	if attrData, ok := data["attribute"].(map[string]interface{}); ok {
		event.Attribute = &AttributeChange{
			Name:     getStringField(attrData, "name"),
			OldValue: getStringField(attrData, "old_value"),
			NewValue: getStringField(attrData, "new_value"),
		}
	}

	return event
}

// getBoolField extracts a boolean from a map field.
func getBoolField(data map[string]interface{}, key string) bool {
	if v, ok := data[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// getMapField extracts a map from a map field.
func getMapField(data map[string]interface{}, key string) map[string]interface{} {
	if v, ok := data[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	return nil
}

// isLocalhost checks if a host refers to localhost.
// This includes "localhost", "127.0.0.1", and "::1" (IPv6 loopback).
func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// extractScheme extracts the scheme from a URL string without full parsing.
func extractScheme(rawURL string) string {
	if strings.HasPrefix(rawURL, "https://") {
		return "https"
	} else if strings.HasPrefix(rawURL, "http://") {
		return "http"
	}
	return "unknown"
}

// parsePanelMessage parses a panel message from JSON data.
func parsePanelMessage(data map[string]interface{}, id string, timestamp time.Time, url string) PanelMessage {
	msg := PanelMessage{
		ID:        id,
		Timestamp: timestamp,
		URL:       url,
	}

	// Parse payload
	if payload, ok := data["payload"].(map[string]interface{}); ok {
		msg.Message = getStringField(payload, "message")

		// Parse attachments
		if attachments, ok := payload["attachments"].([]interface{}); ok {
			for _, attData := range attachments {
				if am, ok := attData.(map[string]interface{}); ok {
					att := PanelAttachment{
						Type:     getStringField(am, "type"),
						Selector: getStringField(am, "selector"),
						Tag:      getStringField(am, "tag"),
						ID:       getStringField(am, "id"),
						Text:     getStringField(am, "text"),
					}

					// Parse classes
					if classes, ok := am["classes"].([]interface{}); ok {
						for _, c := range classes {
							if s, ok := c.(string); ok {
								att.Classes = append(att.Classes, s)
							}
						}
					}

					// Parse area (for screenshot_area type)
					if area, ok := am["area"].(map[string]interface{}); ok {
						att.Area = &ScreenshotArea{
							X:      getIntField(area, "x"),
							Y:      getIntField(area, "y"),
							Width:  getIntField(area, "width"),
							Height: getIntField(area, "height"),
							Data:   getStringField(area, "data"),
						}
					}

					msg.Attachments = append(msg.Attachments, att)
				}
			}
		}
	}

	return msg
}

// parseSketchEntry parses a sketch entry from JSON data.
func parseSketchEntry(data map[string]interface{}, id string, timestamp time.Time, url string) SketchEntry {
	entry := SketchEntry{
		ID:           id,
		Timestamp:    timestamp,
		URL:          url,
		Description:  getStringField(data, "description"),
		ElementCount: getIntField(data, "element_count"),
		ImageData:    getStringField(data, "image"),
	}

	// Parse sketch data (store as-is for JSON flexibility)
	if sketchData, ok := data["sketch"].(map[string]interface{}); ok {
		entry.Sketch = sketchData
	}

	return entry
}

// parseScreenshotCapture parses a screenshot capture from panel JSON data.
func parseScreenshotCapture(data map[string]interface{}, timestamp time.Time, url string) ScreenshotCapture {
	capture := ScreenshotCapture{
		ID:        getStringField(data, "id"),
		Timestamp: timestamp,
		URL:       url,
	}

	// Parse nested data field
	if nested, ok := data["data"].(map[string]interface{}); ok {
		capture.Summary = getStringField(nested, "summary")

		// Parse area
		if area, ok := nested["area"].(map[string]interface{}); ok {
			capture.Area.X = getIntField(area, "x")
			capture.Area.Y = getIntField(area, "y")
			capture.Area.Width = getIntField(area, "width")
			capture.Area.Height = getIntField(area, "height")
		}
	}

	return capture
}

// parseElementCapture parses an element capture from panel JSON data.
func parseElementCapture(data map[string]interface{}, timestamp time.Time, url string) ElementCapture {
	capture := ElementCapture{
		ID:        getStringField(data, "id"),
		Timestamp: timestamp,
		URL:       url,
	}

	// Parse nested data field
	if nested, ok := data["data"].(map[string]interface{}); ok {
		capture.Summary = getStringField(nested, "summary")
		capture.Selector = getStringField(nested, "selector")
		capture.Tag = getStringField(nested, "tag")
		capture.ElementID = getStringField(nested, "id")
		capture.Text = getStringField(nested, "text")

		// Parse classes array
		if classes, ok := nested["classes"].([]interface{}); ok {
			for _, c := range classes {
				if s, ok := c.(string); ok {
					capture.Classes = append(capture.Classes, s)
				}
			}
		}

		// Parse rect
		if rect, ok := nested["rect"].(map[string]interface{}); ok {
			capture.Rect.X = getFloatField(rect, "x")
			capture.Rect.Y = getFloatField(rect, "y")
			capture.Rect.Width = getFloatField(rect, "width")
			capture.Rect.Height = getFloatField(rect, "height")
		}
	}

	return capture
}

// parseSketchCapture parses a sketch capture from panel JSON data.
func parseSketchCapture(data map[string]interface{}, timestamp time.Time, url string) SketchCapture {
	capture := SketchCapture{
		ID:        getStringField(data, "id"),
		Timestamp: timestamp,
		URL:       url,
	}

	// Parse nested data field
	if nested, ok := data["data"].(map[string]interface{}); ok {
		capture.ElementCount = getIntField(nested, "elementCount")
		capture.Summary = fmt.Sprintf("Sketch with %d elements", capture.ElementCount)
		capture.ImageData = getStringField(nested, "image")

		// Parse sketch data (store as-is for JSON flexibility)
		if sketchData, ok := nested["sketch"].(map[string]interface{}); ok {
			capture.Sketch = sketchData
		}
	}

	return capture
}

func parseDesignState(data map[string]interface{}, id string, timestamp time.Time, url string) DesignState {
	state := DesignState{
		ID:           id,
		Timestamp:    timestamp,
		URL:          url,
		Selector:     getStringField(data, "selector"),
		XPath:        getStringField(data, "xpath"),
		OriginalHTML: getStringField(data, "originalHTML"),
		ContextHTML:  getStringField(data, "contextHTML"),
	}

	// Parse metadata
	if metaData, ok := data["metadata"].(map[string]interface{}); ok {
		state.Metadata = parseDesignElementMetadata(metaData)
	}

	return state
}

func parseDesignRequest(data map[string]interface{}, id string, timestamp time.Time, url string) DesignRequest {
	request := DesignRequest{
		ID:                id,
		Timestamp:         timestamp,
		URL:               url,
		Selector:          getStringField(data, "selector"),
		XPath:             getStringField(data, "xpath"),
		CurrentHTML:       getStringField(data, "currentHTML"),
		OriginalHTML:      getStringField(data, "originalHTML"),
		ContextHTML:       getStringField(data, "contextHTML"),
		AlternativesCount: getIntField(data, "alternativesCount"),
	}

	// Parse metadata
	if metaData, ok := data["metadata"].(map[string]interface{}); ok {
		request.Metadata = parseDesignElementMetadata(metaData)
	}

	// Parse chat history
	if history, ok := data["chatHistory"].([]interface{}); ok {
		for _, item := range history {
			if msgData, ok := item.(map[string]interface{}); ok {
				request.ChatHistory = append(request.ChatHistory, DesignChatMessage{
					Timestamp: getInt64Field(msgData, "timestamp"),
					Message:   getStringField(msgData, "message"),
					Role:      getStringField(msgData, "role"),
				})
			}
		}
	}

	return request
}

func parseDesignChat(data map[string]interface{}, id string, timestamp time.Time, url string) DesignChat {
	chat := DesignChat{
		ID:           id,
		Timestamp:    timestamp,
		URL:          url,
		Message:      getStringField(data, "message"),
		Selector:     getStringField(data, "selector"),
		XPath:        getStringField(data, "xpath"),
		CurrentHTML:  getStringField(data, "currentHTML"),
		OriginalHTML: getStringField(data, "originalHTML"),
		ContextHTML:  getStringField(data, "contextHTML"),
	}

	// Parse metadata
	if metaData, ok := data["metadata"].(map[string]interface{}); ok {
		chat.Metadata = parseDesignElementMetadata(metaData)
	}

	// Parse chat history
	if history, ok := data["chatHistory"].([]interface{}); ok {
		for _, item := range history {
			if msgData, ok := item.(map[string]interface{}); ok {
				chat.ChatHistory = append(chat.ChatHistory, DesignChatMessage{
					Timestamp: getInt64Field(msgData, "timestamp"),
					Message:   getStringField(msgData, "message"),
					Role:      getStringField(msgData, "role"),
				})
			}
		}
	}

	return chat
}

func parseDesignElementMetadata(data map[string]interface{}) DesignElementMetadata {
	metadata := DesignElementMetadata{
		Tag:  getStringField(data, "tag"),
		ID:   getStringField(data, "id"),
		Text: getStringField(data, "text"),
	}

	// Parse classes array
	if classes, ok := data["classes"].([]interface{}); ok {
		for _, class := range classes {
			if classStr, ok := class.(string); ok {
				metadata.Classes = append(metadata.Classes, classStr)
			}
		}
	}

	// Parse attributes
	if attrs, ok := data["attributes"].(map[string]interface{}); ok {
		metadata.Attributes = make(map[string]string)
		for key, val := range attrs {
			if valStr, ok := val.(string); ok {
				metadata.Attributes[key] = valStr
			}
		}
	}

	// Parse rect
	if rect, ok := data["rect"].(map[string]interface{}); ok {
		metadata.Rect.Width = getIntField(rect, "width")
		metadata.Rect.Height = getIntField(rect, "height")
	}

	return metadata
}
