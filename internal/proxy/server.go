package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/httpcaps"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/store"
)

// ProxyServer is a reverse proxy that logs traffic and injects instrumentation.
type ProxyServer struct {
	ID            string
	TargetURL     *url.URL
	ListenAddr    string
	Path          string
	BindAddress   string // Bind address used (127.0.0.1 or 0.0.0.0)
	AllowExternal bool   // Whether external binding was explicitly allowed
	// StrictListenPort, when true, disables the silent :0 auto-assign
	// fallback in Start(). A bind failure on the requested listen port
	// becomes a hard error instead of being papered over with a random
	// port. Set by callers that hand us an explicit listen port (e.g.
	// autostart with .agnt.kdl `listen-port`).
	StrictListenPort bool
	SkipTLSVerify    bool // Whether TLS cert verification was disabled for the backend transport
	logger           *TrafficLogger
	pageTracker      *PageTracker
	// httpServer is replaced by the runServer auto-restart loop, so it is
	// published atomically; readers (Stop, Serve) must Load() it.
	httpServer atomic.Pointer[http.Server]
	// publicURL holds the optional public URL for tunnel services. Tunnels
	// resolve their URL after the proxy is already serving, so SetPublicURL
	// races request-path readers (checkWSOrigin, URL rewriting, Stats) —
	// hence atomic. Access via GetPublicURL/SetPublicURL only.
	publicURL  atomic.Pointer[string]
	wsUpgrader websocket.Upgrader
	proxy      *httputil.ReverseProxy
	running    atomic.Bool
	startTime  time.Time
	requestSeq atomic.Int64
	mu         sync.Mutex
	cancelFunc context.CancelFunc
	wsConns    sync.Map     // Active WebSocket connections
	lastError  atomic.Value // stores last error (string) if server crashed

	// Ready signal - closed when server is ready to accept connections
	ready     chan struct{}
	readyOnce sync.Once

	// Connection attempt log for the loading page
	connAttempts   []ConnAttempt
	connAttemptsMu sync.Mutex

	// Auto-restart configuration
	autoRestart   bool
	maxRestarts   int
	restartWindow time.Duration
	restarts      []time.Time // timestamps of recent restarts
	restartsMu    sync.Mutex

	// Pending executions for async results
	pendingExecs sync.Map // map[string]chan *ExecutionResult

	// activeFrameID is the content frame the page last reported as active (the
	// always-wrap model — docs/responsive-canonical-target.md §5.3). MCP exec /
	// audits default to it. Empty until a content frame reports in; a single
	// content frame reports on load so this is set in the common case.
	activeFrameID atomic.Pointer[string]

	// boundAddr holds the *live* listen address, updated atomically by the
	// runServer goroutine when an auto-restart rebinds to a different port
	// (the rare stolen-port fallback). The exported ListenAddr field is
	// written once before serving begins and never mutated afterward, so it
	// is race-free for the many cross-package readers; request-path code that
	// needs the current address after a restart calls liveAddr() instead.
	boundAddr atomic.Pointer[string]

	// Base transport for connection pool management (CloseIdleConnections on backend restart)
	baseTransport *http.Transport

	// Overlay notifier for sending events to agent overlay
	overlayNotifier *OverlayNotifier

	// namedCaptures maps screenshot names to file paths for cross-goroutine lookup.
	// Used by proxy exec screenshots: the WebSocket goroutine writes, MCP tool handler reads.
	namedCaptures sync.Map // map[string]string: name → filePath

	// Voice sessions for speech-to-text (map[connID]*VoiceSession)
	voiceSessions sync.Map

	// Tunnel manager for ngrok/cloudflared integration
	tunnel *TunnelManager

	// Chaos engine for failure injection
	chaosEngine *ChaosEngine

	// Backend health probe
	healthCheckInterval time.Duration
	healthFailThreshold int32
	healthFailCount     atomic.Int32
	backendHealthy      atomic.Bool
	lastHealthCheck     atomic.Value // stores time.Time
	healthProbeTimeout  time.Duration

	// Restart backoff (nanoseconds stored as int64 for lock-free access)
	backoffDuration atomic.Int64

	// Session client factory for handling session API requests from browser
	sessionClientFactory SessionClientFactory

	// readiness gates the forwarding path while the proxy is waiting for
	// declared script dependencies to reach Running Healthy (or Running
	// With Errors). When the gate is closed, handleProxy returns a 503
	// with the ReadinessSentinel body instead of forwarding. See
	// readiness.go for the full contract.
	readiness *readinessGate

	// tlsCertSkipped is set when the TLS fallback transport detects a
	// self-signed or otherwise invalid cert and retries without verification.
	// New WebSocket clients receive a warning toast when this flag is set.
	tlsCertSkipped atomic.Bool

	// authBreakout holds the OAuth-breakout rules for this proxy (nil = off).
	// Atomic pointer: the daemon sets it post-create from the project's
	// .agnt.kdl (wireProxyLogger path) while requests are already flowing.
	authBreakout atomic.Pointer[AuthBreakout]

	// secretSink receives secret values submitted from the browser's
	// secret-entry field (ws_handler "secret_submit"). The daemon wires it to
	// the project store at proxy creation. The sink is the ONLY place a
	// submitted secret value flows — it is branched off before any event
	// struct, log entry, or overlay notification is built, and none of those
	// ever carry the value.
	secretSink atomic.Pointer[func(name, value string) error]
}

