package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
	hubsocket "github.com/standardbeagle/go-cli-server/socket"
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
	running atomic.Bool
	// lifeMu serializes Start() and Stop() so their epoch swaps (running CAS,
	// shutdown channel, listener, and WaitGroup) can never interleave — which
	// otherwise caused a double-close panic and a wrong-listener close.
	lifeMu sync.Mutex
	// shutMu guards shutdown and wg. Both are per-epoch: recreated on each Start.
	// Giving each epoch its OWN WaitGroup means a prior epoch's in-flight
	// wg.Wait() (a Stop whose ctx expired mid-drain) can never collide with the
	// next epoch's wg.Add() — the reuse panic that a single shared WaitGroup risks.
	shutMu   sync.Mutex
	shutdown chan struct{}
	wg       *sync.WaitGroup
}

// currentShutdown returns the active shutdown channel under lock.
func (s *SubprocessServer) currentShutdown() chan struct{} {
	s.shutMu.Lock()
	defer s.shutMu.Unlock()
	return s.shutdown
}

// currentWG returns the active epoch WaitGroup under lock.
func (s *SubprocessServer) currentWG() *sync.WaitGroup {
	s.shutMu.Lock()
	defer s.shutMu.Unlock()
	return s.wg
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
	shutdown := make(chan struct{})
	close(shutdown)
	return &SubprocessServer{
		config:       config,
		handlers:     make(map[string]CommandHandler),
		verbRegistry: protocol.NewVerbRegistry(),
		shutdown:     shutdown,
		wg:           &sync.WaitGroup{},
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
	// Serialize the whole epoch setup against Stop() so a concurrent Start||Stop
	// cannot swap listener/shutdown/wg out from under each other.
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()

	// CAS instead of load-then-store so two concurrent Starts cannot both proceed.
	if !s.running.CompareAndSwap(false, true) {
		return fmt.Errorf("subprocess server already running")
	}

	// Reinstate a fresh shutdown channel AND a fresh WaitGroup for this epoch.
	// Stop() closed the previous channel; a restarted server's handlers would
	// otherwise see the closed channel and exit immediately. The fresh WaitGroup
	// isolates this epoch's Add()s from any prior epoch's still-draining Wait().
	epochWG := &sync.WaitGroup{}
	s.shutMu.Lock()
	s.shutdown = make(chan struct{})
	s.wg = epochWG
	s.shutMu.Unlock()

	var listener net.Listener
	var err error
	switch s.config.Transport.Type {
	case "unix":
		if err := removeStaleUnixSocket(s.config.Transport.Address); err != nil {
			s.running.Store(false)
			return err
		}
		listener, err = hubsocket.Listen("unix", s.config.Transport.Address)
	case "tcp":
		listener, err = hubsocket.Listen("tcp", s.config.Transport.Address)
	default:
		s.running.Store(false) // roll back the CAS so a retry can start
		return fmt.Errorf("unsupported transport type: %s", s.config.Transport.Type)
	}

	if err != nil {
		s.running.Store(false)
		return fmt.Errorf("failed to start listener: %w", err)
	}

	s.listener = listener
	epochWG.Add(1)
	// Pass the listener, this epoch's WaitGroup, AND its shutdown channel to the
	// accept loop so it operates on THIS epoch's resources rather than re-reading
	// shared fields a later Start could reassign.
	go s.acceptLoop(listener, epochWG, s.currentShutdown())

	return nil
}

func removeStaleUnixSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to stat unix socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("unix socket path exists and is not a socket: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to remove stale unix socket: %w", err)
	}
	return nil
}

