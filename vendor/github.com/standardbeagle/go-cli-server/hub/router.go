package hub

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
)

// SubprocessRouter extends the Hub to support subprocess-based command routing.
// It maintains a registry of subprocesses and routes commands to them based on
// their registered command patterns.
type SubprocessRouter struct {
	hub *Hub

	// Subprocess registry
	subprocesses sync.Map // id -> *ManagedSubprocess

	// Routing tables (rebuilt when subprocesses change)
	exactRoutes  sync.Map // "PROXY START" -> subprocess ID
	prefixRoutes sync.Map // "PROXY" -> subprocess ID

	// Route version for cache invalidation
	routeVersion atomic.Int64

	// Statistics
	totalRouted     atomic.Int64
	totalFailed     atomic.Int64
	routingDuration atomic.Int64 // nanoseconds, for avg calculation
}

// ManagedSubprocess represents a subprocess managed by the router.
type ManagedSubprocess struct {
	// Configuration
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Commands    []string `json:"commands"` // Patterns: "PROXY *", "SESSION GET"
	Description string   `json:"description,omitempty"`

	// Transport - how to communicate
	Transport SubprocessTransportConfig `json:"transport"`

	// Lifecycle configuration
	AutoStart   bool          `json:"auto_start"`
	AutoRestart bool          `json:"auto_restart"`
	MaxRestarts int           `json:"max_restarts"`
	RestartWait time.Duration `json:"restart_wait"`

	// Health check configuration
	HealthCheck SubprocessHealthConfig `json:"health_check"`

	// State (atomic for lock-free reads)
	state        atomic.Value // ManagedSubprocessState
	stateChanged atomic.Pointer[time.Time]

	// Connection (protected by mutex for writes)
	conn   *SubprocessConn
	connMu sync.RWMutex

	// Statistics
	commandsHandled atomic.Int64
	commandsFailed  atomic.Int64
	restartCount    atomic.Int32
	lastCommand     atomic.Pointer[time.Time]
	lastHealthy     atomic.Pointer[time.Time]

	// Health tracking
	healthy          atomic.Bool
	consecutiveFails atomic.Int32

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// ManagedSubprocessState represents subprocess state.
type ManagedSubprocessState string

const (
	SubprocessPending  ManagedSubprocessState = "pending"
	SubprocessStarting ManagedSubprocessState = "starting"
	SubprocessRunning  ManagedSubprocessState = "running"
	SubprocessStopping ManagedSubprocessState = "stopping"
	SubprocessStopped  ManagedSubprocessState = "stopped"
	SubprocessFailed   ManagedSubprocessState = "failed"
)

// SubprocessTransportConfig defines how to communicate with a subprocess.
type SubprocessTransportConfig struct {
	// Type: "unix", "tcp", "stdio"
	Type string `json:"type"`
	// Address for "unix" or "tcp" transport
	Address string `json:"address,omitempty"`
	// Command for "stdio" transport
	Command string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
	// Timeout for connection/command operations
	Timeout time.Duration `json:"timeout,omitempty"`
}

// SubprocessHealthConfig defines health check behavior.
type SubprocessHealthConfig struct {
	Enabled          bool          `json:"enabled"`
	Interval         time.Duration `json:"interval"`
	Timeout          time.Duration `json:"timeout"`
	FailureThreshold int           `json:"failure_threshold"`
}

// DefaultSubprocessHealthConfig returns sensible defaults.
func DefaultSubprocessHealthConfig() SubprocessHealthConfig {
	return SubprocessHealthConfig{
		Enabled:          true,
		Interval:         10 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
	}
}

// SubprocessConn is a connection to a subprocess.
type SubprocessConn struct {
	parser *protocol.Parser
	writer *protocol.Writer
	closer func() error

	mu sync.Mutex
}

// SendCommand sends a command to the subprocess and reads the response.
func (c *SubprocessConn) SendCommand(ctx context.Context, cmd *protocol.Command) (*protocol.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Write the command using the Writer interface
	var err error
	if cmd.SubVerb != "" {
		err = c.writer.WriteCommandWithSubVerb(cmd.Verb, cmd.SubVerb, cmd.Args, cmd.Data)
	} else {
		err = c.writer.WriteCommand(cmd.Verb, cmd.Args, cmd.Data)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to write command: %w", err)
	}

	// Read response (this is blocking, context not directly supported by parser)
	// TODO: Add context-aware reading with deadline
	resp, err := c.parser.ParseResponse()
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return resp, nil
}

// Close closes the subprocess connection.
func (c *SubprocessConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closer != nil {
		return c.closer()
	}
	return nil
}

// NewSubprocessRouter creates a router that extends the hub with subprocess support.
func NewSubprocessRouter(hub *Hub) *SubprocessRouter {
	r := &SubprocessRouter{
		hub: hub,
	}

	// Register a catch-all handler that routes to subprocesses
	_ = hub.RegisterCommand(CommandDefinition{
		Verb:    "*", // Special: matches any unhandled command
		Handler: r.routeToSubprocess,
	})

	return r
}

// Register adds a subprocess to the registry.
func (r *SubprocessRouter) Register(sp *ManagedSubprocess) error {
	if sp.ID == "" {
		return fmt.Errorf("subprocess ID is required")
	}

	if _, exists := r.subprocesses.Load(sp.ID); exists {
		return fmt.Errorf("subprocess %q already registered", sp.ID)
	}

	// Initialize state
	sp.state.Store(SubprocessPending)
	now := time.Now()
	sp.stateChanged.Store(&now)

	// Create cancellable context
	sp.ctx, sp.cancel = context.WithCancel(context.Background())

	r.subprocesses.Store(sp.ID, sp)
	r.rebuildRoutes()

	return nil
}

// Unregister removes a subprocess from the registry.
func (r *SubprocessRouter) Unregister(id string) error {
	val, ok := r.subprocesses.Load(id)
	if !ok {
		return fmt.Errorf("subprocess %q not found", id)
	}

	sp := val.(*ManagedSubprocess)
	sp.cancel() // Signal shutdown

	// Wait for graceful stop
	done := make(chan struct{})
	go func() {
		sp.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	r.subprocesses.Delete(id)
	r.rebuildRoutes()

	return nil
}

// Get retrieves a subprocess by ID.
func (r *SubprocessRouter) Get(id string) (*ManagedSubprocess, bool) {
	val, ok := r.subprocesses.Load(id)
	if !ok {
		return nil, false
	}
	return val.(*ManagedSubprocess), true
}

// List returns all registered subprocesses.
func (r *SubprocessRouter) List() []*ManagedSubprocess {
	var result []*ManagedSubprocess
	r.subprocesses.Range(func(key, value interface{}) bool {
		result = append(result, value.(*ManagedSubprocess))
		return true
	})
	return result
}

// rebuildRoutes rebuilds the routing tables from registered subprocesses.
func (r *SubprocessRouter) rebuildRoutes() {
	// Clear existing routes
	r.exactRoutes = sync.Map{}
	r.prefixRoutes = sync.Map{}

	r.subprocesses.Range(func(key, value interface{}) bool {
		sp := value.(*ManagedSubprocess)
		for _, pattern := range sp.Commands {
			pattern = strings.ToUpper(strings.TrimSpace(pattern))
			if pattern == "" {
				continue
			}

			// Check for wildcard suffix
			if strings.HasSuffix(pattern, " *") || strings.HasSuffix(pattern, "*") {
				prefix := strings.TrimSuffix(strings.TrimSuffix(pattern, "*"), " ")
				r.prefixRoutes.Store(prefix, sp.ID)
			} else {
				r.exactRoutes.Store(pattern, sp.ID)
			}
		}
		return true
	})

	r.routeVersion.Add(1)
}

// routeToSubprocess is the handler that routes commands to subprocesses.
func (r *SubprocessRouter) routeToSubprocess(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	start := time.Now()
	defer func() {
		r.routingDuration.Add(time.Since(start).Nanoseconds())
	}()

	verb := strings.ToUpper(cmd.Verb)
	subverb := strings.ToUpper(cmd.SubVerb)

	// Build full command for exact match
	fullCmd := verb
	if subverb != "" {
		fullCmd = verb + " " + subverb
	}

	// Find the subprocess to route to
	var subprocessID string

	// 1. Try exact match
	if id, ok := r.exactRoutes.Load(fullCmd); ok {
		subprocessID = id.(string)
	} else if id, ok := r.prefixRoutes.Load(verb); ok {
		// 2. Try prefix match
		subprocessID = id.(string)
	}

	if subprocessID == "" {
		r.totalFailed.Add(1)
		return conn.WriteErr(protocol.ErrInvalidCommand, fmt.Sprintf("no subprocess handles command: %s", fullCmd))
	}

	// Get subprocess
	sp, ok := r.Get(subprocessID)
	if !ok {
		r.totalFailed.Add(1)
		return conn.WriteNotFound("subprocess", subprocessID)
	}

	// Check subprocess is running
	state := sp.state.Load().(ManagedSubprocessState)
	if state != SubprocessRunning {
		r.totalFailed.Add(1)
		return conn.WriteErr(protocol.ErrInvalidState, fmt.Sprintf("subprocess %s is not running (state: %s)", subprocessID, state))
	}

	// Get subprocess connection
	sp.connMu.RLock()
	spConn := sp.conn
	sp.connMu.RUnlock()

	if spConn == nil {
		r.totalFailed.Add(1)
		return conn.WriteErr(protocol.ErrInvalidState, fmt.Sprintf("subprocess %s has no active connection", subprocessID))
	}

	// Forward command to subprocess
	r.totalRouted.Add(1)
	now := time.Now()
	sp.lastCommand.Store(&now)

	resp, err := spConn.SendCommand(ctx, cmd)
	if err != nil {
		sp.commandsFailed.Add(1)
		r.totalFailed.Add(1)
		return conn.WriteInternalErr(fmt.Sprintf("failed to forward command: %v", err))
	}

	sp.commandsHandled.Add(1)

	// Relay response back to client
	return r.relayResponse(conn, resp)
}

// relayResponse relays a subprocess response to the client connection.
func (r *SubprocessRouter) relayResponse(conn *Connection, resp *protocol.Response) error {
	switch resp.Type {
	case protocol.ResponseOK:
		return conn.WriteOK(resp.Message)
	case protocol.ResponseErr:
		return conn.WriteErr(protocol.ErrorCode(resp.Code), resp.Message)
	case protocol.ResponseJSON:
		return conn.WriteJSON(resp.Data)
	case protocol.ResponseData:
		return conn.WriteData(resp.Data)
	case protocol.ResponsePong:
		return conn.WritePong()
	default:
		return conn.WriteInternalErr(fmt.Sprintf("unknown response type from subprocess: %s", resp.Type))
	}
}

// Start starts a subprocess.
func (r *SubprocessRouter) Start(ctx context.Context, id string) error {
	sp, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("subprocess %q not found", id)
	}

	return sp.start(ctx)
}

// Stop stops a subprocess.
func (r *SubprocessRouter) Stop(ctx context.Context, id string) error {
	sp, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("subprocess %q not found", id)
	}

	return sp.stop(ctx)
}

