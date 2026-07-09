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

	// routes is an immutable routing snapshot swapped atomically on every rebuild.
	// Readers load the pointer once and read the maps lock-free; rebuildRoutes
	// builds fresh maps and Stores a new pointer. This replaces the previous
	// sync.Map fields, whose struct reassignment torn-raced routeToSubprocess.
	routes atomic.Pointer[routeTable]

	// rebuildMu serializes rebuildRoutes. The build reads the live subprocesses
	// map and Stores a fresh snapshot; two concurrent rebuilds could each miss the
	// other's just-registered entry and the later Store would drop it, leaving a
	// subprocess unroutable. Serializing makes the last build reflect the full map.
	rebuildMu sync.Mutex

	// Statistics
	totalRouted     atomic.Int64
	totalFailed     atomic.Int64
	routingDuration atomic.Int64 // nanoseconds, for avg calculation
}

// routeTable is an immutable snapshot of command routing. It is never mutated
// after publication; rebuildRoutes builds a new one and atomically swaps it in.
type routeTable struct {
	exact  map[string]string // "PROXY START" -> subprocess ID
	prefix map[string]string // "PROXY" (or "FOO BAR") -> subprocess ID
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

	// stopped is set by an explicit stop() and cleared by start(). doRestart
	// consults it after its wait so a subprocess the user deliberately stopped is
	// not resurrected by an auto-restart that was already in flight.
	stopped atomic.Bool

	// restarting guards the auto-restart path so a burst of failing health ticks
	// (Interval < RestartWait) cannot spawn a storm of concurrent doRestart
	// goroutines. Set before a restart is scheduled, cleared when it resolves.
	restarting atomic.Bool

	// Lifecycle. The context is held behind an atomic pointer so start()/restart
	// can install a fresh one without racing readers (health loop, monitored
	// process, stop()).
	life atomic.Pointer[subprocessLifecycle]
	wg   sync.WaitGroup
}

// subprocessLifecycle bundles a cancellable context with its cancel func.
type subprocessLifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// canceledContext is returned when no lifecycle has been installed yet.
var canceledContext = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

// newLifecycle installs a fresh cancellable context and returns it.
func (sp *ManagedSubprocess) newLifecycle() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	sp.life.Store(&subprocessLifecycle{ctx: ctx, cancel: cancel})
	return ctx
}

// currentCtx returns the active lifecycle context, or an already-cancelled one.
func (sp *ManagedSubprocess) currentCtx() context.Context {
	if l := sp.life.Load(); l != nil {
		return l.ctx
	}
	return canceledContext
}

// cancelLifecycle cancels the active lifecycle context if present.
func (sp *ManagedSubprocess) cancelLifecycle() {
	if l := sp.life.Load(); l != nil {
		l.cancel()
	}
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
	Command string   `json:"command,omitempty"`
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

	// rd is the read side of the transport, used to enforce an I/O deadline so a
	// hung subprocess cannot pin mu forever (which would wedge both routed
	// commands and the shared health-check PING). nil when the transport does not
	// support deadlines.
	rd      interface{ SetReadDeadline(time.Time) error }
	timeout time.Duration // per-command deadline; 0 disables

	// inFlight counts routed commands currently holding or waiting on mu. The
	// health-check PING shares this mu; a slow routed command would otherwise make
	// the PING block until its deadline and count as a health failure, restarting a
	// busy-but-healthy subprocess mid-work. doHealthCheck skips when inFlight > 0.
	inFlight atomic.Int32

	mu sync.Mutex
}

// busy reports whether a routed command is currently in flight.
func (c *SubprocessConn) busy() bool { return c.inFlight.Load() > 0 }

// SendCommand sends a command to the subprocess and reads the response.
func (c *SubprocessConn) SendCommand(ctx context.Context, cmd *protocol.Command) (*protocol.Response, error) {
	var first *protocol.Response
	err := c.SendCommandStream(ctx, cmd, func(resp *protocol.Response) error {
		first = resp
		return nil
	})
	return first, err
}

