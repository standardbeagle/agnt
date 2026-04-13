package client

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrReconnecting is returned when an operation is attempted during reconnection.
	ErrReconnecting = errors.New("connection is reconnecting")
	// ErrShutdown is returned when an operation is attempted after shutdown.
	ErrShutdown = errors.New("connection has been shut down")
)

// ReconnectCallback is called after successful reconnection.
// It should restore any state that needs to be re-registered with the hub.
type ReconnectCallback func(conn *Conn) error

// VersionCheckFunc checks version compatibility between client and hub.
// Returns nil to proceed, or an error to fail the connection.
type VersionCheckFunc func(conn *Conn) error

// ResilientConfig configures a ResilientConn.
type ResilientConfig struct {
	// AutoStartConfig for hub auto-start
	AutoStartConfig AutoStartConfig

	// HeartbeatInterval is how often to send heartbeats (0 disables)
	HeartbeatInterval time.Duration

	// HeartbeatTimeout is how long to wait for heartbeat response
	HeartbeatTimeout time.Duration

	// ReconnectBackoffMin is the minimum backoff between reconnection attempts
	ReconnectBackoffMin time.Duration

	// ReconnectBackoffMax is the maximum backoff between reconnection attempts
	ReconnectBackoffMax time.Duration

	// MaxReconnectAttempts limits reconnection attempts (0 = unlimited)
	MaxReconnectAttempts int

	// OnReconnect is called after successful reconnection
	OnReconnect ReconnectCallback

	// OnDisconnect is called when connection is lost
	OnDisconnect func(err error)

	// OnReconnectFailed is called when reconnection fails permanently
	OnReconnectFailed func(err error)

	// VersionCheck is called to verify hub version compatibility.
	// If nil, version checking is skipped.
	VersionCheck VersionCheckFunc
}

// DefaultResilientConfig returns sensible defaults.
func DefaultResilientConfig(socketName string) ResilientConfig {
	return ResilientConfig{
		AutoStartConfig:      DefaultAutoStartConfig(socketName),
		HeartbeatInterval:    10 * time.Second,
		HeartbeatTimeout:     5 * time.Second,
		ReconnectBackoffMin:  100 * time.Millisecond,
		ReconnectBackoffMax:  30 * time.Second,
		MaxReconnectAttempts: 0, // Unlimited
	}
}

// ResilientConn wraps Conn with automatic reconnection and health monitoring.
type ResilientConn struct {
	config ResilientConfig

	conn   *Conn
	connMu sync.RWMutex

	connected    atomic.Bool
	reconnecting atomic.Bool
	shutdown     atomic.Bool

	// Heartbeat management
	heartbeatCancel func()

	// Statistics
	reconnectCount     atomic.Int64
	lastConnectTime    atomic.Pointer[time.Time]
	lastDisconnectTime atomic.Pointer[time.Time]
}

// NewResilientConn creates a new resilient connection.
func NewResilientConn(config ResilientConfig) *ResilientConn {
	return &ResilientConn{
		config: config,
	}
}

// Connect establishes the initial connection to the hub.
func (rc *ResilientConn) Connect() error {
	if rc.shutdown.Load() {
		return ErrShutdown
	}

	rc.connMu.Lock()
	defer rc.connMu.Unlock()

	// Create new connection and connect
	conn, err := EnsureHubRunning(rc.config.AutoStartConfig)
	if err != nil {
		return err
	}

	// Check version compatibility if configured
	if rc.config.VersionCheck != nil {
		if err := rc.config.VersionCheck(conn); err != nil {
			conn.Close()
			return err
		}
	}

	rc.conn = conn
	rc.connected.Store(true)
	now := time.Now()
	rc.lastConnectTime.Store(&now)

	// Start heartbeat monitor
	rc.startHeartbeat()

	return nil
}

// Close shuts down the resilient connection.
func (rc *ResilientConn) Close() error {
	if rc.shutdown.Swap(true) {
		return nil // Already shut down
	}

	// Stop heartbeat
	if rc.heartbeatCancel != nil {
		rc.heartbeatCancel()
	}

	// Close underlying connection
	rc.connMu.Lock()
	defer rc.connMu.Unlock()

	if rc.conn != nil {
		return rc.conn.Close()
	}
	return nil
}

// IsConnected returns whether the connection is currently connected.
func (rc *ResilientConn) IsConnected() bool {
	return rc.connected.Load() && !rc.reconnecting.Load()
}

// IsReconnecting returns whether the connection is currently reconnecting.
func (rc *ResilientConn) IsReconnecting() bool {
	return rc.reconnecting.Load()
}