// StartAll starts all registered subprocesses.
func (r *SubprocessRouter) StartAll(ctx context.Context) error {
	var errs []error
	r.subprocesses.Range(func(key, value interface{}) bool {
		sp := value.(*ManagedSubprocess)
		if sp.AutoStart {
			if err := sp.start(ctx); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", sp.ID, err))
			}
		}
		return true
	})

	if len(errs) > 0 {
		return fmt.Errorf("failed to start %d subprocess(es)", len(errs))
	}
	return nil
}

// StopAll stops all running subprocesses.
func (r *SubprocessRouter) StopAll(ctx context.Context) error {
	var errs []error
	r.subprocesses.Range(func(key, value interface{}) bool {
		sp := value.(*ManagedSubprocess)
		if err := sp.stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sp.ID, err))
		}
		return true
	})

	if len(errs) > 0 {
		return fmt.Errorf("failed to stop %d subprocess(es)", len(errs))
	}
	return nil
}

// Stats returns router statistics.
func (r *SubprocessRouter) Stats() SubprocessRouterStats {
	stats := SubprocessRouterStats{
		Subprocesses: make([]ManagedSubprocessStats, 0),
	}

	r.subprocesses.Range(func(key, value interface{}) bool {
		sp := value.(*ManagedSubprocess)
		spStats := ManagedSubprocessStats{
			ID:              sp.ID,
			Name:            sp.Name,
			State:           sp.state.Load().(ManagedSubprocessState),
			Healthy:         sp.healthy.Load(),
			CommandsHandled: sp.commandsHandled.Load(),
			CommandsFailed:  sp.commandsFailed.Load(),
			RestartCount:    int(sp.restartCount.Load()),
		}
		if t := sp.lastCommand.Load(); t != nil {
			spStats.LastCommand = *t
		}
		stats.Subprocesses = append(stats.Subprocesses, spStats)
		stats.Total++
		if sp.state.Load() == SubprocessRunning {
			stats.Running++
		}
		if sp.healthy.Load() {
			stats.Healthy++
		}
		return true
	})

	stats.TotalRouted = r.totalRouted.Load()
	stats.TotalFailed = r.totalFailed.Load()
	if stats.TotalRouted > 0 {
		stats.AvgRoutingMs = float64(r.routingDuration.Load()) / float64(stats.TotalRouted) / 1e6
	}

	return stats
}