// SendCommandStream sends a command and relays every response frame until the
// subprocess ends the exchange. Non-streaming responses produce one callback.
func (c *SubprocessConn) SendCommandStream(ctx context.Context, cmd *protocol.Command, handle func(*protocol.Response) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Bound the exchange with a read deadline. Without it a hung subprocess would
	// block ParseResponse while holding mu, so the health-check PING (same mu)
	// could never fire and auto-restart would never trigger, while routed clients
	// pile up unbounded. Prefer the caller's ctx deadline, else the configured
	// timeout.
	if c.rd != nil {
		deadline, ok := ctx.Deadline()
		if !ok && c.timeout > 0 {
			deadline = time.Now().Add(c.timeout)
			ok = true
		}
		if ok {
			_ = c.rd.SetReadDeadline(deadline)
			defer func() { _ = c.rd.SetReadDeadline(time.Time{}) }()
		}
	}

	// Write the command using the Writer interface
	var err error
	if cmd.SubVerb != "" {
		err = c.writer.WriteCommandWithSubVerb(cmd.Verb, cmd.SubVerb, cmd.Args, cmd.Data)
	} else {
		err = c.writer.WriteCommand(cmd.Verb, cmd.Args, cmd.Data)
	}
	if err != nil {
		return fmt.Errorf("failed to write command: %w", err)
	}

	for {
		resp, err := c.parser.ParseResponse()
		if err != nil {
			// The read failed (deadline or transport error) but the subprocess may
			// still deliver the late response into the socket buffer. If we leave the
			// connection open, the NEXT SendCommand (routed command or health PING)
			// reads this command's stale response as its own and every subsequent
			// exchange is off-by-one forever. Tear the connection down so the next
			// caller fails fast and the health loop forces a reconnect. Close inline
			// (not via c.Close, which re-locks mu) since we already hold mu.
			if c.closer != nil {
				_ = c.closer()
				c.closer = nil
			}
			return fmt.Errorf("failed to read response: %w", err)
		}
		if err := handle(resp); err != nil {
			if c.closer != nil {
				_ = c.closer()
				c.closer = nil
			}
			return err
		}
		if resp.Type != protocol.ResponseChunk {
			return nil
		}
	}
}

// Close closes the subprocess connection.
func (c *SubprocessConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closer != nil {
		closer := c.closer
		c.closer = nil
		return closer()
	}
	return nil
}

// NewSubprocessRouter creates a router that extends the hub with subprocess support.
func NewSubprocessRouter(hub *Hub) *SubprocessRouter {
	r := &SubprocessRouter{
		hub: hub,
	}
	r.routes.Store(&routeTable{exact: map[string]string{}, prefix: map[string]string{}})

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

	r.rebuildMu.Lock()
	defer r.rebuildMu.Unlock()

	if err := r.validateRoutePatterns(sp); err != nil {
		return err
	}

	// Initialize state before publishing so a concurrent reader never observes a
	// half-built subprocess.
	sp.state.Store(SubprocessPending)
	now := time.Now()
	sp.stateChanged.Store(&now)
	sp.newLifecycle()

	// LoadOrStore is the atomic register-once primitive; a plain Load-then-Store
	// let two concurrent Registers of the same ID both win.
	if _, loaded := r.subprocesses.LoadOrStore(sp.ID, sp); loaded {
		return fmt.Errorf("subprocess %q already registered", sp.ID)
	}
	r.rebuildRoutesLocked()

	return nil
}