// SetSecretSink installs the callback that receives browser-submitted
// secret values. See the secretSink field doc for the zero-leak contract.
func (ps *ProxyServer) SetSecretSink(fn func(name, value string) error) {
	ps.secretSink.Store(&fn)
}

// DeliverSecret validates a submitted secret and routes it to the wired
// sink. It never logs, stores, or echoes the value itself; the returned
// error message never contains the value either.
func (ps *ProxyServer) DeliverSecret(name, value string) error {
	if !store.ValidSecretName(name) {
		return fmt.Errorf("invalid secret name %q: must be env-var style ([A-Za-z_][A-Za-z0-9_]*)", name)
	}
	if value == "" {
		return fmt.Errorf("empty secret value")
	}
	sink := ps.secretSink.Load()
	if sink == nil {
		return fmt.Errorf("no secret sink configured for proxy %s", ps.ID)
	}
	return (*sink)(name, value)
}

// ProxyConfig holds configuration for creating a proxy server.
type ProxyConfig struct {
	ID            string
	TargetURL     string
	ListenPort    int
	MaxLogSize    int
	AutoRestart   bool   // Enable automatic restart on crash (default: true)
	Path          string // Working directory where proxy was created
	BindAddress   string // Bind address: "127.0.0.1" (default, localhost only) or "0.0.0.0" (all interfaces)
	PublicURL     string // Optional public URL for tunnel services (e.g., "https://abc123.trycloudflare.com")
	AllowExternal bool   // Allow binding to non-localhost addresses (0.0.0.0, ::). Requires explicit opt-in for security.
	SkipTLSVerify bool   // Skip TLS certificate verification (default: false, verifies certs). Set true for self-signed/expired certs in dev.
	// StrictListenPort disables the silent :0 auto-assign fallback when
	// ListenPort is already in use. When true, a bind conflict surfaces
	// as a hard error from Start(). Used by autostart when a user
	// explicitly declares `listen-port` in .agnt.kdl — an explicit port
	// means "this port or nothing", not "this port or a random one".
	StrictListenPort bool
	Tunnel           *protocol.TunnelConfig

	// HealthCheckInterval controls how often the backend is probed.
	// Zero disables health checks. Default: 30s.
	HealthCheckInterval time.Duration
	// HealthFailThreshold is the number of consecutive failures before
	// marking the backend unhealthy and emitting a diagnostic event.
	// Default: 3.
	HealthFailThreshold int

	// AuthBreakout enables OAuth-flow breakout from the content iframe
	// (nil = off). Populated from the project's .agnt.kdl auth-breakout
	// block; may also be set post-create via SetAuthBreakout.
	AuthBreakout *AuthBreakout
}

// SetAuthBreakout installs (or clears, with nil) the OAuth-breakout rules.
// Safe to call while the proxy is serving.
func (ps *ProxyServer) SetAuthBreakout(ab *AuthBreakout) {
	ps.authBreakout.Store(ab)
}

