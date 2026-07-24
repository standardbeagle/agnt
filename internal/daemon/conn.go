package daemon

import (
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"

	goclient "github.com/standardbeagle/go-cli-server/client"
)

// ErrConnectionClosed is returned when operating on a closed connection.
// Re-exported from go-cli-server/client for use within this package.
var ErrConnectionClosed = goclient.ErrConnectionClosed

// Conn provides a shared, reusable client connection to the daemon.
// Create one Conn and share it across all components that need
// to communicate with the daemon.
//
// Conn wraps go-cli-server/client.Conn. Use this type instead of importing
// go-cli-server/client directly to share a single connection across components.
//
// Conn is distinct from Connection (server-side handler in connection.go).
//
// # Request Builder
//
// Conn exposes a fluent request builder instead of per-command methods:
//
//	result, err := conn.Request("PROC", "LIST").WithJSON(filter).JSON()
//	err := conn.Request("PROXY", "STOP", proxyID).OK()
//
// # Thread Safety
//
// Conn is thread-safe. Multiple goroutines can issue requests concurrently.
//
// # Auto-Reconnection
//
// If the connection drops, the next request will automatically reconnect.
type Conn struct {
	conn *goclient.Conn
}

// NewConn creates a new shared daemon connection.
// The connection is not established until the first request or EnsureConnected().
func NewConn(socketPath string) *Conn {
	if socketPath == "" {
		socketPath = protocol.DefaultSocketPath()
	}
	return &Conn{
		conn: goclient.NewConn(
			goclient.WithSocketPath(socketPath),
			goclient.WithTimeout(30*time.Second),
		),
	}
}

// SocketPath returns the configured socket path.
func (c *Conn) SocketPath() string {
	return c.conn.SocketPath()
}

// SetTimeout sets the default timeout for operations.
func (c *Conn) SetTimeout(d time.Duration) {
	c.conn.SetTimeout(d)
}

// EnsureConnected ensures the connection is established.
// If already connected, returns nil immediately.
// If not connected, attempts to connect.
func (c *Conn) EnsureConnected() error {
	return c.conn.EnsureConnected()
}

// IsConnected returns whether the connection is currently established.
func (c *Conn) IsConnected() bool {
	return c.conn.IsConnected()
}

// Close closes the connection permanently.
// After Close, the Conn cannot be reused.
func (c *Conn) Close() error {
	return c.conn.Close()
}

// Disconnect closes the current connection but allows reconnection.
// Use this to release resources temporarily while keeping the Conn usable.
func (c *Conn) Disconnect() error {
	return c.conn.Disconnect()
}

// Request creates a new request builder for the given verb and arguments.
// The verb is the protocol command (e.g., "PROC", "PROXY", "PROXYLOG").
// Additional arguments are appended (e.g., "LIST", "STATUS", process ID).
//
// Example:
//
//	conn.Request("PROC", "LIST")
//	conn.Request("PROC", "STATUS", processID)
//	conn.Request("PROXY", "START", id, targetURL, port)
func (c *Conn) Request(verb string, args ...string) *RequestBuilder {
	return &RequestBuilder{
		builder: c.conn.Request(verb, args...),
	}
}

// Ping sends a ping to the daemon and waits for a pong response.
func (c *Conn) Ping() error {
	return c.conn.Ping()
}

// RequestBuilder builds and executes requests to the daemon.
// Use Conn.Request() to create a RequestBuilder.
type RequestBuilder struct {
	builder *goclient.RequestBuilder
}

// WithArgs appends additional string arguments to the request.
//
//	conn.Request("PROC", "OUTPUT", id).WithArgs("tail=50", "stream=stderr")
func (r *RequestBuilder) WithArgs(args ...string) *RequestBuilder {
	r.builder.WithArgs(args...)
	return r
}

// WithData sets the request payload as raw bytes.
func (r *RequestBuilder) WithData(data []byte) *RequestBuilder {
	r.builder.WithData(data)
	return r
}

// WithJSON marshals the value as JSON and sets it as the request payload.
// If marshaling fails, the error is deferred until execution.
//
//	conn.Request("PROC", "LIST").WithJSON(protocol.DirectoryFilter{Global: true})
func (r *RequestBuilder) WithJSON(v interface{}) *RequestBuilder {
	r.builder.WithJSON(v)
	return r
}

// OK executes the request and returns nil on success.
// Use this for commands that return OK/ERR without data.
//
//	err := conn.Request("PROXY", "STOP", proxyID).OK()
func (r *RequestBuilder) OK() error {
	return r.builder.OK()
}

// JSON executes the request and returns the response as a map.
// Most daemon commands return JSON responses.
//
//	result, err := conn.Request("PROC", "LIST").JSON()
//	processes := result["processes"].([]interface{})
func (r *RequestBuilder) JSON() (map[string]interface{}, error) {
	return r.builder.JSON()
}

// JSONInto executes the request and unmarshals the response into v.
//
//	var info protocol.DaemonInfo
//	err := conn.Request("INFO").JSONInto(&info)
func (r *RequestBuilder) JSONInto(v interface{}) error {
	return r.builder.JSONInto(v)
}

// Bytes executes the request and returns the raw JSON response bytes.
// Use this when you need to handle JSON parsing yourself.
func (r *RequestBuilder) Bytes() ([]byte, error) {
	return r.builder.Bytes()
}

// Chunked executes the request and collects chunked response data.
// Use this for commands that return large data (e.g., process output).
//
//	data, err := conn.Request("PROC", "OUTPUT", id).WithArgs("tail=100").Chunked()
func (r *RequestBuilder) Chunked() ([]byte, error) {
	return r.builder.Chunked()
}

// String executes the request with chunked response and returns as string.
// Convenience wrapper around Chunked() for text output.
//
//	output, err := conn.Request("PROC", "OUTPUT", id).String()
func (r *RequestBuilder) String() (string, error) {
	return r.builder.String()
}