func (r *SubprocessRouter) validateRoutePatterns(sp *ManagedSubprocess) error {
	rt := r.routes.Load()
	seen := make(map[string]struct{})

	for _, raw := range sp.Commands {
		pattern := strings.ToUpper(strings.TrimSpace(raw))
		if pattern == "" {
			continue
		}

		isPrefix := strings.HasSuffix(pattern, " *") || strings.HasSuffix(pattern, "*")
		key := pattern
		kind := "exact"
		if isPrefix {
			key = strings.TrimSpace(strings.TrimSuffix(pattern, "*"))
			kind = "prefix"
		}
		fields := strings.Fields(key)
		if len(fields) == 0 {
			return fmt.Errorf("invalid empty command pattern %q", raw)
		}
		if len(fields) > 2 {
			return fmt.Errorf("command pattern %q is unroutable: only verb or verb+subverb patterns are supported", raw)
		}
		if r.hub.commands.HasVerb(fields[0]) {
			return fmt.Errorf("command pattern %q collides with hub command verb %q", raw, fields[0])
		}

		routeKey := kind + "\x00" + strings.Join(fields, " ")
		if _, ok := seen[routeKey]; ok {
			return fmt.Errorf("duplicate command pattern %q in subprocess %q", raw, sp.ID)
		}
		seen[routeKey] = struct{}{}

		normalized := strings.Join(fields, " ")
		if kind == "exact" {
			if existing, ok := rt.exact[normalized]; ok && existing != sp.ID {
				return fmt.Errorf("command pattern %q already registered by subprocess %q", raw, existing)
			}
		} else {
			if existing, ok := rt.prefix[normalized]; ok && existing != sp.ID {
				return fmt.Errorf("command pattern %q already registered by subprocess %q", raw, existing)
			}
		}
	}
	return nil
}