// AuthBreakoutRules returns the active OAuth-breakout rules, or nil.
func (ps *ProxyServer) AuthBreakoutRules() *AuthBreakout {
	return ps.authBreakout.Load()
}

// isExternalBindAddress returns true if the address would expose the proxy
// beyond localhost (e.g. 0.0.0.0, ::, or any non-loopback IP).
func isExternalBindAddress(addr string) bool {
	switch addr {
	case "", "127.0.0.1", "localhost", "::1":
		return false
	default:
		ip := net.ParseIP(addr)
		if ip == nil {
			return false // unparseable, let net.Listen fail later
		}
		return !ip.IsLoopback()
	}
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
	debug.Log("proxy", "NewProxyServer: id=%s target=%s port=%d", config.ID, config.TargetURL, config.ListenPort)
	targetURL, err := url.Parse(config.TargetURL)
	if err != nil {
		debug.Error("proxy", "invalid target URL %q: %v", config.TargetURL, err)
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

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

	// Reject non-localhost bind addresses unless explicitly allowed
	if isExternalBindAddress(bindAddress) {
		if !config.AllowExternal {
			return nil, fmt.Errorf("binding to %s exposes the proxy to the network; set allow_external: true to confirm", bindAddress)
		}
		debug.Warn("proxy", "Proxy %s binding to %s — accessible from external network", config.ID, bindAddress)
	}

	// Validate public URL if provided
	if config.PublicURL != "" {
		if _, err := url.Parse(config.PublicURL); err != nil {
			return nil, fmt.Errorf("invalid public URL: %w", err)
		}
	}

	logger := NewTrafficLogger(config.MaxLogSize)

	// Health check defaults
	healthInterval := config.HealthCheckInterval
	if healthInterval == 0 {
		healthInterval = 30 * time.Second
	}
	healthThreshold := int32(config.HealthFailThreshold)
	if healthThreshold == 0 {
		healthThreshold = 3
	}

	ps := &ProxyServer{
		ID:                  config.ID,
		TargetURL:           targetURL,
		ListenAddr:          fmt.Sprintf("%s:%d", bindAddress, config.ListenPort),
		Path:                config.Path,
		BindAddress:         bindAddress,
		AllowExternal:       config.AllowExternal,
		StrictListenPort:    config.StrictListenPort && config.ListenPort > 0,
		SkipTLSVerify:       config.SkipTLSVerify,
		logger:              logger,
		pageTracker:         NewPageTracker(100, 5*time.Minute),
		ready:               make(chan struct{}),
		autoRestart:         config.AutoRestart,
		maxRestarts:         5,               // Max 5 restarts
		restartWindow:       1 * time.Minute, // Within 1 minute window
		restarts:            make([]time.Time, 0, 5),
		overlayNotifier:     NewOverlayNotifier(),
		chaosEngine:         NewChaosEngine(logger),
		healthCheckInterval: healthInterval,
		healthFailThreshold: healthThreshold,
		healthProbeTimeout:  5 * time.Second,
		readiness:           newReadinessGate(),
	}

	// Gate WebSocket upgrades to same-origin browser connections. The telemetry
	// channel carries control frames (frame_active retargets exec, etc.), so a
	// foreign page must not be able to open it and hijack the exec target.
	ps.wsUpgrader = websocket.Upgrader{CheckOrigin: ps.checkWSOrigin}

	ps.SetPublicURL(config.PublicURL)

	ps.authBreakout.Store(config.AuthBreakout)

	// Push chaos state to connected browser clients whenever any control
	// surface (MCP, hub, browser panel) mutates the engine.
	ps.chaosEngine.SetOnChange(func() { ps.BroadcastChaosState() })

	// Create reverse proxy with custom Director for proper Host handling
	ps.proxy = httputil.NewSingleHostReverseProxy(targetURL)

	// Configure base transport
	// By default, TLS certificates are verified. SkipTLSVerify opts in to insecure mode.
	baseTransport := ps.proxy.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}

	if config.SkipTLSVerify {
		debug.Warn("proxy", "TLS certificate verification disabled for proxy %s — connections to %s will not verify server certificates", config.ID, config.TargetURL)
		// Explicit skip: clone and disable immediately, no fallback needed.
		if defaultTransport, ok := baseTransport.(*http.Transport); ok {
			clonedTransport := defaultTransport.Clone()
			if clonedTransport.TLSClientConfig == nil {
				clonedTransport.TLSClientConfig = &tls.Config{} //nolint:gosec
			}
			clonedTransport.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec
			baseTransport = clonedTransport
		} else {
			baseTransport = &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			}
		}
		ps.tlsCertSkipped.Store(true)
	}

	// Configure custom dialer to prefer IPv4 for localhost connections.
	// On Windows, "localhost" often resolves to [::1] (IPv6) first, but many
	// development servers only listen on 127.0.0.1 (IPv4), causing connection
	// failures. This ensures we try IPv4 first for localhost/127.0.0.1.
	// Always clone the transport to avoid mutating the shared http.DefaultTransport.
	if transport, ok := baseTransport.(*http.Transport); ok {
		if transport == http.DefaultTransport.(*http.Transport) {
			transport = transport.Clone()
			baseTransport = transport
		}
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

	// Configure transport timeouts for dev server proxying.
	// ResponseHeaderTimeout is 0 (no limit) because upstream APIs like Figma
	// may take 4-5+ seconds to respond. The custom DialContext handles dead
	// backend detection via TCP connect timeout instead.
	if transport, ok := baseTransport.(*http.Transport); ok {
		transport.ResponseHeaderTimeout = 0          // No limit - slow APIs are valid
		transport.IdleConnTimeout = 60 * time.Second // Amortize TLS handshake on HTTPS upstreams (was 10s — too aggressive, forced reconnects during bursts)
		transport.MaxIdleConnsPerHost = 6            // Limit pooled connections per backend
		ps.baseTransport = transport
	}

	// For HTTPS targets without an explicit SkipTLSVerify, wrap the transport
	// in a TLSFallbackTransport that retries cert errors without verification,
	// logs a warning, and sets tlsCertSkipped so new WS clients receive a toast.
	if !config.SkipTLSVerify && targetURL.Scheme == "https" {
		if baseT, ok := baseTransport.(*http.Transport); ok {
			baseTransport = NewTLSFallbackTransport(baseT, func(certErr error) {
				debug.Warn("proxy", "TLS cert error for proxy %s (%s): %v — retrying without verification", config.ID, config.TargetURL, certErr)
				ps.tlsCertSkipped.Store(true)
				// Emit a warning-level diagnostic rather than a level="warn"
				// custom log: diagnostics route through the daemon broadcast
				// gate into the incident pipeline (FromProxyDiagnostic), so the
				// AI agent sees the bypass. A custom-warn is invisible to the
				// agent's error surfaces (only level="error" customs count).
				// Event is deliberately NOT a transport event ("cert_bypass")
				// so it does not trip transport-outage suppression.
				ps.logger.LogDiagnostic(ProxyDiagnostic{
					Timestamp: time.Now(),
					Level:     DiagnosticWarning,
					Category:  "proxy",
					Event:     "cert_bypass",
					Message:   "TLS certificate verification failed and was bypassed. The backend is using a self-signed or invalid certificate. Traffic is still being proxied.",
					Target:    config.TargetURL,
					Data: map[string]any{
						"proxy_id":   config.ID,
						"cert_error": certErr.Error(),
					},
				})
			})
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

		// Filter Accept-Encoding to only include formats we can decompress
		// This prevents the backend from sending unsupported formats
		// that would result in garbled output when we can't decompress them
		if acceptEncoding := req.Header.Get("Accept-Encoding"); acceptEncoding != "" {
			// Parse the Accept-Encoding values
			var supported []string
			for _, encoding := range strings.Split(acceptEncoding, ",") {
				encoding = strings.TrimSpace(strings.ToLower(encoding))
				// Remove quality values (e.g., "gzip;q=1.0" -> "gzip")
				if idx := strings.Index(encoding, ";"); idx != -1 {
					encoding = encoding[:idx]
				}
				// Only include encodings we support
				if encoding == "gzip" || encoding == "deflate" || encoding == "br" || encoding == "zstd" || encoding == "identity" {
					supported = append(supported, encoding)
				}
			}
			if len(supported) > 0 {
				req.Header.Set("Accept-Encoding", strings.Join(supported, ", "))
			} else {
				// If no supported encodings, request identity (uncompressed)
				req.Header.Set("Accept-Encoding", "identity")
			}
		}
	}

	ps.proxy.ErrorHandler = ps.errorHandler
	ps.proxy.ModifyResponse = ps.modifyResponse
	ps.proxy.FlushInterval = -1 // Flush immediately for streaming/WebSocket responses

	// Initialize tunnel manager if configured
	if config.Tunnel != nil && config.Tunnel.Provider != "" {
		ps.tunnel = NewTunnelManager(config.Tunnel, config.ListenPort)
	}

	return ps, nil
}

// Start begins the proxy server.
func (ps *ProxyServer) Start(ctx context.Context) error {
	debug.Log("proxy", "Start: id=%s addr=%s", ps.ID, ps.ListenAddr)
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.running.Load() {
		debug.Log("proxy", "Start: proxy %s already running", ps.ID)
		return fmt.Errorf("proxy server already running")
	}

	ctx, cancel := context.WithCancel(ctx)
	ps.cancelFunc = cancel

	mux := http.NewServeMux()
	mux.HandleFunc("/__devtool_metrics", ps.handleWebSocket)
	mux.HandleFunc("/__devtool_axe", handleAxeCore)
	mux.HandleFunc("/__devtool_html2canvas", handleHtml2Canvas)
	mux.HandleFunc("/__devtool_impeccable", handleImpeccableDetect)
	mux.HandleFunc("/__devtool/", handleInstrumentationAsset)
	mux.HandleFunc("/", ps.handleProxy)

	// Try to bind to requested port first
	listener, err := net.Listen("tcp", ps.ListenAddr)
	if err != nil {
		// If port is in use, try to find an available port — unless
		// the caller pinned an explicit listen port. A strict listen
		// port means "this port or fail"; silently drifting to a
		// random port would defeat the whole purpose of declaring one
		// in .agnt.kdl (CORS origin registration, shareable URLs,
		// hostname→port mapping).
		if isAddressInUse(err) && !ps.StrictListenPort {
			debug.Log("proxy", "address %s in use, trying auto-assign", ps.ListenAddr)
			// Auto-assign a port on the same host (preserves loopback scope).
			listener, err = net.Listen("tcp", ps.autoAssignAddr())
			if err != nil {
				debug.Error("proxy", "failed to find available port: %v", err)
				cancel()
				return fmt.Errorf("failed to find available port: %w", err)
			}
		} else {
			debug.Error("proxy", "failed to listen on %s: %v", ps.ListenAddr, err)
			cancel()
			if ps.StrictListenPort && isAddressInUse(err) {
				return fmt.Errorf("strict listen port %s in use: %w", ps.ListenAddr, err)
			}
			return fmt.Errorf("failed to listen on %s: %w", ps.ListenAddr, err)
		}
	}

	// Update ListenAddr with actual bound address. This is the last write to
	// ListenAddr; the runServer goroutine never touches it again (see boundAddr).
	ps.ListenAddr = listener.Addr().String()
	ps.setBoundAddr(ps.ListenAddr)

	// Potentially-public (a tunnel can expose this proxy), so it is hardened to
	// public standard: header/idle/connection bounds via the shared constructor.
	// It uses the Streaming carve-out because a reverse proxy streams
	// arbitrary-length proxied bodies and hijacks WebSocket upgrades (e.g. Vite
	// HMR) — a whole-request write deadline would sever those. Slowloris and fd
	// exhaustion stay bounded by ReadHeaderTimeout + IdleTimeout + the connection
	// cap below.
	proxyCaps := httpcaps.Streaming()
	ps.httpServer.Store(proxyCaps.Apply(&http.Server{
		Addr:    ps.ListenAddr,
		Handler: mux,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}))

	ps.startTime = time.Now()
	ps.running.Store(true)

	// Start server in goroutine using existing listener, capped to a bounded
	// number of concurrent connections.
	go ps.runServer(ctx, proxyCaps.LimitListener(listener))

	// Start backend health probe (non-zero interval means enabled)
	ps.backendHealthy.Store(true)
	go ps.runHealthCheck(ctx)

	// Start tunnel if configured, pointing it at the ACTUAL bound port — the
	// manager was constructed with the requested port, which is wrong when
	// the bind fell back to an auto-assigned one.
	if ps.tunnel != nil {
		ps.tunnel.SetProxyPort(ps.BoundPort())
		if err := ps.tunnel.Start(ctx); err != nil {
			// Log but don't fail - proxy can work without tunnel
			ps.logger.LogError(FrontendError{
				Message: fmt.Sprintf("failed to start tunnel: %v", err),
				Source:  "tunnel",
			})
		}
	}

	// Emit a lifecycle diagnostic so STREAM-EVENTS consumers (e.g. agnt ssh's
	// port-forward manager, task 07 of the remote-ssh epic) can react to a
	// proxy coming up without polling PROXY LIST on every event. proxy_id and
	// listen_addr live in Data since ProxyDiagnostic has no dedicated fields
	// for them (see cert_bypass diagnostic above for the same pattern).
	ps.logger.LogDiagnostic(ProxyDiagnostic{
		Timestamp: time.Now(),
		Level:     DiagnosticInfo,
		Category:  "proxy",
		Event:     "proxy_started",
		Message:   fmt.Sprintf("proxy %s listening on %s", ps.ID, ps.ListenAddr),
		Target:    ps.ListenAddr,
		Data: map[string]any{
			"proxy_id":    ps.ID,
			"listen_addr": ps.ListenAddr,
		},
	})

	return nil
}

// setBoundAddr publishes the current live listen address atomically.
func (ps *ProxyServer) setBoundAddr(addr string) {
	ps.boundAddr.Store(&addr)
}

// liveAddr returns the current listen address, preferring the atomically
// published value (which tracks auto-restart rebinds) and falling back to the
// write-once ListenAddr field before the first bind completes.
func (ps *ProxyServer) liveAddr() string {
	if p := ps.boundAddr.Load(); p != nil {
		return *p
	}
	return ps.ListenAddr
}

// autoAssignAddr returns the address to hand net.Listen for the port-in-use
// fallback: the *original host* with port 0, so the OS picks a free port while
// the interface scope is preserved. A bare ":0" would bind 0.0.0.0/[::] and
// silently promote a loopback-only dev proxy to a LAN-reachable one — an
// unintended network exposure. Falls back to 127.0.0.1 when the host cannot be
// parsed (never empty, so we never widen the scope by accident).
func (ps *ProxyServer) autoAssignAddr() string {
	host, _, err := net.SplitHostPort(ps.ListenAddr)
	if err != nil || host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, "0")
}

