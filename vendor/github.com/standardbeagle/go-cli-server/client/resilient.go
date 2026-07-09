package client

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/go-cli-server/socket"
)

var (
	// ErrReconnecting is returned when an operation is attempted during reconnection.
	ErrReconnecting = errors.New("connection is reconnecting")
	// ErrShutdown is returned when an operation is attempted after shutdown.
	ErrShutdown = errors.New("connection has been shut down")
	// errHeartbeatTimeout marks a heartbeat that did not return within the
	// timeout. It is distinguished from a transport error so the loop can treat a
	// timeout caused by lock contention (a busy connection) as alive.
	errHeartbeatTimeout = errors.New("heartbeat timeout")
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

func normalizeResilientConfig(config ResilientConfig) ResilientConfig {
	if config.AutoStartConfig.SocketPath == "" {
		config.AutoStartConfig.SocketPath = socket.DefaultSocketPath(socket.DefaultSocketName)
	}
	if config.HeartbeatInterval > 0 && config.HeartbeatTimeout <= 0 {
		config.HeartbeatTimeout = 5 * time.Second
	}
	if config.ReconnectBackoffMin <= 0 {
		config.ReconnectBackoffMin = 100 * time.Millisecond
	}
	if config.ReconnectBackoffMax <= 0 {
		config.ReconnectBackoffMax = 30 * time.Second
	}
	return config
}

// ResilientConn wraps Conn with automatic reconnection and health monitoring.
type ResilientConn struct {
	config ResilientConfig

	conn   *Conn
	connMu sync.RWMutex

	connected       atomic.Bool
	reconnecting    atomic.Bool
	shutdown        atomic.Bool
	reconnectFailed atomic.Bool

	// generation increments every time a new underlying conn is published. A
	// request builder captures the generation it was created against; an error it
	// later reports triggers a reconnect only if it still matches the current
	// generation, so a stale builder holding a since-replaced conn cannot tear
	// down the fresh healthy connection.
	generation atomic.Int64

	// onBackoff, when set, is called with the pending delay just before
	// reconnectLoop waits it out. Nil in production; a test uses it to observe
	// that the loop has actually reached its backoff rather than racing it.
	onBackoff func(time.Duration)

	// closed is closed by Close. reconnectLoop waits on it alongside its backoff
	// timer: a bare sleep would keep the goroutine alive for up to
	// ReconnectBackoffMax after shutdown, outliving the caller that closed us.
	closedOnce sync.Once
	closedCh   chan struct{}

	// Heartbeat management
	hbMu            sync.Mutex
	heartbeatCancel func()

	// Statistics
	reconnectCount     atomic.Int64
	lastConnectTime    atomic.Pointer[time.Time]
	lastDisconnectTime atomic.Pointer[time.Time]
}

// NewResilientConn creates a new resilient connection.
func NewResilientConn(config ResilientConfig) *ResilientConn {
	return &ResilientConn{
		config: normalizeResilientConfig(config),
	}
}

// closed returns the channel closed on shutdown. Created lazily so a zero-value
// ResilientConn still works.
func (rc *ResilientConn) closed() <-chan struct{} {
	rc.closedOnce.Do(func() { rc.closedCh = make(chan struct{}) })
	return rc.closedCh
}

// signalClosed unblocks anything waiting on closed(). Called once, from Close.
func (rc *ResilientConn) signalClosed() {
	rc.closedOnce.Do(func() { rc.closedCh = make(chan struct{}) })
	close(rc.closedCh)
}

// Connect establishes the initial connection to the hub.
func (rc *ResilientConn) Connect() error {
	if rc.shutdown.Load() {
		return ErrShutdown
	}
	rc.reconnectFailed.Store(false)

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

	old := rc.conn
	rc.conn = conn
	rc.generation.Add(1)
	rc.connected.Store(true)
	now := time.Now()
	rc.lastConnectTime.Store(&now)
	if old != nil {
		_ = old.Close()
	}

	// Start heartbeat monitor
	rc.startHeartbeat()

	return nil
}

// Close shuts down the resilient connection.
func (rc *ResilientConn) Close() error {
	if rc.shutdown.Swap(true) {
		return nil // Already shut down
	}

	// Wake a reconnect backoff that would otherwise sleep out its full delay.
	rc.signalClosed()

	// Stop heartbeat
	rc.hbMu.Lock()
	cancel := rc.heartbeatCancel
	rc.hbMu.Unlock()
	if cancel != nil {
		cancel()
	}

	// Close underlying connection
	rc.connMu.Lock()
	defer rc.connMu.Unlock()

	// Clear connected so IsConnected() reports false after Close; leaving it set
	// made a shut-down connection still look live.
	rc.connected.Store(false)

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
	gen := rc.generation.Load()
	rc.connMu.RUnlock()

	if conn == nil {
		return ErrNotConnected
	}

	err := fn(conn)
	if err != nil {
		// Check if this is a connection error that should trigger reconnection
		if isConnectionError(err) {
			rc.triggerReconnectIfCurrent(gen, err)
		}
	}
	return err
}

func (rc *ResilientConn) triggerReconnectIfCurrent(gen int64, err error) {
	if gen != rc.generation.Load() {
		return
	}
	rc.triggerReconnect(err)
}

// startHeartbeat starts the heartbeat monitoring goroutine.
func (rc *ResilientConn) startHeartbeat() {
	if rc.config.HeartbeatInterval <= 0 {
		return
	}

	done := make(chan struct{})
	var once sync.Once

	rc.hbMu.Lock()
	// Cancel any existing heartbeat before replacing it.
	if rc.heartbeatCancel != nil {
		rc.heartbeatCancel()
	}
	rc.heartbeatCancel = func() { once.Do(func() { close(done) }) }
	rc.hbMu.Unlock()

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
			// Exit (not just skip) once shut down: a heartbeat started by a
			// reconnect that raced Close() must terminate itself, else it spins
			// forever.
			if rc.shutdown.Load() || rc.reconnectFailed.Load() {
				return
			}
			if rc.reconnecting.Load() {
				continue
			}

			// A real request in flight demonstrates the connection is alive. A
			// heartbeat PING would only queue behind it on Conn.mu and measure lock
			// contention — a long chunked PROC OUTPUT transfer could hold mu past 3
			// intervals and trip a bogus "heartbeat timeout", tearing down a healthy
			// connection and aborting the in-flight request. Skip the probe instead.
			rc.connMu.RLock()
			conn := rc.conn
			rc.connMu.RUnlock()
			if conn != nil && conn.Busy() {
				consecutiveFailures = 0
				continue
			}

			err := rc.sendHeartbeat()
			if err != nil {
				// If a request grabbed mu after our Busy() check, the PING times out
				// on contention, not death. Treat a busy connection as alive.
				if errors.Is(err, errHeartbeatTimeout) && conn != nil && conn.Busy() {
					consecutiveFailures = 0
					continue
				}
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
		return errHeartbeatTimeout
	}
}

// triggerReconnect initiates the reconnection process.
func (rc *ResilientConn) triggerReconnect(err error) {
	if rc.shutdown.Load() || rc.reconnectFailed.Load() {
		return
	}
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
	maxBackoff := rc.config.ReconnectBackoffMax
	attempts := 0

	// Cancelled when Close signals, so an in-flight connect attempt unwinds.
	connectCtx, cancelConnect := context.WithCancel(context.Background())
	defer cancelConnect()
	go func() {
		select {
		case <-rc.closed():
			cancelConnect()
		case <-connectCtx.Done():
		}
	}()

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

		// Attempt to connect. The attempt itself is abandoned on Close: waiting
		// for a hub that is starting can block for StartTimeout, which would keep
		// this goroutine alive that long past shutdown even though its backoff is
		// interruptible.
		conn, err := EnsureHubRunningContext(connectCtx, rc.config.AutoStartConfig)
		if err == nil {
			// A concurrent Close() may have set shutdown after our loop-top check.
			// Publish the new conn only if still live; otherwise close it and stop,
			// so we don't leak a live conn + heartbeat past shutdown.
			rc.connMu.Lock()
			if rc.shutdown.Load() {
				rc.connMu.Unlock()
				conn.Close()
				return
			}
			rc.conn = conn
			rc.generation.Add(1)
			rc.connMu.Unlock()

			rc.connected.Store(true)
			rc.reconnectFailed.Store(false)
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
			rc.reconnectFailed.Store(true)
			rc.hbMu.Lock()
			if rc.heartbeatCancel != nil {
				rc.heartbeatCancel()
				rc.heartbeatCancel = nil
			}
			rc.hbMu.Unlock()
			if rc.config.OnReconnectFailed != nil {
				rc.config.OnReconnectFailed(err)
			}
			return
		}

		// Exponential backoff, abandoned immediately on Close. Sleeping outright
		// leaks this goroutine for up to maxBackoff past shutdown.
		if rc.onBackoff != nil {
			rc.onBackoff(backoff)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-rc.closed():
			timer.Stop()
			return
		}
		backoff = minDuration(backoff*2, maxBackoff)
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
	gen := rc.generation.Load()
	rc.connMu.RUnlock()

	if conn == nil {
		return nil, ErrNotConnected
	}

	return &ResilientRequestBuilder{
		rc:      rc,
		builder: conn.Request(verb, args...),
		gen:     gen,
	}, nil
}

// ResilientRequestBuilder wraps RequestBuilder with connection error handling.
type ResilientRequestBuilder struct {
	rc      *ResilientConn
	builder *RequestBuilder
	// gen is the connection generation this builder was created against.
	gen int64
}

// triggerReconnect requests a reconnect only if this builder's connection is
// still the current one. A builder holding a since-replaced conn must not tear
// down the fresh connection over an error from the old one.
func (r *ResilientRequestBuilder) triggerReconnect(err error) {
	if r.gen != r.rc.generation.Load() {
		return
	}
	r.rc.triggerReconnect(err)
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
		r.triggerReconnect(err)
	}
	return err
}

