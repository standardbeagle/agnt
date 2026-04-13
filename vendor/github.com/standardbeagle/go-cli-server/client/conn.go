// Package client provides a client for communicating with the hub over Unix sockets.
//
// # Shared Connection Design
//
// The Conn type provides a shared, reusable client connection to the hub.
// Instead of each component creating its own client with a socket path,
// a single Conn is created at startup and shared across all consumers.
//
// # Request Builder Pattern
//
// Instead of method-per-command (client.ProcList(), client.ProcStatus(), etc.),
// Conn exposes a fluent Request builder:
//
//	// Single request returning JSON map
//	result, err := conn.Request("PROC", "LIST").
//	    WithJSON(filter).
//	    JSON()
//
//	// Request with inline args
//	output, err := conn.Request("PROC", "OUTPUT", processID).
//	    WithArgs("tail=50", "stream=combined").
//	    String()
//
//	// Request expecting OK/ERR only
//	err := conn.Request("PROC", "STOP", processID).OK()
//
// # Thread Safety
//
// Conn is thread-safe. Multiple goroutines can issue requests concurrently.
// Requests are serialized internally (the protocol is request-response, not pipelined).
//
// # Auto-Reconnection
//
// If the connection drops, the next request will automatically reconnect.
// Use EnsureConnected() to explicitly verify connectivity before issuing requests.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
	"github.com/standardbeagle/go-cli-server/socket"
)

var (
	// ErrNotConnected is returned when trying to use a disconnected client.
	ErrNotConnected = errors.New("not connected to hub")
	// ErrConnectionClosed is returned when operating on a closed connection.
	ErrConnectionClosed = errors.New("connection closed")
	// ErrServerError is returned when the hub returns an error response.
	ErrServerError = errors.New("hub error")
)

// Conn provides a shared, reusable client connection to the hub.
// Create one Conn and share it across all components that need
// to communicate with the hub.
type Conn struct {
	socketPath string
	timeout    time.Duration

	mu     sync.Mutex
	conn   net.Conn
	parser *protocol.Parser
	writer *protocol.Writer
	closed bool
}

// Option configures a Conn.
type Option func(*Conn)

// WithSocketPath sets the socket path for the connection.
func WithSocketPath(path string) Option {
	return func(c *Conn) {
		c.socketPath = path
	}
}

// WithTimeout sets the default timeout for operations.
func WithTimeout(d time.Duration) Option {
	return func(c *Conn) {
		c.timeout = d
	}
}

// NewConn creates a new shared hub connection.
// The connection is not established until the first request or EnsureConnected().
func NewConn(opts ...Option) *Conn {
	c := &Conn{
		socketPath: socket.DefaultSocketPath("mcp-hub"),
		timeout:    30 * time.Second,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SocketPath returns the configured socket path.
func (c *Conn) SocketPath() string {
	return c.socketPath
}

// SetTimeout sets the default timeout for operations.
func (c *Conn) SetTimeout(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timeout = d
}

// EnsureConnected ensures the connection is established.
// If already connected, returns nil immediately.
// If not connected, attempts to connect.
func (c *Conn) EnsureConnected() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureConnectedLocked()
}

// ensureConnectedLocked connects if not already connected. Caller must hold mu.
func (c *Conn) ensureConnectedLocked() error {
	if c.closed {
		return ErrConnectionClosed
	}

	if c.conn != nil {
		return nil // Already connected
	}

	conn, err := socket.Connect(c.socketPath)
	if err != nil {
		return err
	}

	c.conn = conn
	c.parser = protocol.NewParser(conn)
	c.writer = protocol.NewWriter(conn)
	return nil
}

// IsConnected returns whether the connection is currently established.
func (c *Conn) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && !c.closed
}

// Close closes the connection permanently.
// After Close, the Conn cannot be reused.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.parser = nil
		c.writer = nil
		return err
	}
	return nil
}

// Disconnect closes the current connection but allows reconnection.
// Use this to release resources temporarily while keeping the Conn usable.
func (c *Conn) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}

	err := c.conn.Close()
	c.conn = nil
	c.parser = nil
	c.writer = nil
	return err
}

// Request creates a new request builder for the given verb and arguments.
// The verb is the protocol command (e.g., "PROC", "RELAY", "SESSION").
// Additional arguments are appended (e.g., "LIST", "STATUS", process ID).
//
// Example:
//
//	conn.Request("PROC", "LIST")
//	conn.Request("PROC", "STATUS", processID)
//	conn.Request("RELAY", "SEND", targetID)
func (c *Conn) Request(verb string, args ...string) *RequestBuilder {
	return &RequestBuilder{
		conn: c,
		verb: verb,
		args: args,
	}
}

// Ping sends a ping to the hub and waits for a pong response.
func (c *Conn) Ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnectedLocked(); err != nil {
		return err
	}

	if err := c.writer.WriteCommand(protocol.VerbPing, nil, nil); err != nil {
		c.handleErrorLocked()
		return fmt.Errorf("failed to send ping: %w", err)
	}

	resp, err := c.parser.ParseResponse()
	if err != nil {
		c.handleErrorLocked()
		return fmt.Errorf("failed to read pong: %w", err)
	}

	if resp.Type != protocol.ResponsePong {
		return fmt.Errorf("expected PONG, got %s", resp.Type)
	}

	return nil
}