// SubprocessRouterStats contains router statistics.
type SubprocessRouterStats struct {
	Total        int                      `json:"total"`
	Running      int                      `json:"running"`
	Healthy      int                      `json:"healthy"`
	TotalRouted  int64                    `json:"total_routed"`
	TotalFailed  int64                    `json:"total_failed"`
	AvgRoutingMs float64                  `json:"avg_routing_ms"`
	Subprocesses []ManagedSubprocessStats `json:"subprocesses"`
}

// ManagedSubprocessStats contains statistics for a managed subprocess.
type ManagedSubprocessStats struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	State           ManagedSubprocessState `json:"state"`
	Healthy         bool                   `json:"healthy"`
	CommandsHandled int64                  `json:"commands_handled"`
	CommandsFailed  int64                  `json:"commands_failed"`
	RestartCount    int                    `json:"restart_count"`
	LastCommand     time.Time              `json:"last_command,omitempty"`
}

// GetRoutes returns the routing table for debugging.
func (r *SubprocessRouter) GetRoutes() map[string]string {
	routes := make(map[string]string)

	r.exactRoutes.Range(func(key, value interface{}) bool {
		routes[key.(string)] = value.(string) + " (exact)"
		return true
	})

	r.prefixRoutes.Range(func(key, value interface{}) bool {
		routes[key.(string)+" *"] = value.(string) + " (prefix)"
		return true
	})

	return routes
}

