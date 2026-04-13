package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
)

// SubprocessServer allows an application to act as a hub subprocess.
// It listens for incoming connections from the hub and dispatches commands
// to registered handlers.
type SubprocessServer struct {
	config   SubprocessServerConfig
	listener net.Listener

	// Handler registry
	handlers   map[string]CommandHandler // verb -> handler
	handlersMu sync.RWMutex

	// Custom verb registry for this server
	verbRegistry *protocol.VerbRegistry

	// Active connections
	connections sync.Map // conn -> *subprocessConn

	// State
	running  atomic.Bool
	shutdown chan struct{}
	wg       sync.WaitGroup
}

// SubprocessServerConfig configures a subprocess server.
type SubprocessServerConfig struct {
	// ID is the subprocess identifier (for logging)
	ID string

	// Transport configuration
	Transport TransportConfig

	// OnConnect is called when the hub connects
	OnConnect func()

	// OnDisconnect is called when the hub disconnects
	OnDisconnect func(err error)
}

// TransportConfig specifies how the subprocess listens for connections.
type TransportConfig struct {
	// Type: "unix", "tcp"
	Type string

	// Address: socket path for unix, host:port for tcp
	Address string
}

// CommandHandler handles a command from the hub.
type CommandHandler func(ctx context.Context, cmd *protocol.Command) *protocol.Response

// NewSubprocessServer creates a new subprocess server.
func NewSubprocessServer(config SubprocessServerConfig) *SubprocessServer {
	return &SubprocessServer{
		config:       config,
		handlers:     make(map[string]CommandHandler),
		verbRegistry: protocol.NewVerbRegistry(),
		shutdown:     make(chan struct{}),
	}
}

// RegisterHandler registers a handler for a command verb.
func (s *SubprocessServer) RegisterHandler(verb string, handler CommandHandler) {
	s.handlersMu.Lock()
	defer s.handlersMu.Unlock()
	s.handlers[verb] = handler
	s.verbRegistry.RegisterVerb(verb)
}

// RegisterHandlers registers multiple handlers at once.
func (s *SubprocessServer) RegisterHandlers(handlers map[string]CommandHandler) {
	s.handlersMu.Lock()
	defer s.handlersMu.Unlock()
	for verb, handler := range handlers {
		s.handlers[verb] = handler
		s.verbRegistry.RegisterVerb(verb)
	}
}

// Start starts the subprocess server.
func (s *SubprocessServer) Start() error {
	if s.running.Load() {
		return fmt.Errorf("subprocess server already running")
	}

	var err error
	switch s.config.Transport.Type {
	case "unix":
		// Remove stale socket file
		os.Remove(s.config.Transport.Address)
		s.listener, err = net.Listen("unix", s.config.Transport.Address)
	case "tcp":
		s.listener, err = net.Listen("tcp", s.config.Transport.Address)
	default:
		return fmt.Errorf("unsupported transport type: %s", s.config.Transport.Type)
	}

	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}

	s.running.Store(true)
	s.wg.Add(1)
	go s.acceptLoop()

	return nil
}

// Stop stops the subprocess server gracefully.
func (s *SubprocessServer) Stop(ctx context.Context) error {
	if !s.running.Load() {
		return nil
	}

	s.running.Store(false)
	close(s.shutdown)

	// Close listener
	if s.listener != nil {
		s.listener.Close()
	}

	// Close all connections
	s.connections.Range(func(key, value interface{}) bool {
		if conn, ok := value.(*subprocessConn); ok {
			conn.close()
		}
		return true
	})

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until the server is stopped.
func (s *SubprocessServer) Wait() {
	<-s.shutdown
	s.wg.Wait()
}

// Address returns the actual address the server is listening on.
func (s *SubprocessServer) Address() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// acceptLoop accepts incoming connections.
func (s *SubprocessServer) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.running.Load() {
				// Unexpected error
				continue
			}
			return
		}

		// Handle connection
		sc := &subprocessConn{
			server: s,
			conn:   conn,
			parser: protocol.NewParserWithRegistry(conn, s.verbRegistry),
			writer: protocol.NewWriter(conn),
		}

		s.connections.Store(conn, sc)

		if s.config.OnConnect != nil {
			s.config.OnConnect()
		}

		s.wg.Add(1)
		go sc.handleConnection()
	}
}

// subprocessConn represents a connection from the hub.
type subprocessConn struct {
	server *SubprocessServer
	conn   net.Conn
	parser *protocol.Parser
	writer *protocol.Writer
	closed atomic.Bool
}