// handleErrorLocked handles a connection error by closing the connection.
// Caller must hold mu.
func (c *Conn) handleErrorLocked() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.parser = nil
		c.writer = nil
	}
}

// execute runs the request and returns the raw response.
func (c *Conn) execute(verb string, args []string, data []byte) (*protocol.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnectedLocked(); err != nil {
		return nil, err
	}

	if err := c.writer.WriteCommandWithSubVerb(verb, "", args, data); err != nil {
		c.handleErrorLocked()
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	resp, err := c.parser.ParseResponse()
	if err != nil {
		c.handleErrorLocked()
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return resp, nil
}

// executeChunked runs the request and collects chunked response data.
func (c *Conn) executeChunked(verb string, args []string, data []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnectedLocked(); err != nil {
		return nil, err
	}

	if err := c.writer.WriteCommandWithSubVerb(verb, "", args, data); err != nil {
		c.handleErrorLocked()
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	var result []byte
	for {
		resp, err := c.parser.ParseResponse()
		if err != nil {
			if err == io.EOF {
				break
			}
			c.handleErrorLocked()
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		switch resp.Type {
		case protocol.ResponseChunk:
			result = append(result, resp.Data...)
		case protocol.ResponseEnd:
			return result, nil
		case protocol.ResponseErr:
			return nil, fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
		default:
			return nil, fmt.Errorf("unexpected response type: %s", resp.Type)
		}
	}

	return result, nil
}

// RequestBuilder builds and executes requests to the hub.
// Use Conn.Request() to create a RequestBuilder.
type RequestBuilder struct {
	conn *Conn
	verb string
	args []string
	data []byte
}

// WithArgs appends additional string arguments to the request.
//
//	conn.Request("PROC", "OUTPUT", id).WithArgs("tail=50", "stream=stderr")
func (r *RequestBuilder) WithArgs(args ...string) *RequestBuilder {
	r.args = append(r.args, args...)
	return r
}

// WithData sets the request payload as raw bytes.
func (r *RequestBuilder) WithData(data []byte) *RequestBuilder {
	r.data = data
	return r
}

// WithJSON marshals the value as JSON and sets it as the request payload.
// If marshaling fails, the error is deferred until execution.
//
//	conn.Request("PROC", "LIST").WithJSON(filter)
func (r *RequestBuilder) WithJSON(v interface{}) *RequestBuilder {
	data, err := json.Marshal(v)
	if err != nil {
		// Store nil to signal error - execute will fail
		r.data = nil
		return r
	}
	r.data = data
	return r
}

// OK executes the request and returns nil on success.
// Use this for commands that return OK/ERR without data.
//
//	err := conn.Request("PROC", "STOP", processID).OK()
func (r *RequestBuilder) OK() error {
	resp, err := r.conn.execute(r.verb, r.args, r.data)
	if err != nil {
		return err
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	return nil
}

// JSON executes the request and returns the response as a map.
// Most hub commands return JSON responses.
//
//	result, err := conn.Request("PROC", "LIST").JSON()
//	processes := result["processes"].([]interface{})
func (r *RequestBuilder) JSON() (map[string]interface{}, error) {
	resp, err := r.conn.execute(r.verb, r.args, r.data)
	if err != nil {
		return nil, err
	}

	if resp.Type == protocol.ResponseErr {
		return nil, fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	if resp.Type != protocol.ResponseJSON {
		return nil, fmt.Errorf("expected JSON response, got %s", resp.Type)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return result, nil
}

// JSONInto executes the request and unmarshals the response into v.
//
//	var info HubInfo
//	err := conn.Request("INFO").JSONInto(&info)
func (r *RequestBuilder) JSONInto(v interface{}) error {
	resp, err := r.conn.execute(r.verb, r.args, r.data)
	if err != nil {
		return err
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	if resp.Type != protocol.ResponseJSON {
		return fmt.Errorf("expected JSON response, got %s", resp.Type)
	}

	if err := json.Unmarshal(resp.Data, v); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return nil
}

// Bytes executes the request and returns the raw JSON response bytes.
// Use this when you need to handle JSON parsing yourself.
func (r *RequestBuilder) Bytes() ([]byte, error) {
	resp, err := r.conn.execute(r.verb, r.args, r.data)
	if err != nil {
		return nil, err
	}

	if resp.Type == protocol.ResponseErr {
		return nil, fmt.Errorf("%w: [%s] %s", ErrServerError, resp.Code, resp.Message)
	}

	return resp.Data, nil
}

// Chunked executes the request and collects chunked response data.
// Use this for commands that return large data (e.g., process output).
//
//	data, err := conn.Request("PROC", "OUTPUT", id).WithArgs("tail=100").Chunked()
func (r *RequestBuilder) Chunked() ([]byte, error) {
	return r.conn.executeChunked(r.verb, r.args, r.data)
}

// String executes the request with chunked response and returns as string.
// Convenience wrapper around Chunked() for text output.
//
//	output, err := conn.Request("PROC", "OUTPUT", id).String()
func (r *RequestBuilder) String() (string, error) {
	data, err := r.Chunked()
	if err != nil {
		return "", err
	}
	return string(data), nil
}
