package hub

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
	"github.com/standardbeagle/go-cli-server/socket"
)

// Connection handles a single client connection to the hub.
type Connection struct {
	id   int64
	conn net.Conn
	hub  *Hub

	parser *protocol.Parser
	writer *protocol.Writer

	mu          sync.Mutex
	closed      bool
	sessionCode string
	// terminalWritten prevents an automatic STATUS tick from being emitted
	// after the terminal response for the current request.
	terminalWritten bool
}

// newConnection creates a new connection handler.
func newConnection(id int64, conn net.Conn, hub *Hub) *Connection {
	return &Connection{
		id:     id,
		conn:   conn,
		hub:    hub,
		parser: protocol.NewParserWithRegistry(conn, hub.protocolRegistry),
		writer: protocol.NewWriter(conn),
	}
}

// Handle processes commands from this connection until it closes.
func (c *Connection) Handle(ctx context.Context) {
	defer func() {
		c.Close()
		c.hub.removeClient(c.id)
		if c.sessionCode != "" {
			c.hub.cleanupSession(c.sessionCode)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set read deadline if configured
		if c.hub.config.ReadTimeout > 0 {
			_ = c.conn.SetReadDeadline(time.Now().Add(c.hub.config.ReadTimeout))
		}

		// Parse next command
		cmd, err := c.parser.ParseCommand()
		if err != nil {
			if err == io.EOF || socket.IsClosedError(err) {
				return
			}
			// A partial frame (including a mid-frame deadline timeout) desyncs the
			// stream irrecoverably — close instead of continuing or resyncing.
			if protocol.IsPartialFrame(err) || err == protocol.ErrFrameTooLarge {
				return
			}
			if isTimeoutError(err) {
				continue // Clean timeout between frames is OK, keep waiting
			}
			// Non-fatal parse error (e.g. unknown verb): readUntilTerminator
			// already consumed the whole offending frame up to its ";;", so the
			// stream is still aligned. Report and keep reading — do NOT Resync,
			// which would swallow the client's next legitimate command.
			if err := c.WriteErr(protocol.ErrInvalidCommand, err.Error()); err != nil {
				return
			}
			continue
		}

		// Dispatch command
		if err := c.handleCommand(ctx, cmd); err != nil {
			return
		}
	}
}

// handleCommand dispatches a command to the appropriate handler.
func (c *Connection) handleCommand(ctx context.Context, cmd *protocol.Command) error {
	// Handle built-in commands first
	switch cmd.Verb {
	case protocol.VerbPing:
		return c.WritePong()
	case protocol.VerbInfo:
		return c.handleInfo()
	case protocol.VerbShutdown:
		return c.handleShutdown()
	}

	// Dispatch to registered handlers
	return c.dispatchWithStatus(ctx, cmd)
}

// dispatchWithStatus keeps a healthy but silent request distinguishable from a
// wedged daemon. Status delivery is best-effort and never waits for the shared
// response writer: if a real response is being written, that tick is skipped.
func (c *Connection) dispatchWithStatus(ctx context.Context, cmd *protocol.Command) error {
	interval := c.hub.config.StatusInterval
	if interval < 0 {
		return c.hub.commands.Dispatch(ctx, c, cmd)
	}

	c.mu.Lock()
	c.terminalWritten = false
	c.mu.Unlock()
	done := make(chan struct{})
	started := time.Now()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				status, err := json.Marshal(map[string]any{
					"state":      "running",
					"verb":       cmd.Verb,
					"sub_verb":   cmd.SubVerb,
					"elapsed_ms": time.Since(started).Milliseconds(),
				})
				if err != nil {
					continue
				}
				_, err = c.TryWriteStatus(status)
				if err != nil {
					return
				}
			}
		}
	}()

	err := c.hub.commands.Dispatch(ctx, c, cmd)
	close(done)
	return err
}

// handleInfo returns hub information.
func (c *Connection) handleInfo() error {
	info := map[string]any{
		"version":      c.hub.config.Version,
		"uptime":       time.Since(c.hub.startTime),
		"client_count": c.hub.clientCount.Load(),
	}

	if c.hub.pm != nil {
		info["processes"] = c.hub.pm.ActiveCount()
	}

	data, err := json.Marshal(info)
	if err != nil {
		return c.WriteErr(protocol.ErrInternal, "failed to marshal info")
	}

	return c.WriteJSON(data)
}

