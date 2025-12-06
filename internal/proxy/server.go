package proxy

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/gorilla/websocket"
)

// ProxyServer is a reverse proxy that logs traffic and injects instrumentation.
type ProxyServer struct {
	ID          string
	TargetURL   *url.URL
	ListenAddr  string
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
	wsConns     sync.Map // Active WebSocket connections
	lastError   atomic.Value // stores last error (string) if server crashed

	// Auto-restart configuration
	autoRestart   bool
	maxRestarts   int
	restartWindow time.Duration
	restarts      []time.Time // timestamps of recent restarts
	restartsMu    sync.Mutex

	// Pending executions for async results
	pendingExecs sync.Map // map[string]chan *ExecutionResult
}

// ProxyConfig holds configuration for creating a proxy server.
type ProxyConfig struct {
	ID          string
	TargetURL   string
	ListenPort  int
	MaxLogSize  int
	AutoRestart bool // Enable automatic restart on crash (default: true)
}

// NewProxyServer creates a new reverse proxy server.
func NewProxyServer(config ProxyConfig) (*ProxyServer, error) {
	targetURL, err := url.Parse(config.TargetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	// Only set default port if not specified (negative values use default, 0 means auto-assign)
	if config.ListenPort < 0 {
		config.ListenPort = 8080
	}

	if config.MaxLogSize <= 0 {
		config.MaxLogSize = 1000
	}

	ps := &ProxyServer{
		ID:            config.ID,
		TargetURL:     targetURL,
		ListenAddr:    fmt.Sprintf(":%d", config.ListenPort),
		logger:        NewTrafficLogger(config.MaxLogSize),
		pageTracker:   NewPageTracker(100, 5*time.Minute),
		autoRestart:   config.AutoRestart,
		maxRestarts:   5,                // Max 5 restarts
		restartWindow: 1 * time.Minute,  // Within 1 minute window
		restarts:      make([]time.Time, 0, 5),
		wsUpgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for development
			},
		},
	}

	// Create reverse proxy
	ps.proxy = httputil.NewSingleHostReverseProxy(targetURL)
	ps.proxy.ErrorHandler = ps.errorHandler
	ps.proxy.ModifyResponse = ps.modifyResponse

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

	// Start server in goroutine using existing listener
	go ps.runServer(ctx, listener)

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

// Logger returns the traffic logger.
func (ps *ProxyServer) Logger() *TrafficLogger {
	return ps.logger
}

// PageTracker returns the page tracker for this proxy server.
func (ps *ProxyServer) PageTracker() *PageTracker {
	return ps.pageTracker
}

// Stats returns proxy statistics.
func (ps *ProxyServer) Stats() ProxyStats {
	stats := ProxyStats{
		ID:            ps.ID,
		TargetURL:     ps.TargetURL.String(),
		ListenAddr:    ps.ListenAddr,
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

	// Create response recorder to capture response for non-WebSocket requests
	recorder := &responseRecorder{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		body:           &bytes.Buffer{},
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

// modifyResponse injects JavaScript into HTML responses.
func (ps *ProxyServer) modifyResponse(resp *http.Response) error {
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

	// Inject instrumentation
	modifiedBody := InjectInstrumentation(bodyBytes, port)

	// Update response with uncompressed modified content
	resp.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	resp.ContentLength = int64(len(modifiedBody))
	resp.Header.Set("Content-Length", strconv.Itoa(len(modifiedBody)))

	// Remove encoding headers since we're returning uncompressed content
	resp.Header.Del("Content-Encoding")

	return nil
}

// errorHandler handles proxy errors.
func (ps *ProxyServer) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	seq := ps.requestSeq.Add(1)
	reqID := fmt.Sprintf("req-%d", seq)

	ps.logger.LogHTTP(HTTPLogEntry{
		ID:         reqID,
		Timestamp:  time.Now(),
		Method:     r.Method,
		URL:        r.URL.String(),
		StatusCode: http.StatusBadGateway,
		Error:      err.Error(),
	})

	// Provide helpful error message based on error type
	var userMsg string
	errStr := err.Error()

	if strings.Contains(errStr, "context canceled") {
		userMsg = fmt.Sprintf("Proxy Error: Request canceled. The proxy may be shutting down, or the target server (%s) is unavailable.", ps.TargetURL.String())
	} else if strings.Contains(errStr, "connection refused") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot connect to target server %s. Make sure the server is running.", ps.TargetURL.String())
	} else if strings.Contains(errStr, "no such host") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot resolve target host %s. Check the target URL.", ps.TargetURL.String())
	} else {
		userMsg = fmt.Sprintf("Proxy Error: %s (target: %s)", errStr, ps.TargetURL.String())
	}

	http.Error(w, userMsg, http.StatusBadGateway)
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

	// Read messages from frontend
	for {
		var msg struct {
			Type string                 `json:"type"`
			Data map[string]interface{} `json:"data"`
			URL  string                 `json:"url"`
		}

		err := conn.ReadJSON(&msg)
		if err != nil {
			break
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
			ps.pageTracker.TrackError(errEntry)

		case "performance":
			metric := PerformanceMetric{
				ID:               id,
				Timestamp:        timestamp,
				URL:              msg.URL,
				NavigationStart:  getInt64Field(msg.Data, "navigation_start"),
				LoadEventEnd:     getInt64Field(msg.Data, "load_event_end"),
				DOMContentLoaded: getInt64Field(msg.Data, "dom_content_loaded"),
				FirstPaint:       getInt64Field(msg.Data, "first_paint"),
				FirstContentfulPaint: getInt64Field(msg.Data, "first_contentful_paint"),
				Custom:           msg.Data,
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
			ps.pageTracker.TrackPerformance(metric)

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
		}
	}
}

// responseRecorder captures response data for logging.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
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
