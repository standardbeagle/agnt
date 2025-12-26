package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// Connection represents a client connection to the daemon.
type Connection struct {
	id     int64
	conn   net.Conn
	daemon *Daemon

	parser *protocol.Parser
	writer *protocol.Writer

	mu          sync.Mutex // Protects writes
	closed      bool
	sessionCode string // Session code if this connection registered a session
}

// newConnection creates a new connection handler.
func newConnection(id int64, conn net.Conn, daemon *Daemon) *Connection {
	return &Connection{
		id:     id,
		conn:   conn,
		daemon: daemon,
		parser: protocol.NewParser(conn),
		writer: protocol.NewWriter(conn),
	}
}

// Handle processes commands from the client until disconnect or error.
func (c *Connection) Handle(ctx context.Context) {
	defer c.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set read deadline if configured
		if c.daemon.config.ReadTimeout > 0 {
			c.conn.SetReadDeadline(time.Now().Add(c.daemon.config.ReadTimeout))
		}

		// Parse next command
		cmd, err := c.parser.ParseCommand()
		if err != nil {
			if err == io.EOF || isClosedError(err) {
				return // Client disconnected
			}
			if isTimeoutError(err) {
				continue // Timeout, try again
			}
			log.Printf("Client %d: parse error: %v", c.id, err)
			c.writeErr(protocol.ErrInvalidArgs, err.Error())
			continue
		}

		// Handle command
		if err := c.handleCommand(ctx, cmd); err != nil {
			if isClosedError(err) {
				return
			}
			log.Printf("Client %d: command error: %v", c.id, err)
			// Error already sent to client in handleCommand
		}
	}
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

// handleCommand dispatches a command to the appropriate handler.
func (c *Connection) handleCommand(ctx context.Context, cmd *protocol.Command) error {
	switch cmd.Verb {
	case protocol.VerbPing:
		return c.handlePing()
	case protocol.VerbInfo:
		return c.handleInfo()
	case protocol.VerbShutdown:
		return c.handleShutdown()
	case protocol.VerbDetect:
		return c.handleDetect(cmd)
	case protocol.VerbRun, protocol.VerbRunJSON:
		return c.handleRun(ctx, cmd)
	case protocol.VerbProc:
		return c.handleProc(ctx, cmd)
	case protocol.VerbProxy:
		return c.handleProxy(ctx, cmd)
	case protocol.VerbProxyLog:
		return c.handleProxyLog(cmd)
	case protocol.VerbCurrentPage:
		return c.handleCurrentPage(cmd)
	case protocol.VerbOverlay:
		return c.handleOverlay(cmd)
	case protocol.VerbTunnel:
		return c.handleTunnel(ctx, cmd)
	case protocol.VerbChaos:
		return c.handleChaos(cmd)
	case protocol.VerbSession:
		return c.handleSession(cmd)
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidCommand,
			Message:      "unknown command",
			Command:      cmd.Verb,
			ValidActions: protocol.DefaultRegistry.ValidVerbs(),
		})
	}
}

// handlePing handles the PING command.
func (c *Connection) handlePing() error {
	return c.writePong()
}

// handleInfo handles the INFO command.
func (c *Connection) handleInfo() error {
	info := c.daemon.Info()
	data, err := json.Marshal(info)
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}
	return c.writeJSON(data)
}

// handleShutdown handles the SHUTDOWN command.
func (c *Connection) handleShutdown() error {
	// Send OK before shutdown
	if err := c.writeOK("shutting down"); err != nil {
		return err
	}

	// Trigger daemon shutdown in background
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.daemon.Stop(ctx)
	}()

	return nil
}

// Write helpers with locking

func (c *Connection) writeOK(msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.daemon.config.WriteTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.daemon.config.WriteTimeout))
	}
	return c.writer.WriteOK(msg)
}

func (c *Connection) writeErr(code protocol.ErrorCode, msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.daemon.config.WriteTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.daemon.config.WriteTimeout))
	}
	return c.writer.WriteErr(code, msg)
}

// writeStructuredErr sends a structured error as JSON for programmatic parsing.
func (c *Connection) writeStructuredErr(err *protocol.StructuredError) error {
	data, _ := json.Marshal(err)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.daemon.config.WriteTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.daemon.config.WriteTimeout))
	}
	return c.writer.WriteErr(err.Code, string(data))
}

func (c *Connection) writePong() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.daemon.config.WriteTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.daemon.config.WriteTimeout))
	}
	return c.writer.WritePong()
}

func (c *Connection) writeJSON(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.daemon.config.WriteTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.daemon.config.WriteTimeout))
	}
	return c.writer.WriteJSON(data)
}

func (c *Connection) writeChunk(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.daemon.config.WriteTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.daemon.config.WriteTimeout))
	}
	return c.writer.WriteChunk(data)
}

func (c *Connection) writeEnd() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.daemon.config.WriteTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.daemon.config.WriteTimeout))
	}
	return c.writer.WriteEnd()
}

// Helper functions

func isClosedError(err error) bool {
	if err == nil {
		return false
	}
	// Check for net.ErrClosed or the error message (which may be wrapped)
	return err == net.ErrClosed ||
		strings.Contains(err.Error(), "use of closed network connection")
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	netErr, ok := err.(net.Error)
	return ok && netErr.Timeout()
}

// BufferedWriter wraps a connection with buffered writing.
type BufferedWriter struct {
	conn net.Conn
	bw   *bufio.Writer
}

// NewBufferedWriter creates a buffered writer for a connection.
func NewBufferedWriter(conn net.Conn) *BufferedWriter {
	return &BufferedWriter{
		conn: conn,
		bw:   bufio.NewWriter(conn),
	}
}

// Write writes data to the buffer.
func (w *BufferedWriter) Write(p []byte) (n int, err error) {
	return w.bw.Write(p)
}

// Flush flushes the buffer to the connection.
func (w *BufferedWriter) Flush() error {
	return w.bw.Flush()
}