// handleShutdown initiates hub shutdown.
//
// SHUTDOWN is intentionally unauthenticated. The hub runs in a single-user trust
// domain: the socket lives in a per-user private directory (0700, uid-owned — see
// socket.secureSocketDir) or XDG_RUNTIME_DIR, so only the owning user can connect.
// Any client that can reach the socket is already the user who owns the hub and
// its managed processes, so there is no privilege boundary to cross. If the hub is
// ever exposed beyond a single user (e.g. a shared TCP transport), SHUTDOWN — and
// every other command — would need an authentication layer added at the transport.
func (c *Connection) handleShutdown() error {
	_ = c.WriteOK("shutting down")
	go func() {
		if c.hub.onShutdown != nil {
			c.hub.onShutdown()
		}
		_ = c.hub.Stop(context.Background())
	}()
	return nil
}

// ID returns the connection ID.
func (c *Connection) ID() int64 {
	return c.id
}

// Hub returns the parent hub.
func (c *Connection) Hub() *Hub {
	return c.hub
}

// SessionCode returns the session code if registered.
func (c *Connection) SessionCode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionCode
}

// SetSessionCode sets the session code for this connection.
func (c *Connection) SetSessionCode(code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionCode = code
}

// Close closes the connection.
func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// IsClosed returns true if the connection is closed.
func (c *Connection) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Write methods for sending responses

// WriteOK sends an OK response.
func (c *Connection) WriteOK(msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	c.terminalWritten = true
	return c.writer.WriteOK(msg)
}

// WriteErr sends an error response.
func (c *Connection) WriteErr(code protocol.ErrorCode, msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	c.terminalWritten = true
	return c.writer.WriteErr(code, msg)
}

// WriteStructuredErr sends a structured error response.
func (c *Connection) WriteStructuredErr(err *protocol.StructuredError) error {
	data, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		return c.WriteErr(err.Code, err.Message)
	}
	return c.WriteJSON(data)
}

// Convenience error methods - reduce shotgun surgery by centralizing common error patterns

// WriteMissingParam sends a missing parameter error.
func (c *Connection) WriteMissingParam(command, param, message string) error {
	return c.WriteStructuredErr(protocol.NewMissingParamError(command, param, message))
}

// WriteInvalidAction sends an invalid action error.
func (c *Connection) WriteInvalidAction(command, action string, validActions []string) error {
	return c.WriteStructuredErr(protocol.NewInvalidActionError(command, action, validActions))
}

// WriteInternalErr sends an internal error.
func (c *Connection) WriteInternalErr(message string) error {
	return c.WriteStructuredErr(protocol.NewInternalError(message))
}

// WriteNotFound sends a not found error.
func (c *Connection) WriteNotFound(resource, id string) error {
	return c.WriteStructuredErr(protocol.NewNotFoundError(resource, id))
}

// WriteJSON sends a JSON response.
func (c *Connection) WriteJSON(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	c.terminalWritten = true
	return c.writer.WriteJSON(data)
}

// WriteData sends a binary data response.
func (c *Connection) WriteData(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	c.terminalWritten = true
	return c.writer.WriteData(data)
}

// WriteChunk sends a chunk in a streaming response.
func (c *Connection) WriteChunk(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	return c.writer.WriteChunk(data)
}

// TryWriteStatus sends a progress frame only when the response writer is idle.
// It returns sent=false rather than blocking behind another write.
func (c *Connection) TryWriteStatus(data []byte) (sent bool, err error) {
	if !c.mu.TryLock() {
		return false, nil
	}
	defer c.mu.Unlock()
	if c.closed {
		return false, net.ErrClosed
	}
	if c.terminalWritten {
		return false, nil
	}
	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	return true, c.writer.WriteStatus(data)
}

// WriteEnd sends the END marker for chunked responses.
func (c *Connection) WriteEnd() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	c.terminalWritten = true
	return c.writer.WriteEnd()
}

// WritePong sends a PONG response.
func (c *Connection) WritePong() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hub.config.WriteTimeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.config.WriteTimeout))
	}
	c.terminalWritten = true
	return c.writer.WritePong()
}

// Helper functions

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	netErr, ok := err.(net.Error)
	return ok && netErr.Timeout()
}
