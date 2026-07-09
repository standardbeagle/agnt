// Package hub provides a reusable daemon/hub component for MCP servers.
// It manages client connections, command dispatch, and optional process management.
package hub

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/protocol"
	"github.com/standardbeagle/go-cli-server/socket"
)

// Config holds configuration for the Hub.
type Config struct {
	// SocketPath is the Unix socket path. Empty uses default.
	SocketPath string
	// SocketName is used to generate the default socket path.
	SocketName string
	// MaxClients is the maximum number of concurrent clients (0 = unlimited).
	MaxClients int
	// ReadTimeout is the timeout for reading from clients.
	ReadTimeout time.Duration
	// WriteTimeout is the timeout for writing to clients.
	WriteTimeout time.Duration
	// EnableProcessMgmt enables the built-in ProcessManager.
	EnableProcessMgmt bool
	// EnableProcessCommands registers built-in PROC/RUN/RUN-JSON handlers.
	// Library consumers that provide their own command surface should leave this
	// false and register handlers explicitly.
	EnableProcessCommands bool
	// EnableScriptCommands registers built-in SCRIPT handlers.
	EnableScriptCommands bool
	// EnableSessionCommands registers built-in SESSION handlers.
	EnableSessionCommands bool
	// ProcessConfig is configuration for the ProcessManager (if enabled).
	ProcessConfig process.ManagerConfig
	// Version is the hub version string for INFO responses.
	Version string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		SocketName:        socket.DefaultSocketName,
		MaxClients:        0,
		ReadTimeout:       0,
		WriteTimeout:      5 * time.Second,
		EnableProcessMgmt: true,
		ProcessConfig:     process.DefaultManagerConfig(),
		Version:           "1.0.0",
	}
}

// Hub is the central daemon that coordinates clients and commands.
type Hub struct {
	config Config

	// Socket management
	sockMgr  *socket.Manager
	listener net.Listener

	// Process management (optional)
	pm *process.ProcessManager

	// Subprocess router
	subRouter *SubprocessRouter

	// Command registry
	commands         *CommandRegistry
	protocolRegistry *protocol.VerbRegistry

	// Client tracking (lock-free)
	clients     sync.Map // clientID -> *Connection
	clientCount atomic.Int64
	nextID      atomic.Int64

	// Session registry
	sessions sync.Map // sessionCode -> *Session

	// Lifecycle
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	shutdown  atomic.Bool
	startTime time.Time

	// Cleanup callback
	sessionCleanup func(sessionCode string)

	// Shutdown callback - called when hub receives SHUTDOWN command
	onShutdown func()
}

// New creates a new Hub with the given configuration.
func New(config Config) *Hub {
	if config.SocketName == "" {
		config.SocketName = socket.DefaultSocketName
	}

	sockConfig := socket.Config{
		Path: config.SocketPath,
		Name: config.SocketName,
	}

	ctx, cancel := context.WithCancel(context.Background())

	protocolRegistry := protocol.NewVerbRegistry()
	h := &Hub{
		config:           config,
		sockMgr:          socket.NewManager(sockConfig),
		protocolRegistry: protocolRegistry,
		commands:         NewCommandRegistry(protocolRegistry),
		ctx:              ctx,
		cancel:           cancel,
		startTime:        time.Now(),
	}

	// Create ProcessManager if enabled
	if config.EnableProcessMgmt {
		h.pm = process.NewProcessManager(config.ProcessConfig)
	}

	// Create subprocess router
	h.subRouter = NewSubprocessRouter(h)

	// Register built-in commands
	h.registerBuiltinCommands()

	return h
}

