package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// SandboxNetworkConfig configures network access in sandbox mode.
type SandboxNetworkConfig struct {
	UnixSockets  []string `json:"unix_sockets,omitempty"`
	LocalBinding bool     `json:"local_binding,omitempty"`
	ProxyPorts   []int    `json:"proxy_ports,omitempty"`
}

// SandboxIgnoreViolations configures violations to ignore in sandbox mode.
type SandboxIgnoreViolations struct {
	FilePaths    []string `json:"file_paths,omitempty"`
	NetworkHosts []string `json:"network_hosts,omitempty"`
}

// SandboxSettings configures sandbox behavior.
type SandboxSettings struct {
	Enabled          bool                     `json:"enabled,omitempty"`
	AutoApprove      bool                     `json:"auto_approve,omitempty"`
	ExcludedCommands []string                 `json:"excluded_commands,omitempty"`
	Network          *SandboxNetworkConfig    `json:"network,omitempty"`
	IgnoreViolations *SandboxIgnoreViolations `json:"ignore_violations,omitempty"`
}

// AgentDefinition defines a custom agent.
type AgentDefinition struct {
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools,omitempty"`
	Model       string   `json:"model,omitempty"`
}

// SDKPluginConfig defines a local SDK plugin.
type SDKPluginConfig struct {
	Type     string `json:"type"` // Always "local"
	FilePath string `json:"file_path"`
}

// AgentOptions provides comprehensive configuration for Claude agents.
// This is the Go equivalent of Python's ClaudeAgentOptions.
type AgentOptions struct {
	// API Authentication
	APIKey      string `json:"api_key,omitempty"`
	AccessToken string `json:"access_token,omitempty"`

	// API Endpoint Configuration
	BaseURL string `json:"base_url,omitempty"`

	// Provider Selection (anthropic, bedrock, vertex)
	Provider APIProvider `json:"provider,omitempty"`

	// Model configuration
	Model             string `json:"model,omitempty"`
	SmallFastModel    string `json:"small_fast_model,omitempty"` // Haiku-class model
	BigModel          string `json:"big_model,omitempty"`        // Opus-class model
	MaxThinkingTokens int    `json:"max_thinking_tokens,omitempty"`
	MaxTokens         int    `json:"max_tokens,omitempty"` // Max output tokens

	// System prompt
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Tool configuration
	AllowedTools    []string `json:"allowed_tools,omitempty"`
	DisallowedTools []string `json:"disallowed_tools,omitempty"`

	// Permission handling
	PermissionMode PermissionMode `json:"permission_mode,omitempty"`

	// Resource limits
	MaxTurns     int     `json:"max_turns,omitempty"`
	MaxBudgetUSD float64 `json:"max_budget_usd,omitempty"`
	TimeoutSecs  int     `json:"timeout_secs,omitempty"`

	// Session management
	Resume               string `json:"resume,omitempty"`
	ContinueConversation bool   `json:"continue_conversation,omitempty"`
	ForkSession          string `json:"fork_session,omitempty"`

	// MCP servers
	MCPServers    MCPServers `json:"-"`
	MCPConfigPath string     `json:"mcp_config,omitempty"`

	// Hooks
	Hooks *HookRegistry `json:"-"`

	// Plugins
	Plugins map[string]SDKPluginConfig `json:"plugins,omitempty"`

	// Agent definitions
	Agents map[string]AgentDefinition `json:"agents,omitempty"`

	// Sandbox settings
	Sandbox *SandboxSettings `json:"sandbox,omitempty"`

	// File management
	FileCheckpoints  bool     `json:"file_checkpoints,omitempty"`
	AddDirectories   []string `json:"add_directories,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`

	// AWS Bedrock Configuration
	Bedrock *BedrockConfig `json:"bedrock,omitempty"`

	// Google Vertex Configuration
	Vertex *VertexConfig `json:"vertex,omitempty"`

	// Proxy Configuration
	Proxy *ProxyConfig `json:"proxy,omitempty"`

	// Environment variables (passed to CLI subprocess)
	Environment map[string]string `json:"environment,omitempty"`

	// CLI configuration
	CLIPath string `json:"cli_path,omitempty"`

	// Behavior flags
	Debug       bool `json:"debug,omitempty"`
	Verbose     bool `json:"verbose,omitempty"`
	NoTelemetry bool `json:"no_telemetry,omitempty"`
	SkipOAuth   bool `json:"skip_oauth,omitempty"`

	// Output configuration
	OutputFormat string `json:"output_format,omitempty"` // text, json, stream-json

	// Legacy: Interactive mode (for backward compatibility)
	Interactive bool `json:"interactive,omitempty"`
}