// Stats returns connection statistics.
func (rc *ResilientConn) Stats() map[string]interface{} {
	stats := map[string]interface{}{
		"connected":       rc.connected.Load(),
		"reconnecting":    rc.reconnecting.Load(),
		"reconnect_count": rc.reconnectCount.Load(),
	}

	if t := rc.lastConnectTime.Load(); t != nil {
		stats["last_connect"] = *t
	}
	if t := rc.lastDisconnectTime.Load(); t != nil {
		stats["last_disconnect"] = *t
	}

	return stats
}

// Conn returns the underlying Conn for direct access.
// Returns nil if not connected.
func (rc *ResilientConn) Conn() *Conn {
	rc.connMu.RLock()
	defer rc.connMu.RUnlock()
	return rc.conn
}

// WithConn executes a function with the connection, handling reconnection.
func (rc *ResilientConn) WithConn(fn func(*Conn) error) error {
	if rc.shutdown.Load() {
		return ErrShutdown
	}

	if rc.reconnecting.Load() {
		return ErrReconnecting
	}

	rc.connMu.RLock()
	conn := rc.conn
	rc.connMu.RUnlock()

	if conn == nil {
		return ErrNotConnected
	}

	err := fn(conn)
	if err != nil {
		// Check if this is a connection error that should trigger reconnection
		if isConnectionError(err) {
			rc.triggerReconnect(err)
		}
	}
	return err
}

// startHeartbeat starts the heartbeat monitoring goroutine.
func (rc *ResilientConn) startHeartbeat() {
	if rc.config.HeartbeatInterval <= 0 {
		return
	}

	// Cancel any existing heartbeat
	if rc.heartbeatCancel != nil {
		rc.heartbeatCancel()
	}

	done := make(chan struct{})
	var once sync.Once
	rc.heartbeatCancel = func() { once.Do(func() { close(done) }) }

	go rc.heartbeatLoop(done)
}

// heartbeatLoop sends periodic heartbeats and detects connection failures.
func (rc *ResilientConn) heartbeatLoop(done <-chan struct{}) {
	ticker := time.NewTicker(rc.config.HeartbeatInterval)
	defer ticker.Stop()

	consecutiveFailures := 0
	maxConsecutiveFailures := 3

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if rc.reconnecting.Load() || rc.shutdown.Load() {
				continue
			}

			err := rc.sendHeartbeat()
			if err != nil {
				consecutiveFailures++
				if consecutiveFailures >= maxConsecutiveFailures {
					rc.triggerReconnect(err)
					consecutiveFailures = 0
				}
			} else {
				consecutiveFailures = 0
			}
		}
	}
}

// sendHeartbeat sends a single heartbeat ping.
func (rc *ResilientConn) sendHeartbeat() error {
	rc.connMu.RLock()
	conn := rc.conn
	rc.connMu.RUnlock()

	if conn == nil {
		return ErrNotConnected
	}

	// Use a timeout for the ping
	done := make(chan error, 1)
	go func() {
		done <- conn.Ping()
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(rc.config.HeartbeatTimeout):
		return errors.New("heartbeat timeout")
	}
}

// triggerReconnect initiates the reconnection process.
func (rc *ResilientConn) triggerReconnect(err error) {
	// Only one reconnection at a time
	if !rc.reconnecting.CompareAndSwap(false, true) {
		return
	}

	rc.connected.Store(false)
	now := time.Now()
	rc.lastDisconnectTime.Store(&now)

	// Notify disconnect callback
	if rc.config.OnDisconnect != nil {
		go rc.config.OnDisconnect(err)
	}

	// Start reconnection in background
	go rc.reconnectLoop()
}

// reconnectLoop attempts to reconnect with exponential backoff.
func (rc *ResilientConn) reconnectLoop() {
	defer rc.reconnecting.Store(false)

	backoff := rc.config.ReconnectBackoffMin
	attempts := 0

	for {
		if rc.shutdown.Load() {
			return
		}

		attempts++

		// Close old connection
		rc.connMu.Lock()
		if rc.conn != nil {
			rc.conn.Close()
			rc.conn = nil
		}
		rc.connMu.Unlock()

		// Attempt to connect
		conn, err := EnsureHubRunning(rc.config.AutoStartConfig)
		if err == nil {
			rc.connMu.Lock()
			rc.conn = conn
			rc.connMu.Unlock()

			rc.connected.Store(true)
			rc.reconnectCount.Add(1)
			now := time.Now()
			rc.lastConnectTime.Store(&now)

			// Call reconnect callback to restore state
			if rc.config.OnReconnect != nil {
				// Ignore callback errors - state restoration is best-effort
				_ = rc.config.OnReconnect(conn)
			}

			// Restart heartbeat
			rc.startHeartbeat()
			return
		}

		// Check if we've exceeded max attempts
		if rc.config.MaxReconnectAttempts > 0 && attempts >= rc.config.MaxReconnectAttempts {
			if rc.config.OnReconnectFailed != nil {
				rc.config.OnReconnectFailed(err)
			}
			return
		}

		// Exponential backoff
		time.Sleep(backoff)
		backoff = minDuration(backoff*2, rc.config.ReconnectBackoffMax)
	}
}