// registerBuiltinCommands registers the core hub commands.
func (h *Hub) registerBuiltinCommands() {
	// PROC/RUN commands are opt-in. Consumers often provide richer handlers under
	// these verbs; registering thin defaults here would shadow them.
	if h.pm != nil && h.config.EnableProcessCommands {
		_ = h.commands.Register(CommandDefinition{
			Verb:     "PROC",
			SubVerbs: []string{"STATUS", "OUTPUT", "STOP", "LIST", "CLEANUP-PORT", "STDIN"},
			Handler:  h.handleProc,
		})

		_ = h.commands.Register(CommandDefinition{
			Verb:    "RUN",
			Handler: h.handleRun,
		})

		_ = h.commands.Register(CommandDefinition{
			Verb:    "RUN-JSON",
			Handler: h.handleRun,
		})
	}

	// SCRIPT command is opt-in for the same reason as PROC/RUN.
	if h.pm != nil && h.config.EnableScriptCommands {
		_ = h.commands.Register(CommandDefinition{
			Verb:     "SCRIPT",
			SubVerbs: []string{"LIST", "GET", "OUTPUT", "RESTART", "STOP"},
			Handler:  h.handleScript,
		})
	}

	if h.config.EnableSessionCommands {
		_ = h.commands.Register(CommandDefinition{
			Verb:     "SESSION",
			SubVerbs: []string{"REGISTER", "UNREGISTER", "HEARTBEAT", "LIST", "GET"},
			Handler:  h.handleSession,
		})
	}

	// SUBPROCESS commands (via SubprocessRouter)
	h.subRouter.RegisterSubprocessCommands()
}

// RegisterCommand adds a custom command handler.
func (h *Hub) RegisterCommand(def CommandDefinition) error {
	return h.commands.Register(def)
}

// ExtendCommand adds product-specific sub-verbs to an already registered
// command without replacing the command's default handler or existing
// sub-handlers. Extensions are scoped to this Hub instance.
func (h *Hub) ExtendCommand(def CommandDefinition) error {
	return h.commands.Extend(def)
}

// ReplaceSubHandler intentionally replaces one existing sub-verb handler on
// this Hub instance. It does not add new sub-verbs; use ExtendCommand for that.
func (h *Hub) ReplaceSubHandler(verb, subVerb string, handler CommandHandler) error {
	return h.commands.ReplaceSubHandler(verb, subVerb, handler)
}

// ReplaceCommandHandler intentionally replaces the default handler for an
// existing command on this Hub instance. Prefer ExtendCommand or
// ReplaceSubHandler when a sub-verb boundary exists.
func (h *Hub) ReplaceCommandHandler(verb string, handler CommandHandler) error {
	return h.commands.ReplaceCommandHandler(verb, handler)
}

// HasCommand reports whether this Hub instance has a handler for verb.
func (h *Hub) HasCommand(verb string) bool {
	return h.commands.HasVerb(verb)
}

// ValidSubVerbs returns this Hub instance's registered sub-verbs for verb.
func (h *Hub) ValidSubVerbs(verb string) []string {
	return h.commands.ValidSubVerbs(verb)
}

// ProcessManager returns the ProcessManager, or nil if not enabled.
func (h *Hub) ProcessManager() *process.ProcessManager {
	return h.pm
}

// Start starts the hub and begins accepting connections.
func (h *Hub) Start() error {
	listener, err := h.sockMgr.Listen()
	if err != nil {
		return err
	}
	h.listener = listener

	h.wg.Add(1)
	go h.acceptLoop()

	return nil
}

// Stop gracefully shuts down the hub.
func (h *Hub) Stop(ctx context.Context) error {
	if !h.shutdown.CompareAndSwap(false, true) {
		return nil // Already shutting down
	}

	h.cancel()

	// Close listener to unblock accept loop
	if h.listener != nil {
		h.listener.Close()
	}

	// Close all client connections
	h.clients.Range(func(key, value any) bool {
		conn := value.(*Connection)
		conn.Close()
		return true
	})

	// Stop all subprocesses (stdio children + health-check goroutines) before the
	// process manager. Nothing else calls StopAll, so without this hub shutdown
	// orphans every stdio child and leaks its health loop.
	if h.subRouter != nil {
		_ = h.subRouter.StopAll(ctx)
	}

	// Shutdown ProcessManager if enabled
	if h.pm != nil {
		_ = h.pm.Shutdown(ctx)
	}

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	// Cleanup socket files
	return h.sockMgr.Close()
}