// JSON executes the request and returns the response as a map.
func (r *ResilientRequestBuilder) JSON() (map[string]interface{}, error) {
	result, err := r.builder.JSON()
	if err != nil && isConnectionError(err) {
		r.triggerReconnect(err)
	}
	return result, err
}

// JSONInto executes the request and unmarshals the response into v.
func (r *ResilientRequestBuilder) JSONInto(v interface{}) error {
	err := r.builder.JSONInto(v)
	if err != nil && isConnectionError(err) {
		r.triggerReconnect(err)
	}
	return err
}

// Bytes executes the request and returns the raw response bytes.
func (r *ResilientRequestBuilder) Bytes() ([]byte, error) {
	result, err := r.builder.Bytes()
	if err != nil && isConnectionError(err) {
		r.triggerReconnect(err)
	}
	return result, err
}

// Chunked executes the request and collects chunked response data.
func (r *ResilientRequestBuilder) Chunked() ([]byte, error) {
	result, err := r.builder.Chunked()
	if err != nil && isConnectionError(err) {
		r.triggerReconnect(err)
	}
	return result, err
}

// String executes the request with chunked response and returns as string.
func (r *ResilientRequestBuilder) String() (string, error) {
	result, err := r.builder.String()
	if err != nil && isConnectionError(err) {
		r.triggerReconnect(err)
	}
	return result, err
}

// isConnectionError reports whether err is a genuine transport failure that
// warrants tearing down and reconnecting. It matches only on sentinels and
// net.Error — NOT on substrings of the message. An application-level ERR from
// the hub (e.g. "[not_found] no socket registered") is wrapped as ErrServerError
// and must never trigger a reconnect just because its text contains "socket".
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// Application-level protocol errors are not connection failures.
	if errors.Is(err, ErrServerError) {
		return false
	}

	if errors.Is(err, ErrNotConnected) || errors.Is(err, ErrConnectionClosed) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

// minDuration returns the smaller of two durations.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