// start starts the subprocess.
func (sp *ManagedSubprocess) start(ctx context.Context) error {
	state := sp.state.Load().(ManagedSubprocessState)
	if state == SubprocessRunning || state == SubprocessStarting {
		return fmt.Errorf("subprocess already %s", state)
	}

	sp.state.Store(SubprocessStarting)
	now := time.Now()
	sp.stateChanged.Store(&now)

	// Connect based on transport type
	var err error
	switch sp.Transport.Type {
	case "unix":
		err = sp.connectUnix(ctx)
	case "tcp":
		err = sp.connectTCP(ctx)
	case "stdio":
		err = sp.startStdio(ctx)
	default:
		err = fmt.Errorf("unsupported transport type: %s", sp.Transport.Type)
	}

	if err != nil {
		sp.state.Store(SubprocessFailed)
		now = time.Now()
		sp.stateChanged.Store(&now)
		return fmt.Errorf("failed to start subprocess: %w", err)
	}

	sp.state.Store(SubprocessRunning)
	sp.healthy.Store(true)
	now = time.Now()
	sp.stateChanged.Store(&now)

	// Start health check loop if enabled
	if sp.HealthCheck.Enabled {
		sp.wg.Add(1)
		go sp.healthCheckLoop()
	}

	return nil
}

