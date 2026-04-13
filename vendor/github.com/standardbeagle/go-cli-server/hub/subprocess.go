// Package hub provides the core infrastructure for the go-cli-server hub.
//
// The hub manages subprocess registration, command routing, and client connections.
// It enables multiple applications (MCP servers, CLI tools, GUI apps) to share
// resources like code indexes, reverse proxies, and SSH terminals.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// SubprocessState represents the current state of a subprocess.
type SubprocessState string

const (
	SubprocessStateStarting   SubprocessState = "starting"
	SubprocessStateRunning    SubprocessState = "running"
	SubprocessStateStopping   SubprocessState = "stopping"
	SubprocessStateStopped    SubprocessState = "stopped"
	SubprocessStateFailed     SubprocessState = "failed"
	SubprocessStateRestarting SubprocessState = "restarting"
)

// SubprocessConfig defines how to start and communicate with a subprocess.
type SubprocessConfig struct {
	// ID is a unique identifier for this subprocess.
	ID string `json:"id"`

	// Name is a human-readable name.
	Name string `json:"name"`

	// Commands this subprocess handles.
	// Supports exact matches ("PROXY START") and prefixes ("PROXY").
	// Use "*" suffix for prefix matching: "PROXY *" matches all PROXY commands.
	Commands []string `json:"commands"`

	// Transport specifies how to communicate with the subprocess.
	Transport TransportConfig `json:"transport"`

	// HealthCheck configuration.
	HealthCheck HealthCheckConfig `json:"health_check"`

	// AutoRestart enables automatic restart on crash.
	AutoRestart bool `json:"auto_restart"`

	// MaxRestarts limits restart attempts (0 = unlimited).
	MaxRestarts int `json:"max_restarts"`

	// RestartDelay is the delay between restart attempts.
	RestartDelay time.Duration `json:"restart_delay"`
}

// TransportConfig specifies the subprocess communication transport.
type TransportConfig struct {
	// Type is the transport type: "unix", "tcp", "stdio", "grpc".
	Type string `json:"type"`

	// Address is the endpoint address (socket path, host:port).
	// For "stdio" type, this is ignored.
	Address string `json:"address,omitempty"`

	// Command is the executable path (for "stdio" transport).
	Command string `json:"command,omitempty"`

	// Args are command arguments (for "stdio" transport).
	Args []string `json:"args,omitempty"`

	// Env are additional environment variables (for "stdio" transport).
	Env []string `json:"env,omitempty"`
}

// HealthCheckConfig configures subprocess health checking.
type HealthCheckConfig struct {
	// Enabled turns on health checking.
	Enabled bool `json:"enabled"`

	// Interval between health checks.
	Interval time.Duration `json:"interval"`

	// Timeout for each health check.
	Timeout time.Duration `json:"timeout"`

	// FailureThreshold is the number of consecutive failures before marking unhealthy.
	FailureThreshold int `json:"failure_threshold"`
}

// DefaultHealthCheckConfig returns sensible defaults.
func DefaultHealthCheckConfig() HealthCheckConfig {
	return HealthCheckConfig{
		Enabled:          true,
		Interval:         10 * time.Second,
		Timeout:          5 * time.Second,
		FailureThreshold: 3,
	}
}