// Ping sends a ping to the hub.
func (rc *ResilientConn) Ping() error {
	return rc.WithConn(func(c *Conn) error {
		return c.Ping()
	})
}

// Request creates a request builder using the resilient connection.
// The request will use the current underlying connection.
func (rc *ResilientConn) Request(verb string, args ...string) (*ResilientRequestBuilder, error) {
	if rc.shutdown.Load() {
		return nil, ErrShutdown
	}
	if rc.reconnecting.Load() {
		return nil, ErrReconnecting
	}

	rc.connMu.RLock()
	conn := rc.conn
	rc.connMu.RUnlock()

	if conn == nil {
		return nil, ErrNotConnected
	}

	return &ResilientRequestBuilder{
		rc:      rc,
		builder: conn.Request(verb, args...),
	}, nil
}

// ResilientRequestBuilder wraps RequestBuilder with connection error handling.
type ResilientRequestBuilder struct {
	rc      *ResilientConn
	builder *RequestBuilder
}

// WithArgs appends additional string arguments to the request.
func (r *ResilientRequestBuilder) WithArgs(args ...string) *ResilientRequestBuilder {
	r.builder.WithArgs(args...)
	return r
}

// WithData sets the request payload as raw bytes.
func (r *ResilientRequestBuilder) WithData(data []byte) *ResilientRequestBuilder {
	r.builder.WithData(data)
	return r
}

// WithJSON marshals the value as JSON and sets it as the request payload.
func (r *ResilientRequestBuilder) WithJSON(v interface{}) *ResilientRequestBuilder {
	r.builder.WithJSON(v)
	return r
}

// OK executes the request and returns nil on success.
func (r *ResilientRequestBuilder) OK() error {
	err := r.builder.OK()
	if err != nil && isConnectionError(err) {
		r.rc.triggerReconnect(err)
	}
	return err
}

// JSON executes the request and returns the response as a map.
func (r *ResilientRequestBuilder) JSON() (map[string]interface{}, error) {
	result, err := r.builder.JSON()
	if err != nil && isConnectionError(err) {
		r.rc.triggerReconnect(err)
	}
	return result, err
}

// JSONInto executes the request and unmarshals the response into v.
func (r *ResilientRequestBuilder) JSONInto(v interface{}) error {
	err := r.builder.JSONInto(v)
	if err != nil && isConnectionError(err) {
		r.rc.triggerReconnect(err)
	}
	return err
}

// Bytes executes the request and returns the raw response bytes.
func (r *ResilientRequestBuilder) Bytes() ([]byte, error) {
	result, err := r.builder.Bytes()
	if err != nil && isConnectionError(err) {
		r.rc.triggerReconnect(err)
	}
	return result, err
}

// Chunked executes the request and collects chunked response data.
func (r *ResilientRequestBuilder) Chunked() ([]byte, error) {
	result, err := r.builder.Chunked()
	if err != nil && isConnectionError(err) {
		r.rc.triggerReconnect(err)
	}
	return result, err
}

// String executes the request with chunked response and returns as string.
func (r *ResilientRequestBuilder) String() (string, error) {
	result, err := r.builder.String()
	if err != nil && isConnectionError(err) {
		r.rc.triggerReconnect(err)
	}
	return result, err
}

// isConnectionError checks if an error indicates a connection problem.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// Check for known connection errors
	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrConnectionClosed) {
		return true
	}

	// Check error message for common connection problems
	errStr := err.Error()
	connectionErrors := []string{
		"connection refused",
		"broken pipe",
		"connection reset",
		"EOF",
		"no such file or directory",
		"socket",
		"network",
	}

	for _, ce := range connectionErrors {
		if containsIgnoreCase(errStr, ce) {
			return true
		}
	}

	return false
}

// containsIgnoreCase checks if s contains substr (case-insensitive).
func containsIgnoreCase(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFoldAt(s, i, substr) {
			return true
		}
	}
	return false
}

func equalFoldAt(s string, i int, substr string) bool {
	for j := 0; j < len(substr); j++ {
		c1 := s[i+j]
		c2 := substr[j]
		if c1 != c2 {
			// Simple ASCII case folding
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 'a' - 'A'
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 'a' - 'A'
			}
			if c1 != c2 {
				return false
			}
		}
	}
	return true
}

// minDuration returns the smaller of two durations.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