// connectUnix connects to a subprocess via Unix socket.
func (sp *ManagedSubprocess) connectUnix(ctx context.Context) error {
	address := sp.Transport.Address
	if address == "" {
		return fmt.Errorf("unix transport requires address")
	}

	timeout := sp.Transport.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	var d net.Dialer
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := d.DialContext(dialCtx, "unix", address)
	if err != nil {
		return fmt.Errorf("failed to connect to unix socket %s: %w", address, err)
	}

	sp.connMu.Lock()
	sp.conn = &SubprocessConn{
		parser: protocol.NewParser(conn),
		writer: protocol.NewWriter(conn),
		closer: conn.Close,
	}
	sp.connMu.Unlock()

	return nil
}

// connectTCP connects to a subprocess via TCP.
func (sp *ManagedSubprocess) connectTCP(ctx context.Context) error {
	address := sp.Transport.Address
	if address == "" {
		return fmt.Errorf("tcp transport requires address")
	}

	timeout := sp.Transport.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	var d net.Dialer
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := d.DialContext(dialCtx, "tcp", address)
	if err != nil {
		return fmt.Errorf("failed to connect to tcp %s: %w", address, err)
	}

	sp.connMu.Lock()
	sp.conn = &SubprocessConn{
		parser: protocol.NewParser(conn),
		writer: protocol.NewWriter(conn),
		closer: conn.Close,
	}
	sp.connMu.Unlock()

	return nil
}

// startStdio starts a subprocess via stdio transport.
func (sp *ManagedSubprocess) startStdio(ctx context.Context) error {
	if sp.Transport.Command == "" {
		return fmt.Errorf("stdio transport requires command")
	}

	// Create the command
	cmd := exec.CommandContext(sp.ctx, sp.Transport.Command, sp.Transport.Args...)

	// Set environment if specified
	if len(sp.Transport.Env) > 0 {
		cmd.Env = append(os.Environ(), sp.Transport.Env...)
	}

	// Get pipes for stdin/stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("failed to start command: %w", err)
	}

	// Create subprocess connection
	sp.connMu.Lock()
	sp.conn = &SubprocessConn{
		parser: protocol.NewParser(stdout),
		writer: protocol.NewWriter(stdin),
		closer: func() error {
			stdin.Close()
			stdout.Close()
			return cmd.Process.Kill()
		},
	}
	sp.connMu.Unlock()

	// Monitor the process in background
	sp.wg.Add(1)
	go func() {
		defer sp.wg.Done()
		err := cmd.Wait()
		if err != nil && sp.state.Load() == SubprocessRunning {
			sp.state.Store(SubprocessFailed)
			now := time.Now()
			sp.stateChanged.Store(&now)
			sp.healthy.Store(false)
		}
	}()

	return nil
}