// Unregister removes a subprocess from the registry.
func (r *SubprocessRouter) Unregister(id string) error {
	val, ok := r.subprocesses.Load(id)
	if !ok {
		return fmt.Errorf("subprocess %q not found", id)
	}

	sp := val.(*ManagedSubprocess)
	// Mark stopped BEFORE cancelling so an in-flight doRestart (which is detached
	// and not tracked by sp.wg, so the Wait below does not cover it) aborts in its
	// stopped re-check instead of resurrecting an unregistered subprocess with a
	// leaked health loop and fd.
	sp.stopped.Store(true)
	sp.cancelLifecycle() // Signal shutdown

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

	// Close the transport. Cancelling the lifecycle stops the goroutines but does
	// not shut the socket/pipe fd; leaking one fd per register/unregister cycle
	// eventually exhausts the descriptor table and Accept starts failing.
	sp.connMu.Lock()
	if sp.conn != nil {
		_ = sp.conn.Close()
		sp.conn = nil
	}
	sp.connMu.Unlock()

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

// rebuildRoutes builds a fresh immutable routing table and swaps it in
// atomically. Never mutates the published table, so concurrent readers in
// routeToSubprocess are safe.
func (r *SubprocessRouter) rebuildRoutes() {
	r.rebuildMu.Lock()
	defer r.rebuildMu.Unlock()
	r.rebuildRoutesLocked()
}

func (r *SubprocessRouter) rebuildRoutesLocked() {
	exact := make(map[string]string)
	prefix := make(map[string]string)

	r.subprocesses.Range(func(key, value interface{}) bool {
		sp := value.(*ManagedSubprocess)
		for _, pattern := range sp.Commands {
			pattern = strings.ToUpper(strings.TrimSpace(pattern))
			if pattern == "" {
				continue
			}

			// Check for wildcard suffix
			if strings.HasSuffix(pattern, " *") || strings.HasSuffix(pattern, "*") {
				p := strings.Join(strings.Fields(strings.TrimSuffix(pattern, "*")), " ")
				prefix[p] = sp.ID
				// Register the verb so the parser accepts it and reaches dispatch.
				fields := strings.Fields(p)
				if len(fields) > 0 {
					r.hub.protocolRegistry.RegisterVerb(fields[0])
				}
				if len(fields) > 1 {
					r.hub.protocolRegistry.RegisterSubVerbForVerb(fields[0], fields[1])
				}
			} else {
				pattern = strings.Join(strings.Fields(pattern), " ")
				exact[pattern] = sp.ID
				// Register verb (and sub-verb for two-word patterns) with the parser so
				// the command survives parsing and reaches routeToSubprocess.
				fields := strings.Fields(pattern)
				if len(fields) > 0 {
					r.hub.protocolRegistry.RegisterVerb(fields[0])
				}
				if len(fields) > 1 {
					r.hub.protocolRegistry.RegisterSubVerbForVerb(fields[0], fields[1])
				}
			}
		}
		return true
	})

	r.routes.Store(&routeTable{exact: exact, prefix: prefix})
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

	// Find the subprocess to route to from the immutable routing snapshot.
	rt := r.routes.Load()
	var subprocessID string

	// 1. Exact match ("FOO BAR"). 2. Multi-word prefix ("FOO BAR *" keyed as
	// "FOO BAR"). 3. Single-verb prefix ("FOO *" keyed as "FOO").
	if id, ok := rt.exact[fullCmd]; ok {
		subprocessID = id
	} else if id, ok := rt.prefix[fullCmd]; ok {
		subprocessID = id
	} else if id, ok := rt.prefix[verb]; ok {
		subprocessID = id
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

	// Mark the connection busy so a concurrent health PING skips instead of
	// blocking on mu behind this command and mistaking the wait for a failure.
	spConn.inFlight.Add(1)
	defer spConn.inFlight.Add(-1)
	err := spConn.SendCommandStream(ctx, cmd, func(resp *protocol.Response) error {
		return r.relayResponse(conn, resp)
	})
	if err != nil {
		sp.commandsFailed.Add(1)
		r.totalFailed.Add(1)
		return conn.WriteInternalErr(fmt.Sprintf("failed to forward command: %v", err))
	}

	sp.commandsHandled.Add(1)
	return nil
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
	case protocol.ResponseChunk:
		return conn.WriteChunk(resp.Data)
	case protocol.ResponseEnd:
		return conn.WriteEnd()
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
	rt := r.routes.Load()

	for k, v := range rt.exact {
		routes[k] = v + " (exact)"
	}
	for k, v := range rt.prefix {
		routes[k+" *"] = v + " (prefix)"
	}

	return routes
}

// start starts the subprocess. This is the user-initiated entry point: it clears
// any prior stop marker (the user is explicitly (re)starting, re-arming
// auto-restart) and then runs the shared startCore.
func (sp *ManagedSubprocess) start(ctx context.Context) error {
	sp.stopped.Store(false)
	return sp.startCore(ctx)
}

// startCore performs the actual connect + health-loop setup. It is shared by the
// user path (start) and the auto-restart path (doRestart). It does NOT clear the
// stop marker, and it re-checks it after winning the CAS so a stop()/Unregister
// that raced an in-flight restart cannot be overridden into a live subprocess.
func (sp *ManagedSubprocess) startCore(ctx context.Context) error {
	// CAS into Starting so two concurrent start() calls cannot both proceed
	// (which would double-connect, leak fds, and run dual health loops). The
	// load-then-store pattern this replaces violated the CAS mandate.
	for {
		state := sp.state.Load().(ManagedSubprocessState)
		// Reject Stopping too: a start() that proceeds from Stopping would install
		// a fresh lifecycle/conn onto the same wg an in-flight stop() is blocked on,
		// so stop() then stamps Stopped over a live Running subprocess (dangling
		// connection + health loop reporting stopped). Back-to-back STOP/START
		// triggered it. The caller must wait for stop() to finish.
		if state == SubprocessRunning || state == SubprocessStarting || state == SubprocessStopping {
			return fmt.Errorf("subprocess busy: %s", state)
		}
		if sp.state.CompareAndSwap(state, SubprocessStarting) {
			break
		}
	}

	// Honor a stop()/Unregister that set the marker after the caller's last check
	// (the doRestart resurrection window). The user path cleared it in start(), so
	// this only fires for a genuine concurrent stop.
	if sp.stopped.Load() {
		sp.state.Store(SubprocessStopped)
		now := time.Now()
		sp.stateChanged.Store(&now)
		return fmt.Errorf("start aborted: subprocess stopped")
	}

	// Establish a fresh lifecycle context. A prior stop() or restart cancels the
	// old one; without recreating it here a stopped subprocess could never be
	// started again (its exec/health goroutines would exit immediately). Safe
	// because only the CAS winner reaches here.
	sp.newLifecycle()

	// Re-check stopped AFTER installing the new lifecycle. An Unregister/stop that
	// set stopped and cancelled between the check above and newLifecycle would
	// otherwise have cancelled the OLD context while this fresh one stays live,
	// leaking the health loop of an unregistered subprocess. If it fired, cancel
	// the context we just installed and abort before connecting.
	if sp.stopped.Load() {
		sp.cancelLifecycle()
		sp.state.Store(SubprocessStopped)
		now := time.Now()
		sp.stateChanged.Store(&now)
		return fmt.Errorf("start aborted: subprocess stopped")
	}

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
	if sp.stopped.Load() || sp.currentCtx().Err() != nil {
		sp.connMu.Lock()
		if sp.conn != nil {
			_ = sp.conn.Close()
			sp.conn = nil
		}
		sp.connMu.Unlock()
		sp.cancelLifecycle()
		sp.state.Store(SubprocessStopped)
		now = time.Now()
		sp.stateChanged.Store(&now)
		return fmt.Errorf("start aborted: subprocess stopped")
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
		parser:  protocol.NewParser(conn),
		writer:  protocol.NewWriter(conn),
		closer:  conn.Close,
		rd:      conn,
		timeout: timeout,
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
		parser:  protocol.NewParser(conn),
		writer:  protocol.NewWriter(conn),
		closer:  conn.Close,
		rd:      conn,
		timeout: timeout,
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
	cmd := exec.CommandContext(sp.currentCtx(), sp.Transport.Command, sp.Transport.Args...)

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

	// stdout/stderr pipes are *os.File, which supports read deadlines — wire it so
	// a hung stdio child gets the same deadline protection as socket transports.
	var rd interface{ SetReadDeadline(time.Time) error }
	if f, ok := stdout.(interface{ SetReadDeadline(time.Time) error }); ok {
		rd = f
	}
	timeout := sp.Transport.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	// Create subprocess connection
	sp.connMu.Lock()
	sp.conn = &SubprocessConn{
		parser:  protocol.NewParser(stdout),
		writer:  protocol.NewWriter(stdin),
		rd:      rd,
		timeout: timeout,
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
			// A crashed stdio child must be resurrected here: doHealthCheck
			// early-returns for non-Running state, so nothing else would ever
			// trigger auto-restart for it.
			sp.triggerAutoRestart()
			return
		}
		if err == nil && sp.state.Load() == SubprocessRunning {
			sp.state.Store(SubprocessStopped)
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
	if state != SubprocessRunning && state != SubprocessStarting {
		return nil
	}

	// Mark as user-stopped before anything else so an auto-restart already in
	// flight (doRestart past its wait) sees the marker and does not resurrect us.
	sp.stopped.Store(true)

	sp.state.Store(SubprocessStopping)
	now := time.Now()
	sp.stateChanged.Store(&now)

	sp.cancelLifecycle()

	// Close connection if exists. Use Close() (which locks the conn's own mu), not
	// the raw closer: SendCommand may concurrently nil closer under that mu on a
	// read error, so calling sp.conn.closer() directly here races that write and
	// can invoke a nil func.
	sp.connMu.Lock()
	if sp.conn != nil {
		_ = sp.conn.Close()
		sp.conn = nil
	}
	sp.connMu.Unlock()

	// Wait for goroutines. The background waiter stamps the terminal Stopped state
	// once they drain, so the subprocess converges to Stopped even if the caller's
	// ctx expires first. Without this, a ctx-cancelled stop() returned with state
	// stuck at Stopping — and startCore now rejects Stopping, permanently bricking
	// any future restart (e.g. a StopAll with a deadline on a slow-draining child).
	sp.healthy.Store(false)
	done := make(chan struct{})
	go func() {
		sp.wg.Wait()
		sp.state.Store(SubprocessStopped)
		stamp := time.Now()
		sp.stateChanged.Store(&stamp)
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Goroutines still draining; the background waiter will stamp Stopped when
		// they finish. Report the caller's cancellation.
		return ctx.Err()
	}
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
		case <-sp.currentCtx().Done():
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

	// A routed command in flight demonstrates the subprocess is alive; the PING
	// would only queue behind it on mu and time out on contention. Skip this tick
	// without counting a failure so a busy subprocess is not killed mid-work.
	if conn.busy() {
		return
	}

	// Create timeout context for health check
	timeout := sp.HealthCheck.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(sp.currentCtx(), timeout)
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

	// Single-flight: further failing ticks are ignored until the in-flight restart
	// resolves. Without this, Interval < RestartWait spawns a storm of concurrent
	// doRestart goroutines that inflate restartCount and exhaust the budget.
	if !sp.restarting.CompareAndSwap(false, true) {
		return
	}

	// Schedule restart in a detached goroutine. It is intentionally NOT tracked by
	// sp.wg: doRestart waits on sp.wg for the current health/monitor goroutines to
	// drain, and tracking itself in the same WaitGroup would deadlock that wait.
	go sp.doRestart()
}

// doRestart tears down the current subprocess instance and starts a fresh one,
// retrying on failure so a single failed attempt does not permanently brick the
// subprocess (a failed start starts no health loop, so nothing else retriggers).
func (sp *ManagedSubprocess) doRestart() {
	defer sp.restarting.Store(false)

	for {
		// Wait for the restart delay, aborting if a shutdown was requested. After a
		// failed attempt start() installs a fresh (live) context, so this select
		// only unblocks early on a real stop()/Unregister.
		restartWait := sp.RestartWait
		if restartWait <= 0 {
			restartWait = time.Second
		}
		select {
		case <-sp.currentCtx().Done():
			return
		case <-time.After(restartWait):
		}

		// Abort if the user deliberately stopped the subprocess while this restart
		// was waiting — resurrecting it would fight an explicit stop().
		if sp.stopped.Load() {
			return
		}

		// Cancel the old context so the health loop and any monitored process stop,
		// then wait for those goroutines to exit before reconnecting.
		sp.cancelLifecycle()
		sp.wg.Wait()

		// Close the stale connection.
		sp.connMu.Lock()
		if sp.conn != nil {
			sp.conn.Close()
			sp.conn = nil
		}
		sp.connMu.Unlock()

		sp.restartCount.Add(1)
		sp.consecutiveFails.Store(0)
		sp.healthy.Store(false)

		// Re-check after the drain: a stop()/Unregister could have landed during
		// wg.Wait(). Bail before touching state so we don't resurrect it.
		if sp.stopped.Load() {
			return
		}

		// Reset state so startCore proceeds — it refuses to start from Running/Starting.
		sp.state.Store(SubprocessStopped)
		now := time.Now()
		sp.stateChanged.Store(&now)

		// Background-derived timeout; the just-cancelled lifecycle ctx must not be used.
		// Use startCore (not start) so the stop marker is NOT cleared and startCore's
		// post-CAS stopped re-check still aborts a resurrection that raced this far.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := sp.startCore(ctx)
		cancel()
		if err == nil {
			return // healthy again; a fresh health loop is running
		}

		// startCore aborted because a stop()/Unregister landed: don't stamp Failed
		// over the stopping subprocess, just exit.
		if sp.stopped.Load() {
			return
		}

		sp.state.Store(SubprocessFailed)
		now = time.Now()
		sp.stateChanged.Store(&now)

		// Give up once the restart budget is exhausted; otherwise loop and retry
		// after RestartWait.
		if sp.MaxRestarts > 0 && int(sp.restartCount.Load()) >= sp.MaxRestarts {
			return
		}
	}
}