// Wait blocks until the hub stops.
func (h *Hub) Wait() {
	h.wg.Wait()
}

// acceptLoop accepts client connections.
func (h *Hub) acceptLoop() {
	defer h.wg.Done()

	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if h.shutdown.Load() {
				return
			}
			// Check if it's a temporary error
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			// Fatal error (e.g. fd exhaustion) with no shutdown in progress: the
			// accept loop can no longer serve, and simply returning would leave a
			// zombie hub still holding the socket path (blocking restarts, and
			// squattable). Drive a full shutdown so the socket/pid files are
			// released. Stop is idempotent and waits on this goroutine's wg slot,
			// which we release by returning immediately.
			if !h.shutdown.Load() {
				go h.Stop(context.Background())
			}
			return
		}

		// Check max clients
		if h.config.MaxClients > 0 && h.clientCount.Load() >= int64(h.config.MaxClients) {
			conn.Close()
			continue
		}

		clientID := h.nextID.Add(1)
		clientConn := newConnection(clientID, conn, h)
		h.clients.Store(clientID, clientConn)
		h.clientCount.Add(1)

		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			clientConn.Handle(h.ctx)
		}()
	}
}

// removeClient removes a client from the registry.
func (h *Hub) removeClient(id int64) {
	h.clients.Delete(id)
	h.clientCount.Add(-1)
}

// cleanupSession cleans up resources for a session.
func (h *Hub) cleanupSession(sessionCode string) {
	if h.sessionCleanup != nil {
		h.sessionCleanup(sessionCode)
	}

	// Stop processes for this session if ProcessManager enabled
	if h.pm != nil {
		if session, ok := h.sessions.Load(sessionCode); ok {
			s := session.(*Session)
			_, _ = h.pm.StopByProjectPath(context.Background(), s.ProjectPath)
		}
	}

	h.sessions.Delete(sessionCode)
}

// SetSessionCleanup sets a callback for session cleanup.
func (h *Hub) SetSessionCleanup(fn func(sessionCode string)) {
	h.sessionCleanup = fn
}

// SetOnShutdown sets a callback invoked when the hub receives a SHUTDOWN command.
// This allows the host process to react to remote shutdown requests
// (e.g., cancel a signal context so the process exits cleanly).
func (h *Hub) SetOnShutdown(fn func()) {
	h.onShutdown = fn
}

// RegisterSession registers a session with the Hub.
// This allows external code to register sessions that will be cleaned up automatically
// when the connection closes. The session will be associated with the projectPath,
// and all processes in that path will be stopped on cleanup.
func (h *Hub) RegisterSession(code, projectPath string) {
	session := &Session{
		Code:        code,
		ProjectPath: projectPath,
		StartedAt:   time.Now(),
	}
	session.SetLastSeen(time.Now())
	h.sessions.Store(code, session)
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int64 {
	return h.clientCount.Load()
}

// IsShuttingDown returns true if the hub is shutting down.
func (h *Hub) IsShuttingDown() bool {
	return h.shutdown.Load()
}

// SocketPath returns the socket path.
func (h *Hub) SocketPath() string {
	return h.sockMgr.Path()
}

// Session represents a registered client session.
type Session struct {
	Code        string
	ProjectPath string
	Command     string
	Args        []string
	StartedAt   time.Time

	// lastSeen holds the heartbeat time as UnixNano. It is written by heartbeats
	// and read concurrently by SESSION LIST/GET, so it must be accessed atomically
	// rather than as a plain time.Time field.
	lastSeen atomic.Int64
}

// SetLastSeen atomically records the session's last-seen time.
func (s *Session) SetLastSeen(t time.Time) { s.lastSeen.Store(t.UnixNano()) }

// LastSeen atomically returns the session's last-seen time.
func (s *Session) LastSeen() time.Time { return time.Unix(0, s.lastSeen.Load()) }