func (c *subprocessConn) handleConnection() {
	defer c.server.wg.Done()
	defer c.close()

	for {
		select {
		case <-c.server.shutdown:
			return
		default:
		}

		// Set read deadline for responsiveness
		_ = c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		cmd, err := c.parser.ParseCommand()
		if err != nil {
			if c.closed.Load() || !c.server.running.Load() {
				return
			}
			// Check if timeout (expected, continue)
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			// Check for unknown command - respond with error but keep connection
			if unknownErr, ok := err.(*protocol.ErrUnknownCommand); ok {
				_ = c.writeResponse(&protocol.Response{
					Type:    protocol.ResponseErr,
					Code:    string(protocol.ErrInvalidCommand),
					Message: fmt.Sprintf("unknown command: %s", unknownErr.Verb),
				})
				continue
			}
			// Connection error
			if c.server.config.OnDisconnect != nil {
				c.server.config.OnDisconnect(err)
			}
			return
		}

		// Handle the command
		resp := c.handleCommand(cmd)
		if resp != nil {
			_ = c.writeResponse(resp)
		}
	}
}

func (c *subprocessConn) handleCommand(cmd *protocol.Command) *protocol.Response {
	// Built-in PING handler
	if cmd.Verb == protocol.VerbPing {
		return &protocol.Response{Type: protocol.ResponsePong}
	}

	// Look up handler
	c.server.handlersMu.RLock()
	handler, ok := c.server.handlers[cmd.Verb]
	c.server.handlersMu.RUnlock()

	if !ok {
		return &protocol.Response{
			Type:    protocol.ResponseErr,
			Code:    string(protocol.ErrInvalidCommand),
			Message: fmt.Sprintf("unknown command: %s", cmd.Verb),
		}
	}

	// Execute handler with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return handler(ctx, cmd)
}

func (c *subprocessConn) writeResponse(resp *protocol.Response) error {
	var err error
	switch resp.Type {
	case protocol.ResponseOK:
		err = c.writer.WriteOK(resp.Message)
	case protocol.ResponseErr:
		err = c.writer.WriteErr(protocol.ErrorCode(resp.Code), resp.Message)
	case protocol.ResponsePong:
		err = c.writer.WritePong()
	case protocol.ResponseJSON:
		err = c.writer.WriteJSON(resp.Data)
	case protocol.ResponseData:
		err = c.writer.WriteData(resp.Data)
	}
	return err
}

func (c *subprocessConn) close() {
	if c.closed.Swap(true) {
		return // Already closed
	}
	c.conn.Close()
	c.server.connections.Delete(c.conn)
}

// Helper functions for creating responses

// OKResponse creates an OK response.
func OKResponse(message string) *protocol.Response {
	return &protocol.Response{
		Type:    protocol.ResponseOK,
		Message: message,
	}
}

// ErrResponse creates an error response.
func ErrResponse(code protocol.ErrorCode, message string) *protocol.Response {
	return &protocol.Response{
		Type:    protocol.ResponseErr,
		Code:    string(code),
		Message: message,
	}
}

// JSONResponse creates a JSON response.
func JSONResponse(data interface{}) *protocol.Response {
	bytes, err := json.Marshal(data)
	if err != nil {
		return ErrResponse(protocol.ErrInternal, fmt.Sprintf("failed to marshal JSON: %v", err))
	}
	return &protocol.Response{
		Type: protocol.ResponseJSON,
		Data: bytes,
	}
}

// DataResponse creates a binary data response.
func DataResponse(data []byte) *protocol.Response {
	return &protocol.Response{
		Type: protocol.ResponseData,
		Data: data,
	}
}

// SubprocessStdioServer is a simpler server for stdio-based subprocesses.
// It reads commands from stdin and writes responses to stdout.
type SubprocessStdioServer struct {
	handlers     map[string]CommandHandler
	handlersMu   sync.RWMutex
	verbRegistry *protocol.VerbRegistry
	running      atomic.Bool
}

// NewSubprocessStdioServer creates a subprocess server that uses stdin/stdout.
func NewSubprocessStdioServer() *SubprocessStdioServer {
	return &SubprocessStdioServer{
		handlers:     make(map[string]CommandHandler),
		verbRegistry: protocol.NewVerbRegistry(),
	}
}

// RegisterHandler registers a command handler.
func (s *SubprocessStdioServer) RegisterHandler(verb string, handler CommandHandler) {
	s.handlersMu.Lock()
	defer s.handlersMu.Unlock()
	s.handlers[verb] = handler
	s.verbRegistry.RegisterVerb(verb)
}