// Stop stops the subprocess server gracefully. It holds lifeMu for the whole
// teardown (including the drain wait) so a concurrent Start cannot begin a new
// epoch — and in particular cannot wg.Add while this wg.Wait is draining.
func (s *SubprocessServer) Stop(ctx context.Context) error {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()

	// CAS so two concurrent Stops cannot both reach close(shutdown), which
	// panics on the second close.
	if !s.running.CompareAndSwap(true, false) {
		return nil
	}
	close(s.currentShutdown())

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

	// Wait for goroutines with timeout, on THIS epoch's WaitGroup. If ctx expires
	// mid-drain we drop lifeMu and return, but the next Start installs a brand-new
	// WaitGroup, so this straggler wait cannot collide with the next epoch's Add.
	epochWG := s.currentWG()
	done := make(chan struct{})
	go func() {
		epochWG.Wait()
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
	s.shutMu.Lock()
	shutdown := s.shutdown
	wg := s.wg
	s.shutMu.Unlock()
	<-shutdown
	wg.Wait()
}

// Address returns the actual address the server is listening on.
func (s *SubprocessServer) Address() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// acceptLoop accepts incoming connections on the listener and WaitGroup it was
// started with (its epoch's resources).
func (s *SubprocessServer) acceptLoop(listener net.Listener, wg *sync.WaitGroup, shutdown <-chan struct{}) {
	defer wg.Done()
	backoff := 10 * time.Millisecond

	for {
		conn, err := listener.Accept()
		if err != nil {
			// A closed listener (this epoch's Stop) terminates the loop. Checking
			// net.ErrClosed rather than the shared running flag prevents a busy-spin
			// when a later epoch has set running=true again.
			if errors.Is(err, net.ErrClosed) || !s.running.Load() {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				// Back off, but abandon the wait when this epoch stops. Sleeping
				// outright makes Stop block for up to the backoff (1s) waiting on
				// the WaitGroup, for no reason other than that we were napping.
				timer := time.NewTimer(backoff)
				select {
				case <-timer.C:
				case <-shutdown:
					timer.Stop()
					return
				}
				if backoff < time.Second {
					backoff *= 2
				}
				continue
			}
			return
		}
		backoff = 10 * time.Millisecond

		// Handle connection
		sc := &subprocessConn{
			server: s,
			conn:   conn,
			parser: protocol.NewParserWithRegistry(conn, s.verbRegistry),
			writer: protocol.NewWriter(conn),
			wg:     wg,
		}

		s.connections.Store(conn, sc)

		if s.config.OnConnect != nil {
			s.config.OnConnect()
		}

		wg.Add(1)
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
	// wg is the epoch WaitGroup this connection belongs to (its Done pairs the
	// Add in acceptLoop). Held directly so a Stop+Start swapping s.wg cannot make
	// this Done() target the wrong epoch's group.
	wg *sync.WaitGroup
}

func (c *subprocessConn) handleConnection() {
	defer c.wg.Done()
	defer c.close()

	// Capture the shutdown channel for this server epoch once, under lock, rather
	// than reading the field each iteration (a restart could reassign it).
	shutdown := c.server.currentShutdown()

	for {
		select {
		case <-shutdown:
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
				if err := c.writeResponse(&protocol.Response{
					Type:    protocol.ResponseErr,
					Code:    string(protocol.ErrInvalidCommand),
					Message: fmt.Sprintf("unknown command: %s", unknownErr.Verb),
				}); err != nil {
					if c.server.config.OnDisconnect != nil {
						c.server.config.OnDisconnect(err)
					}
					return
				}
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
			if err := c.writeResponse(resp); err != nil {
				if c.server.config.OnDisconnect != nil {
					c.server.config.OnDisconnect(err)
				}
				return
			}
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
	case protocol.ResponseChunk:
		err = c.writer.WriteChunk(resp.Data)
	case protocol.ResponseEnd:
		err = c.writer.WriteEnd()
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
	input        io.ReadCloser
	output       io.Writer
	closeInput   bool
}

// NewSubprocessStdioServer creates a subprocess server that uses stdin/stdout.
func NewSubprocessStdioServer() *SubprocessStdioServer {
	return &SubprocessStdioServer{
		handlers:     make(map[string]CommandHandler),
		verbRegistry: protocol.NewVerbRegistry(),
		input:        os.Stdin,
		output:       os.Stdout,
		closeInput:   true,
	}
}

// NewSubprocessStdioServerWithIO creates a subprocess stdio server using the
// provided streams. Stop closes input to unblock Run when it is waiting for the
// next command.
func NewSubprocessStdioServerWithIO(input io.ReadCloser, output io.Writer) *SubprocessStdioServer {
	s := NewSubprocessStdioServer()
	if input != nil {
		s.input = input
	}
	if output != nil {
		s.output = output
	}
	return s
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

	parser := protocol.NewParserWithRegistry(s.input, s.verbRegistry)
	writer := protocol.NewWriter(s.output)

	for s.running.Load() {
		cmd, err := parser.ParseCommand()
		if err != nil {
			if !s.running.Load() {
				return nil
			}
			// Check for EOF (compare the sentinel, not its string form)
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}

		resp := s.handleCommand(cmd)
		if resp != nil {
			if err := writeStdioResponse(writer, resp); err != nil {
				return err
			}
		}
	}

	return nil
}

// Stop stops the stdio server.
func (s *SubprocessStdioServer) Stop() {
	s.running.Store(false)
	if s.closeInput && s.input != nil {
		_ = s.input.Close()
	}
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

func writeStdioResponse(writer *protocol.Writer, resp *protocol.Response) error {
	switch resp.Type {
	case protocol.ResponseOK:
		return writer.WriteOK(resp.Message)
	case protocol.ResponseErr:
		return writer.WriteErr(protocol.ErrorCode(resp.Code), resp.Message)
	case protocol.ResponsePong:
		return writer.WritePong()
	case protocol.ResponseJSON:
		return writer.WriteJSON(resp.Data)
	case protocol.ResponseData:
		return writer.WriteData(resp.Data)
	case protocol.ResponseChunk:
		return writer.WriteChunk(resp.Data)
	case protocol.ResponseEnd:
		return writer.WriteEnd()
	}
	return fmt.Errorf("unsupported response type: %s", resp.Type)
}

// RegisterWithHub connects to a hub and registers this process as a subprocess.
// This is a convenience function for the registration flow.
func RegisterWithHub(socketPath string, config protocol.SubprocessRegisterConfig) error {
	// Connect to hub with a bounded dial + exchange. Without a timeout a hung or
	// half-open hub would block registration (and any caller waiting on it)
	// forever.
	conn, err := net.DialTimeout("unix", socketPath, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to hub: %w", err)
	}
	defer conn.Close()

	// Bound the register/response round-trip too.
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

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

func (p *pipeConn) Read(b []byte) (int, error)  { return p.reader.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error) { return p.writer.Write(b) }
func (p *pipeConn) Close() error                { return p.cmd.Process.Kill() }
func (p *pipeConn) LocalAddr() net.Addr         { return nil }
func (p *pipeConn) RemoteAddr() net.Addr        { return nil }

// deadliner is the subset of net.Conn deadline behavior. exec's stdin/stdout
// pipes are *os.File, which supports read/write deadlines, so we delegate rather
// than silently no-op — a no-op meant a hung stdio subprocess blocked the hub
// read forever.
type deadliner interface{ SetDeadline(time.Time) error }
type readDeadliner interface{ SetReadDeadline(time.Time) error }
type writeDeadliner interface{ SetWriteDeadline(time.Time) error }

func (p *pipeConn) SetDeadline(t time.Time) error {
	// A pipe half only supports the deadline for its own direction; set each on
	// the corresponding end.
	_ = p.SetReadDeadline(t)
	return p.SetWriteDeadline(t)
}

func (p *pipeConn) SetReadDeadline(t time.Time) error {
	if d, ok := p.reader.(readDeadliner); ok {
		return d.SetReadDeadline(t)
	}
	if d, ok := p.reader.(deadliner); ok {
		return d.SetDeadline(t)
	}
	return nil
}

func (p *pipeConn) SetWriteDeadline(t time.Time) error {
	if d, ok := p.writer.(writeDeadliner); ok {
		return d.SetWriteDeadline(t)
	}
	if d, ok := p.writer.(deadliner); ok {
		return d.SetDeadline(t)
	}
	return nil
}