// Subprocess represents a registered subprocess.
type Subprocess struct {
	config SubprocessConfig

	// State management
	state        atomic.Value // SubprocessState
	stateChanged time.Time
	stateMu      sync.RWMutex

	// Connection to subprocess
	conn   net.Conn
	connMu sync.RWMutex

	// Statistics
	commandsHandled atomic.Int64
	commandsFailed  atomic.Int64
	lastCommand     atomic.Pointer[time.Time]
	restartCount    atomic.Int32

	// Health tracking
	healthy          atomic.Bool
	consecutiveFails atomic.Int32
	lastHealthCheck  atomic.Pointer[time.Time]

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewSubprocess creates a new subprocess instance.
func NewSubprocess(config SubprocessConfig) *Subprocess {
	ctx, cancel := context.WithCancel(context.Background())

	sp := &Subprocess{
		config: config,
		ctx:    ctx,
		cancel: cancel,
	}
	sp.state.Store(SubprocessStateStopped)
	sp.healthy.Store(false)

	return sp
}

// ID returns the subprocess ID.
func (sp *Subprocess) ID() string {
	return sp.config.ID
}

// Name returns the subprocess name.
func (sp *Subprocess) Name() string {
	return sp.config.Name
}

// Config returns the subprocess configuration.
func (sp *Subprocess) Config() SubprocessConfig {
	return sp.config
}

// State returns the current subprocess state.
func (sp *Subprocess) State() SubprocessState {
	return sp.state.Load().(SubprocessState)
}

// IsHealthy returns whether the subprocess is healthy.
func (sp *Subprocess) IsHealthy() bool {
	return sp.healthy.Load()
}

// Stats returns subprocess statistics.
func (sp *Subprocess) Stats() SubprocessStats {
	stats := SubprocessStats{
		ID:              sp.config.ID,
		Name:            sp.config.Name,
		State:           sp.State(),
		Healthy:         sp.healthy.Load(),
		CommandsHandled: sp.commandsHandled.Load(),
		CommandsFailed:  sp.commandsFailed.Load(),
		RestartCount:    int(sp.restartCount.Load()),
	}

	if t := sp.lastCommand.Load(); t != nil {
		stats.LastCommand = *t
	}
	if t := sp.lastHealthCheck.Load(); t != nil {
		stats.LastHealthCheck = *t
	}

	return stats
}

// SubprocessStats contains subprocess statistics.
type SubprocessStats struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	State           SubprocessState `json:"state"`
	Healthy         bool            `json:"healthy"`
	CommandsHandled int64           `json:"commands_handled"`
	CommandsFailed  int64           `json:"commands_failed"`
	RestartCount    int             `json:"restart_count"`
	LastCommand     time.Time       `json:"last_command,omitempty"`
	LastHealthCheck time.Time       `json:"last_health_check,omitempty"`
}

// Start starts the subprocess.
func (sp *Subprocess) Start(ctx context.Context) error {
	sp.stateMu.Lock()
	defer sp.stateMu.Unlock()

	currentState := sp.State()
	if currentState == SubprocessStateRunning || currentState == SubprocessStateStarting {
		return fmt.Errorf("subprocess %s is already %s", sp.config.ID, currentState)
	}

	sp.state.Store(SubprocessStateStarting)
	sp.stateChanged = time.Now()

	// Connect based on transport type
	var err error
	switch sp.config.Transport.Type {
	case "unix":
		err = sp.connectUnix(ctx)
	case "tcp":
		err = sp.connectTCP(ctx)
	case "stdio":
		err = sp.startStdio(ctx)
	default:
		err = fmt.Errorf("unsupported transport type: %s", sp.config.Transport.Type)
	}

	if err != nil {
		sp.state.Store(SubprocessStateFailed)
		return fmt.Errorf("failed to start subprocess %s: %w", sp.config.ID, err)
	}

	sp.state.Store(SubprocessStateRunning)
	sp.stateChanged = time.Now()
	sp.healthy.Store(true)

	// Start health check loop if enabled
	if sp.config.HealthCheck.Enabled {
		sp.wg.Add(1)
		go sp.healthCheckLoop()
	}

	return nil
}

// Stop stops the subprocess gracefully.
func (sp *Subprocess) Stop(ctx context.Context) error {
	sp.stateMu.Lock()
	defer sp.stateMu.Unlock()

	if sp.State() != SubprocessStateRunning {
		return nil
	}

	sp.state.Store(SubprocessStateStopping)
	sp.stateChanged = time.Now()

	// Cancel context to stop health check loop
	sp.cancel()

	// Close connection
	sp.connMu.Lock()
	if sp.conn != nil {
		sp.conn.Close()
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

	sp.state.Store(SubprocessStateStopped)
	sp.stateChanged = time.Now()
	sp.healthy.Store(false)

	return nil
}

// SendCommand sends a command to the subprocess and returns the response.
func (sp *Subprocess) SendCommand(ctx context.Context, cmd *Command) (*Response, error) {
	if sp.State() != SubprocessStateRunning {
		return nil, fmt.Errorf("subprocess %s is not running (state: %s)", sp.config.ID, sp.State())
	}

	sp.connMu.RLock()
	conn := sp.conn
	sp.connMu.RUnlock()

	if conn == nil {
		return nil, fmt.Errorf("subprocess %s has no connection", sp.config.ID)
	}

	// Record command time
	now := time.Now()
	sp.lastCommand.Store(&now)

	// Send command (text protocol format)
	cmdBytes, err := cmd.Marshal()
	if err != nil {
		sp.commandsFailed.Add(1)
		return nil, fmt.Errorf("failed to marshal command: %w", err)
	}

	// Set write deadline
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}

	if _, err := conn.Write(cmdBytes); err != nil {
		sp.commandsFailed.Add(1)
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	// Read response
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	}

	resp, err := ReadResponse(conn)
	if err != nil {
		sp.commandsFailed.Add(1)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	sp.commandsHandled.Add(1)
	return resp, nil
}

// connectUnix connects to a Unix socket.
func (sp *Subprocess) connectUnix(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sp.config.Transport.Address)
	if err != nil {
		return err
	}

	sp.connMu.Lock()
	sp.conn = conn
	sp.connMu.Unlock()

	return nil
}