// Run starts processing commands from stdin.
// This blocks until stdin is closed or Stop is called.
func (s *SubprocessStdioServer) Run() error {
	s.running.Store(true)

	parser := protocol.NewParserWithRegistry(os.Stdin, s.verbRegistry)
	writer := protocol.NewWriter(os.Stdout)

	for s.running.Load() {
		cmd, err := parser.ParseCommand()
		if err != nil {
			if !s.running.Load() {
				return nil
			}
			// Check for EOF
			if err.Error() == "EOF" {
				return nil
			}
			continue
		}

		resp := s.handleCommand(cmd)
		if resp != nil {
			writeStdioResponse(writer, resp)
		}
	}

	return nil
}

// Stop stops the stdio server.
func (s *SubprocessStdioServer) Stop() {
	s.running.Store(false)
}

func (s *SubprocessStdioServer) handleCommand(cmd *protocol.Command) *protocol.Response {
	if cmd.Verb == protocol.VerbPing {
		return &protocol.Response{Type: protocol.ResponsePong}
	}

	s.handlersMu.RLock()
	handler, ok := s.handlers[cmd.Verb]
	s.handlersMu.RUnlock()

	if !ok {
		return ErrResponse(protocol.ErrInvalidCommand, fmt.Sprintf("unknown command: %s", cmd.Verb))
	}

	ctx := context.Background()
	return handler(ctx, cmd)
}

func writeStdioResponse(writer *protocol.Writer, resp *protocol.Response) {
	switch resp.Type {
	case protocol.ResponseOK:
		_ = writer.WriteOK(resp.Message)
	case protocol.ResponseErr:
		_ = writer.WriteErr(protocol.ErrorCode(resp.Code), resp.Message)
	case protocol.ResponsePong:
		_ = writer.WritePong()
	case protocol.ResponseJSON:
		_ = writer.WriteJSON(resp.Data)
	case protocol.ResponseData:
		_ = writer.WriteData(resp.Data)
	}
}

// RegisterWithHub connects to a hub and registers this process as a subprocess.
// This is a convenience function for the registration flow.
func RegisterWithHub(socketPath string, config protocol.SubprocessRegisterConfig) error {
	// Connect to hub
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to hub: %w", err)
	}
	defer conn.Close()

	parser := protocol.NewParser(conn)
	writer := protocol.NewWriter(conn)

	// Marshal config
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Send SUBPROCESS REGISTER command
	cmd := &protocol.Command{
		Verb:    protocol.VerbSubprocess,
		SubVerb: protocol.SubVerbRegister,
		Data:    data,
	}

	if err := writer.WriteCommandWithSubVerb(cmd.Verb, cmd.SubVerb, nil, cmd.Data); err != nil {
		return fmt.Errorf("failed to send register command: %w", err)
	}

	// Read response
	resp, err := parser.ParseResponse()
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.ResponseErr {
		return fmt.Errorf("registration failed: %s %s", resp.Code, resp.Message)
	}

	return nil
}

// StartWithHub registers with the hub and starts the subprocess server.
// This is the main entry point for subprocess applications.
func StartWithHub(hubSocket string, regConfig protocol.SubprocessRegisterConfig, serverConfig SubprocessServerConfig) (*SubprocessServer, error) {
	// Start the subprocess server first
	server := NewSubprocessServer(serverConfig)
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("failed to start server: %w", err)
	}

	// Update transport address if we're using a dynamic port
	if serverConfig.Transport.Type == "tcp" && serverConfig.Transport.Address == ":0" {
		regConfig.Transport.Address = server.Address()
	}

	// Register with hub
	if err := RegisterWithHub(hubSocket, regConfig); err != nil {
		_ = server.Stop(context.Background())
		return nil, fmt.Errorf("failed to register: %w", err)
	}

	return server, nil
}

// SpawnSubprocess is a helper for the hub to spawn a subprocess via stdio.
// Returns the process and stdin/stdout for communication.
func SpawnSubprocess(command string, args []string, env []string) (*exec.Cmd, net.Conn, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Create a pipe-based net.Conn wrapper
	conn := &pipeConn{
		reader: stdout,
		writer: stdin,
		cmd:    cmd,
	}

	return cmd, conn, nil
}

// pipeConn wraps stdin/stdout as a net.Conn
type pipeConn struct {
	reader interface{ Read([]byte) (int, error) }
	writer interface{ Write([]byte) (int, error) }
	cmd    *exec.Cmd
}

func (p *pipeConn) Read(b []byte) (int, error)         { return p.reader.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error)        { return p.writer.Write(b) }
func (p *pipeConn) Close() error                       { return p.cmd.Process.Kill() }
func (p *pipeConn) LocalAddr() net.Addr                { return nil }
func (p *pipeConn) RemoteAddr() net.Addr               { return nil }
func (p *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (p *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (p *pipeConn) SetWriteDeadline(t time.Time) error { return nil }