// stop stops the subprocess.
func (sp *ManagedSubprocess) stop(ctx context.Context) error {
	state := sp.state.Load().(ManagedSubprocessState)
	if state != SubprocessRunning {
		return nil
	}

	sp.state.Store(SubprocessStopping)
	now := time.Now()
	sp.stateChanged.Store(&now)

	sp.cancel()

	// Close connection if exists
	sp.connMu.Lock()
	if sp.conn != nil {
		_ = sp.conn.closer()
		sp.conn = nil
	}
	sp.connMu.Unlock()

	// Wait for goroutines
	done := make(chan struct{})
	go func() {
		sp.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	sp.state.Store(SubprocessStopped)
	sp.healthy.Store(false)
	sp.stateChanged.Store(&now)

	return nil
}

// healthCheckLoop runs periodic health checks.
func (sp *ManagedSubprocess) healthCheckLoop() {
	defer sp.wg.Done()

	interval := sp.HealthCheck.Interval
	if interval == 0 {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-sp.ctx.Done():
			return
		case <-ticker.C:
			sp.doHealthCheck()
		}
	}
}

// doHealthCheck performs a single health check.
func (sp *ManagedSubprocess) doHealthCheck() {
	if sp.state.Load() != SubprocessRunning {
		return
	}

	sp.connMu.RLock()
	conn := sp.conn
	sp.connMu.RUnlock()

	if conn == nil {
		sp.consecutiveFails.Add(1)
		sp.checkHealthThreshold()
		return
	}

	// Create timeout context for health check
	timeout := sp.HealthCheck.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(sp.ctx, timeout)
	defer cancel()

	// Send PING command
	pingCmd := &protocol.Command{
		Verb: "PING",
	}

	resp, err := conn.SendCommand(ctx, pingCmd)
	if err != nil || resp.Type != protocol.ResponsePong {
		sp.consecutiveFails.Add(1)
		sp.checkHealthThreshold()
		return
	}

	// Health check passed
	sp.consecutiveFails.Store(0)
	sp.healthy.Store(true)
	now := time.Now()
	sp.lastHealthy.Store(&now)
}

// checkHealthThreshold checks if failure threshold is reached.
func (sp *ManagedSubprocess) checkHealthThreshold() {
	threshold := int32(sp.HealthCheck.FailureThreshold)
	if threshold == 0 {
		threshold = 3
	}

	if sp.consecutiveFails.Load() >= threshold {
		sp.healthy.Store(false)
		sp.triggerAutoRestart()
	}
}

// triggerAutoRestart attempts to restart the subprocess if auto-restart is enabled.
func (sp *ManagedSubprocess) triggerAutoRestart() {
	if !sp.AutoRestart {
		return
	}

	// Check restart limit
	if sp.MaxRestarts > 0 && int(sp.restartCount.Load()) >= sp.MaxRestarts {
		sp.state.Store(SubprocessFailed)
		now := time.Now()
		sp.stateChanged.Store(&now)
		return
	}

	// Only restart if currently running or failed
	state := sp.state.Load().(ManagedSubprocessState)
	if state != SubprocessRunning && state != SubprocessFailed {
		return
	}

	// Schedule restart in background
	sp.wg.Add(1)
	go sp.doRestart()
}

// doRestart performs the actual restart operation.
func (sp *ManagedSubprocess) doRestart() {
	defer sp.wg.Done()

	// Wait for restart delay
	if sp.RestartWait > 0 {
		select {
		case <-sp.ctx.Done():
			return
		case <-time.After(sp.RestartWait):
		}
	}

	// Stop current connection if any
	sp.connMu.Lock()
	if sp.conn != nil {
		sp.conn.Close()
		sp.conn = nil
	}
	sp.connMu.Unlock()

	// Increment restart count
	sp.restartCount.Add(1)

	// Reset health tracking
	sp.consecutiveFails.Store(0)
	sp.healthy.Store(false)

	// Attempt to start
	ctx, cancel := context.WithTimeout(sp.ctx, 30*time.Second)
	defer cancel()

	if err := sp.start(ctx); err != nil {
		// Start failed - will be retried by next health check if still unhealthy
		sp.state.Store(SubprocessFailed)
		now := time.Now()
		sp.stateChanged.Store(&now)
	}
}