// BoundPort returns the TCP port the proxy is actually listening on, parsed
// from ListenAddr (which Start updates to the real bound address, including
// the OS-assigned port when ListenPort was 0). Returns 0 if the address has no
// parseable port. Callers restarting a proxy use this to rebind the same port
// rather than letting it drift to a fresh auto-assignment.
func (ps *ProxyServer) BoundPort() int {
	_, portStr, err := net.SplitHostPort(ps.liveAddr())
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

// runServer runs the HTTP server with automatic restart on crash
func (ps *ProxyServer) runServer(ctx context.Context, listener net.Listener) {
	// Stop the chaos engine's background goroutines when the server exits.
	// ChaosEngine.reorderQueue uses its own context, so it must be stopped
	// explicitly rather than relying on the proxy's context cancellation.
	defer ps.StopChaos()

	// Signal that server is ready to accept connections
	// This must be done inside runServer to ensure the goroutine has started
	// and the server is about to call Serve(), which is the point where it
	// actually starts accepting connections.
	ps.readyOnce.Do(func() {
		close(ps.ready)
	})

	for {
		serveStart := time.Now()
		err := ps.httpServer.Load().Serve(listener)

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

			// Reset backoff if server ran stably for the stable period
			if time.Since(serveStart) >= stablePeriod {
				ps.resetBackoff()
			}

			// Advance backoff for this restart attempt
			ps.advanceBackoff()

			// Wait for backoff delay before restarting
			delay := ps.restartDelay()
			if delay > 0 {
				debug.Log("proxy", "proxy %s backing off for %v before restart (crash after %v)",
					ps.ID, delay, time.Since(serveStart).Round(time.Millisecond))
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}

			// Record restart
			ps.recordRestart()

			// Try to create new listener on same address. If that port has
			// since been taken (another process grabbed it during the crash
			// window), fall back to an auto-assigned port — unless the caller
			// pinned an explicit StrictListenPort, in which case drifting would
			// defeat the declared port and we abort. Mirrors the initial-bind
			// policy above.
			newListener, restartErr := net.Listen("tcp", ps.ListenAddr)
			if restartErr != nil {
				if isAddressInUse(restartErr) && !ps.StrictListenPort {
					debug.Log("proxy", "proxy %s restart: address %s in use, auto-assigning port", ps.ID, ps.ListenAddr)
					newListener, restartErr = net.Listen("tcp", ps.autoAssignAddr())
				}
				if restartErr != nil {
					ps.lastError.Store(fmt.Sprintf("restart failed: %v (original: %v)", restartErr.Error(), err.Error()))
					return
				}
			}

			// Update listener and server state. The bound address may differ from
			// the requested one after an auto-assign fallback. Publish it through
			// the atomic boundAddr rather than mutating the exported ListenAddr
			// field, which is read unsynchronized across packages.
			newAddr := newListener.Addr().String()
			// Re-cap on restart exactly as at initial bind (same Streaming caps),
			// so an auto-restart never silently drops the bounds.
			restartCaps := httpcaps.Streaming()
			listener = restartCaps.LimitListener(newListener)
			ps.setBoundAddr(newAddr)
			ps.httpServer.Store(restartCaps.Apply(&http.Server{
				Addr:    newAddr,
				Handler: ps.httpServer.Load().Handler,
				BaseContext: func(l net.Listener) context.Context {
					return ctx
				},
			}))
			ps.running.Store(true)

			// Continue loop to restart server
			continue
		}

		// Normal exit
		return
	}
}