// connectTCP connects to a TCP endpoint.
func (sp *Subprocess) connectTCP(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", sp.config.Transport.Address)
	if err != nil {
		return err
	}

	sp.connMu.Lock()
	sp.conn = conn
	sp.connMu.Unlock()

	return nil
}

// startStdio starts the subprocess via stdio.
func (sp *Subprocess) startStdio(ctx context.Context) error {
	// TODO: Implement stdio subprocess launching
	// This would exec the command and use stdin/stdout for communication
	return fmt.Errorf("stdio transport not yet implemented")
}

// healthCheckLoop periodically checks subprocess health.
func (sp *Subprocess) healthCheckLoop() {
	defer sp.wg.Done()

	interval := sp.config.HealthCheck.Interval
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
func (sp *Subprocess) doHealthCheck() {
	now := time.Now()
	sp.lastHealthCheck.Store(&now)

	timeout := sp.config.HealthCheck.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(sp.ctx, timeout)
	defer cancel()

	// Send PING command
	cmd := &Command{Verb: "PING"}
	_, err := sp.SendCommand(ctx, cmd)

	if err != nil {
		fails := sp.consecutiveFails.Add(1)
		threshold := int32(sp.config.HealthCheck.FailureThreshold)
		if threshold == 0 {
			threshold = 3
		}

		if fails >= threshold {
			sp.healthy.Store(false)
			// TODO: Trigger restart if AutoRestart is enabled
		}
	} else {
		sp.consecutiveFails.Store(0)
		sp.healthy.Store(true)
	}
}

// Command represents a command to send to a subprocess.
type Command struct {
	Verb    string
	Subverb string
	Args    []string
	Data    []byte
}

// Marshal serializes the command to the text protocol format.
func (c *Command) Marshal() ([]byte, error) {
	// Build command line: VERB [SUBVERB] [ARGS...] [LENGTH]\r\n[DATA]\r\n
	line := c.Verb
	if c.Subverb != "" {
		line += " " + c.Subverb
	}
	for _, arg := range c.Args {
		line += " " + arg
	}

	if len(c.Data) > 0 {
		line += fmt.Sprintf(" %d", len(c.Data))
		line += "\r\n"
		line += string(c.Data)
		line += "\r\n"
	} else {
		line += "\r\n"
	}

	return []byte(line), nil
}

// Response represents a response from a subprocess.
type Response struct {
	Type    string // "OK", "ERR", "JSON", "DATA"
	Message string
	Data    []byte
}

// ReadResponse reads a response from the connection.
func ReadResponse(r io.Reader) (*Response, error) {
	// TODO: Implement proper response parsing
	// For now, simplified implementation
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil {
		return nil, err
	}

	// Parse response type from first line
	data := buf[:n]
	resp := &Response{
		Data: data,
	}

	// Simple parsing: first word is response type
	if len(data) >= 2 {
		switch {
		case string(data[:2]) == "OK":
			resp.Type = "OK"
		case string(data[:3]) == "ERR":
			resp.Type = "ERR"
		case string(data[:4]) == "JSON":
			resp.Type = "JSON"
		case string(data[:4]) == "DATA":
			resp.Type = "DATA"
		default:
			resp.Type = "UNKNOWN"
		}
	}

	return resp, nil
}

// ToJSON returns the response data as parsed JSON.
func (r *Response) ToJSON() (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := json.Unmarshal(r.Data, &result); err != nil {
		return nil, err
	}
	return result, nil
}