// Options is an alias for backward compatibility.
// Deprecated: Use AgentOptions instead.
type Options = AgentOptions

// Client manages Claude sessions and queries.
type Client struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	defaultOpts *AgentOptions
	mcpManager  *MCPServerManager
}

// Session represents an active Claude CLI session.
type Session struct {
	ID       string
	client   *Client
	ctx      context.Context
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	pty      *os.File
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	options  *AgentOptions
	messages chan *Message
	errors   chan error
	closed   bool
	mu       sync.RWMutex
}

// Message represents a streaming message from Claude.
// For structured messages, use the typed message types (AssistantMessage, etc.)
type Message struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	Role      string          `json:"role,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// QueryRequest defines the input for a query.
type QueryRequest struct {
	Prompt    string        `json:"prompt"`
	Options   *AgentOptions `json:"options,omitempty"`
	SessionID string        `json:"session_id,omitempty"`
}

// QueryResponse provides channels for receiving query results.
type QueryResponse struct {
	SessionID string        `json:"session_id"`
	Messages  chan *Message `json:"-"`
	Errors    chan error    `json:"-"`
}

// New creates a new Client with the given options.
func New(opts *AgentOptions) *Client {
	if opts == nil {
		opts = &AgentOptions{
			PermissionMode: PermissionModeBypassPermission,
			Debug:          false,
			Verbose:        false,
			Interactive:    true,
		}
	}

	client := &Client{
		sessions:    make(map[string]*Session),
		defaultOpts: opts,
		mcpManager:  NewMCPServerManager(),
	}

	// Register MCP servers from options
	if opts.MCPServers != nil {
		for name, config := range opts.MCPServers {
			client.mcpManager.Register(name, config)
		}
	}

	return client
}

// RegisterMCPServer registers an MCP server with the client.
func (c *Client) RegisterMCPServer(name string, config MCPServerConfig) {
	c.mcpManager.Register(name, config)
}

// RegisterSDKMCPServer registers an in-process MCP server.
func (c *Client) RegisterSDKMCPServer(name string, server *SDKMCPServer) {
	c.mcpManager.RegisterSDKServer(name, server)
}

// Query sends a prompt to Claude and returns a response channel.
func (c *Client) Query(ctx context.Context, req *QueryRequest) (*QueryResponse, error) {
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	opts := c.mergeOptions(req.Options)
	session, err := c.createSession(ctx, sessionID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	go session.handleIO()

	// Give Claude a moment to start up before sending the prompt
	time.Sleep(100 * time.Millisecond)

	if err := session.sendPrompt(req.Prompt); err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to send prompt: %w", err)
	}

	return &QueryResponse{
		SessionID: sessionID,
		Messages:  session.messages,
		Errors:    session.errors,
	}, nil
}

// GetSession retrieves an active session by ID.
func (c *Client) GetSession(sessionID string) (*Session, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	session, exists := c.sessions[sessionID]
	return session, exists
}

// CloseSession closes a specific session.
func (c *Client) CloseSession(sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if session, exists := c.sessions[sessionID]; exists {
		session.Close()
		delete(c.sessions, sessionID)
		return nil
	}

	return fmt.Errorf("session %s not found", sessionID)
}

// Close closes all sessions and cleans up resources.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for sessionID, session := range c.sessions {
		session.Close()
		delete(c.sessions, sessionID)
	}

	return nil
}

func (c *Client) createSession(ctx context.Context, sessionID string, opts *AgentOptions) (*Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	args := c.buildArgs(opts)

	// Add session ID for interactive mode
	if opts.Interactive {
		args = append(args, "--session-id", sessionID)
	}

	// Find CLI
	cliPath := opts.CLIPath
	if cliPath == "" {
		var err error
		cliPath, err = FindCLI()
		if err != nil {
			cancel()
			return nil, err
		}
	}

	cmd := exec.CommandContext(sessionCtx, cliPath, args...)

	if opts.WorkingDirectory != "" {
		cmd.Dir = opts.WorkingDirectory
	}

	env := cmd.Environ()
	env = append(env, "CLAUDE_CODE_ENTRYPOINT=sdk-go")

	// Pass Options fields as environment variables for the CLI
	if opts.BaseURL != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_BASE_URL=%s", opts.BaseURL))
	}
	if opts.APIKey != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_API_KEY=%s", opts.APIKey))
	}
	if opts.AccessToken != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_AUTH_TOKEN=%s", opts.AccessToken))
	}
	if opts.Model != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_MODEL=%s", opts.Model))
	}
	if opts.SmallFastModel != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_SMALL_FAST_MODEL=%s", opts.SmallFastModel))
	}
	if opts.BigModel != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_DEFAULT_OPUS_MODEL=%s", opts.BigModel))
	}

	// Custom environment variables override
	if opts.Environment != nil {
		for k, v := range opts.Environment {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
	}
	cmd.Env = env

	// Start the command with a PTY for interactive sessions
	ptyFile, err := pty.Start(cmd)
	if err != nil {
		cancel()
		return nil, NewCLIConnectionError("failed to start CLI with PTY", err)
	}

	// Set PTY size to mimic a real terminal
	pty.Setsize(ptyFile, &pty.Winsize{
		Rows: 24,
		Cols: 80,
	})

	// PTY combines stdin/stdout/stderr
	session := &Session{
		ID:       sessionID,
		client:   c,
		ctx:      sessionCtx,
		cancel:   cancel,
		cmd:      cmd,
		pty:      ptyFile,
		stdin:    ptyFile,
		stdout:   ptyFile,
		stderr:   nil, // PTY combines stderr with stdout
		options:  opts,
		messages: make(chan *Message, 100),
		errors:   make(chan error, 10),
		closed:   false,
	}

	c.mu.Lock()
	c.sessions[sessionID] = session
	c.mu.Unlock()

	return session, nil
}

func (c *Client) mergeOptions(opts *AgentOptions) *AgentOptions {
	merged := &AgentOptions{}

	if c.defaultOpts != nil {
		*merged = *c.defaultOpts
	}

	if opts != nil {
		// API Authentication
		if opts.APIKey != "" {
			merged.APIKey = opts.APIKey
		}
		if opts.AccessToken != "" {
			merged.AccessToken = opts.AccessToken
		}
		if opts.BaseURL != "" {
			merged.BaseURL = opts.BaseURL
		}
		if opts.Provider != "" {
			merged.Provider = opts.Provider
		}

		// Model configuration
		if opts.Model != "" {
			merged.Model = opts.Model
		}
		if opts.SmallFastModel != "" {
			merged.SmallFastModel = opts.SmallFastModel
		}
		if opts.BigModel != "" {
			merged.BigModel = opts.BigModel
		}
		if opts.MaxTurns > 0 {
			merged.MaxTurns = opts.MaxTurns
		}
		if opts.MaxThinkingTokens > 0 {
			merged.MaxThinkingTokens = opts.MaxThinkingTokens
		}
		if opts.MaxTokens > 0 {
			merged.MaxTokens = opts.MaxTokens
		}
		if opts.MaxBudgetUSD > 0 {
			merged.MaxBudgetUSD = opts.MaxBudgetUSD
		}
		if opts.TimeoutSecs > 0 {
			merged.TimeoutSecs = opts.TimeoutSecs
		}
		if opts.Debug {
			merged.Debug = opts.Debug
		}
		if opts.Verbose {
			merged.Verbose = opts.Verbose
		}
		if opts.NoTelemetry {
			merged.NoTelemetry = opts.NoTelemetry
		}
		if opts.SkipOAuth {
			merged.SkipOAuth = opts.SkipOAuth
		}
		if opts.PermissionMode != "" {
			merged.PermissionMode = opts.PermissionMode
		}
		if len(opts.AllowedTools) > 0 {
			merged.AllowedTools = opts.AllowedTools
		}
		if len(opts.DisallowedTools) > 0 {
			merged.DisallowedTools = opts.DisallowedTools
		}
		if opts.SystemPrompt != "" {
			merged.SystemPrompt = opts.SystemPrompt
		}
		if opts.MCPConfigPath != "" {
			merged.MCPConfigPath = opts.MCPConfigPath
		}
		if len(opts.AddDirectories) > 0 {
			merged.AddDirectories = opts.AddDirectories
		}
		if opts.Environment != nil {
			if merged.Environment == nil {
				merged.Environment = make(map[string]string)
			}
			for k, v := range opts.Environment {
				merged.Environment[k] = v
			}
		}
		if opts.WorkingDirectory != "" {
			merged.WorkingDirectory = opts.WorkingDirectory
		}
		if opts.CLIPath != "" {
			merged.CLIPath = opts.CLIPath
		}
		if opts.Resume != "" {
			merged.Resume = opts.Resume
		}
		if opts.ContinueConversation {
			merged.ContinueConversation = opts.ContinueConversation
		}
		if opts.ForkSession != "" {
			merged.ForkSession = opts.ForkSession
		}
		if opts.Sandbox != nil {
			merged.Sandbox = opts.Sandbox
		}
		if opts.FileCheckpoints {
			merged.FileCheckpoints = opts.FileCheckpoints
		}
		if opts.Hooks != nil {
			merged.Hooks = opts.Hooks
		}
		if opts.MCPServers != nil {
			merged.MCPServers = opts.MCPServers
		}
		if opts.Plugins != nil {
			merged.Plugins = opts.Plugins
		}
		if opts.Agents != nil {
			merged.Agents = opts.Agents
		}
		if opts.Bedrock != nil {
			merged.Bedrock = opts.Bedrock
		}
		if opts.Vertex != nil {
			merged.Vertex = opts.Vertex
		}
		if opts.Proxy != nil {
			merged.Proxy = opts.Proxy
		}
		if opts.OutputFormat != "" {
			merged.OutputFormat = opts.OutputFormat
		}
	}

	return merged
}

func (c *Client) buildArgs(opts *AgentOptions) []string {
	var args []string

	// Always use interactive mode with stdin for prompt
	args = []string{"--dangerously-skip-permissions"}

	if opts.Debug {
		args = append(args, "--debug")
	}

	if opts.Verbose {
		args = append(args, "--verbose")
	}

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	if opts.PermissionMode != "" && opts.PermissionMode != PermissionModeBypassPermission {
		args = append(args, "--permission-mode", string(opts.PermissionMode))
	}

	if len(opts.AllowedTools) > 0 {
		// Include SDK MCP tools
		tools := opts.AllowedTools
		if c.mcpManager != nil {
			tools = append(tools, c.mcpManager.AllowedToolNames()...)
		}
		args = append(args, "--allowedTools", strings.Join(tools, ","))
	}

	if len(opts.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
	}

	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}

	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}

	if len(opts.AddDirectories) > 0 {
		for _, dir := range opts.AddDirectories {
			args = append(args, "--add-dir", dir)
		}
	}

	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}

	if opts.MaxThinkingTokens > 0 {
		args = append(args, "--max-thinking-tokens", fmt.Sprintf("%d", opts.MaxThinkingTokens))
	}

	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}

	if opts.ContinueConversation {
		args = append(args, "--continue")
	}

	return args
}

func (s *Session) sendPrompt(prompt string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return NewSessionClosedError(s.ID)
	}

	_, err := s.stdin.Write([]byte(prompt + "\n"))
	if err != nil {
		return NewCLIConnectionError("failed to write prompt", err)
	}

	return nil
}

// SendMessage sends a follow-up message in the session.
func (s *Session) SendMessage(content string) error {
	return s.sendPrompt(content)
}

func (s *Session) handleIO() {
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		close(s.messages)
		close(s.errors)
	}()

	// Only start stderr handler if stderr is available (not for PTY mode)
	if s.stderr != nil {
		go s.handleStderr()
	}
	s.handleStdout()
}

func (s *Session) handleStdout() {
	scanner := bufio.NewScanner(s.stdout)
	var contentBuffer strings.Builder
	messagesSent := false

	for scanner.Scan() {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		contentBuffer.WriteString(line)
		contentBuffer.WriteString("\n")

		msg := Message{
			Type:      "content",
			Content:   line,
			Timestamp: time.Now(),
		}

		select {
		case s.messages <- &msg:
			messagesSent = true
		case <-s.ctx.Done():
			return
		}
	}

	if contentBuffer.Len() > 0 && messagesSent {
		finalMsg := Message{
			Type:      "final",
			Content:   strings.TrimSpace(contentBuffer.String()),
			Timestamp: time.Now(),
		}

		select {
		case s.messages <- &finalMsg:
		case <-s.ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		s.errors <- NewCLIConnectionError("stdout scanner error", err)
	}
}

func (s *Session) handleStderr() {
	scanner := bufio.NewScanner(s.stderr)

	for scanner.Scan() {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if line != "" {
			s.errors <- fmt.Errorf("stderr: %s", line)
		}
	}

	if err := scanner.Err(); err != nil {
		s.errors <- NewCLIConnectionError("stderr scanner error", err)
	}
}

// Close closes the session and releases resources.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	s.cancel()

	// Close PTY (which handles stdin/stdout) or just stdin if not using PTY
	if s.pty != nil {
		s.pty.Close()
	} else if s.stdin != nil {
		s.stdin.Close()
	}

	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}

	return nil
}

// Wait waits for the session to complete.
func (s *Session) Wait() error {
	if s.cmd == nil {
		return NewSessionClosedError(s.ID)
	}
	return s.cmd.Wait()
}

// Query is a simple function for one-shot queries.
// It creates a client, sends the prompt, collects all messages, and returns them.
func Query(ctx context.Context, prompt string, opts *AgentOptions) ([]MessageType, error) {
	if opts == nil {
		opts = &AgentOptions{
			PermissionMode: PermissionModeBypassPermission,
			Interactive:    false, // Non-interactive for one-shot queries
		}
	} else if !opts.Interactive {
		// Ensure non-interactive mode for one-shot queries
		optsCopy := *opts
		optsCopy.Interactive = false
		opts = &optsCopy
	}

	// Create transport
	transportOpts := &SubprocessTransportOptions{
		CLIPath:      opts.CLIPath,
		Args:         BuildCLIArgs(opts),
		AgentOptions: opts,
		WorkingDir:   opts.WorkingDirectory,
	}

	transport, err := NewSubprocessTransport(transportOpts)
	if err != nil {
		return nil, err
	}
	defer transport.Close()

	// Connect
	if err := transport.Connect(ctx); err != nil {
		return nil, err
	}

	// Send prompt
	if err := transport.Send([]byte(prompt)); err != nil {
		return nil, err
	}

	// Signal end of input - this is a no-op in PTY mode since we can't
	// half-close a PTY without also closing the read side.
	transport.SignalInputComplete()

	// Parse messages
	parser := NewStreamParser()
	go parser.Parse(ctx, transport)

	var messages []MessageType
	for {
		select {
		case <-ctx.Done():
			return messages, ctx.Err()
		case msg, ok := <-parser.Messages():
			if !ok {
				return messages, nil
			}
			messages = append(messages, msg)
			// Stop on ResultMessage
			if _, isResult := msg.(ResultMessage); isResult {
				return messages, nil
			}
		case err, ok := <-parser.Errors():
			if ok && err != nil {
				return messages, err
			}
		}
	}
}

// QueryIterator returns an iterator for streaming messages from a one-shot query.
type QueryIterator struct {
	transport *SubprocessTransport
	parser    *StreamParser
	ctx       context.Context
	cancel    context.CancelFunc
	started   bool
}

// NewQueryIterator creates a new query iterator.
func NewQueryIterator(ctx context.Context, prompt string, opts *AgentOptions) (*QueryIterator, error) {
	if opts == nil {
		opts = &AgentOptions{
			PermissionMode: PermissionModeBypassPermission,
			Interactive:    false,
		}
	}

	iterCtx, cancel := context.WithCancel(ctx)

	transportOpts := &SubprocessTransportOptions{
		CLIPath:      opts.CLIPath,
		Args:         BuildCLIArgs(opts),
		AgentOptions: opts,
		WorkingDir:   opts.WorkingDirectory,
	}

	transport, err := NewSubprocessTransport(transportOpts)
	if err != nil {
		cancel()
		return nil, err
	}

	if err := transport.Connect(iterCtx); err != nil {
		transport.Close()
		cancel()
		return nil, err
	}

	if err := transport.Send([]byte(prompt)); err != nil {
		transport.Close()
		cancel()
		return nil, err
	}

	// Signal end of input - this is a no-op in PTY mode since we can't
	// half-close a PTY without also closing the read side.
	transport.SignalInputComplete()

	parser := NewStreamParser()
	go parser.Parse(iterCtx, transport)

	return &QueryIterator{
		transport: transport,
		parser:    parser,
		ctx:       iterCtx,
		cancel:    cancel,
		started:   true,
	}, nil
}

// Next returns the next message or nil if done.
func (q *QueryIterator) Next() (MessageType, error) {
	select {
	case <-q.ctx.Done():
		return nil, q.ctx.Err()
	case msg, ok := <-q.parser.Messages():
		if !ok {
			return nil, nil
		}
		return msg, nil
	case err, ok := <-q.parser.Errors():
		if ok && err != nil {
			return nil, err
		}
		return nil, nil
	}
}

// Close closes the iterator and releases resources.
func (q *QueryIterator) Close() error {
	q.cancel()
	if q.transport != nil {
		return q.transport.Close()
	}
	return nil
}

// Messages returns a channel for receiving messages.
func (q *QueryIterator) Messages() <-chan MessageType {
	return q.parser.Messages()
}

// Errors returns a channel for receiving errors.
func (q *QueryIterator) Errors() <-chan error {
	return q.parser.Errors()
}