// checkWSOrigin decides whether to accept a WebSocket upgrade. The control
// channel is browser-only, so an explicit HTTP(S) Origin is required. Ordinary
// same-origin requests are trusted only when their authority is loopback; this
// prevents a hostname rebound to the local listener from authorizing itself.
// A configured public URL is also trusted because it is an explicit proxy host.
func (ps *ProxyServer) checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) && isLoopbackAuthority(r.Host) {
		return true
	}
	if publicURL := ps.GetPublicURL(); publicURL != "" {
		if pu, perr := url.Parse(publicURL); perr == nil && pu.Host != "" && strings.EqualFold(pu.Host, u.Host) {
			return true
		}
	}
	return false
}

func isLoopbackAuthority(authority string) bool {
	host := authority
	if parsedHost, _, err := net.SplitHostPort(authority); err == nil {
		host = parsedHost
	} else {
		host = strings.Trim(authority, "[]")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 16 * time.Second
	stablePeriod   = 30 * time.Second
)

// advanceBackoff doubles the backoff duration for the next restart attempt.
// Sequence: 1s, 2s, 4s, 8s, 16s (capped).
func (ps *ProxyServer) advanceBackoff() {
	current := time.Duration(ps.backoffDuration.Load())
	if current == 0 {
		ps.backoffDuration.Store(int64(initialBackoff))
		return
	}
	next := current * 2
	if next > maxBackoff {
		next = maxBackoff
	}
	ps.backoffDuration.Store(int64(next))
}

// resetBackoff clears the backoff duration, called after stable operation.
func (ps *ProxyServer) resetBackoff() {
	ps.backoffDuration.Store(0)
}

// StopChaos stops the chaos engine's background goroutines. Called by
// runServer's defer for started servers. Tests that create a ProxyServer
// without calling Start must call this directly in their cleanup.
func (ps *ProxyServer) StopChaos() {
	ps.chaosEngine.Stop()
}

// restartDelay returns the current backoff duration with +-25% jitter.
// Returns 0 if no backoff is active (first restart).
func (ps *ProxyServer) restartDelay() time.Duration {
	base := time.Duration(ps.backoffDuration.Load())
	if base == 0 {
		return 0
	}
	jitter := time.Duration(float64(base) * (0.75 + rand.Float64()*0.5))
	return jitter
}
