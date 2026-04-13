package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Transport defines the interface for communicating with Claude.
type Transport interface {
	// Connect establishes a connection and starts the session.
	Connect(ctx context.Context) error

	// Send sends a message to Claude.
	Send(msg []byte) error

	// Receive returns a channel for receiving messages.
	Receive() <-chan TransportMessage

	// Close closes the transport.
	Close() error

	// IsConnected returns whether the transport is connected.
	IsConnected() bool
}

// TransportMessage wraps a message from the transport.
type TransportMessage struct {
	Data  json.RawMessage
	Error error
}

// SubprocessTransport communicates with Claude via subprocess using pipes.
type SubprocessTransport struct {
	cliPath    string
	args       []string
	env        []string
	workingDir string

	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	messages chan TransportMessage

	connected bool
	mu        sync.RWMutex
	writeLock sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
}

// SubprocessTransportOptions configures a subprocess transport.
type SubprocessTransportOptions struct {
	CLIPath      string
	Args         []string
	Env          map[string]string // Legacy: use AgentOptions instead
	EnvList      []string          // Pre-built environment list (takes precedence)
	AgentOptions *AgentOptions     // Full options for environment building
	WorkingDir   string
	BufferSize   int
	// UsePipes disables PTY mode and uses standard pipes for stdin/stdout/stderr.
	// This is useful for non-interactive JSON communication where PTY echo
	// and line discipline features are not needed.
	UsePipes bool
}

// NewSubprocessTransport creates a new subprocess transport.
func NewSubprocessTransport(opts *SubprocessTransportOptions) (*SubprocessTransport, error) {
	if opts == nil {
		opts = &SubprocessTransportOptions{}
	}

	cliPath := opts.CLIPath
	if cliPath == "" {
		var err error
		cliPath, err = FindCLI()
		if err != nil {
			return nil, err
		}
	}

	// Verify working directory exists
	if opts.WorkingDir != "" {
		if _, err := os.Stat(opts.WorkingDir); os.IsNotExist(err) {
			return nil, NewCLIConnectionError(fmt.Sprintf("working directory does not exist: %s", opts.WorkingDir), err)
		}
	}

	// Build environment (priority: EnvList > AgentOptions > Env map)
	var env []string
	if len(opts.EnvList) > 0 {
		env = opts.EnvList
	} else if opts.AgentOptions != nil {
		env = BuildCLIEnv(opts.AgentOptions)
	} else {
		env = os.Environ()
		for k, v := range opts.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		// Set SDK entrypoint identifier
		env = append(env, "CLAUDE_CODE_ENTRYPOINT=sdk-go")
	}

	bufferSize := opts.BufferSize
	if bufferSize <= 0 {
		bufferSize = 100
	}

	return &SubprocessTransport{
		cliPath:    cliPath,
		args:       opts.Args,
		env:        env,
		workingDir: opts.WorkingDir,
		messages:   make(chan TransportMessage, bufferSize),
	}, nil
}

// Connect starts the subprocess and establishes communication.
// Uses standard pipes for stdin/stdout/stderr for clean JSON communication.
func (t *SubprocessTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.connected {
		return nil
	}

	t.ctx, t.cancel = context.WithCancel(ctx)

	t.cmd = exec.CommandContext(t.ctx, t.cliPath, t.args...)
	t.cmd.Env = t.env
	if t.workingDir != "" {
		t.cmd.Dir = t.workingDir
	}

	// Use standard pipes for clean JSON communication (no PTY echo/line discipline)
	var err error
	t.stdin, err = t.cmd.StdinPipe()
	if err != nil {
		return NewCLIConnectionError("failed to create stdin pipe", err)
	}

	t.stdout, err = t.cmd.StdoutPipe()
	if err != nil {
		return NewCLIConnectionError("failed to create stdout pipe", err)
	}

	t.stderr, err = t.cmd.StderrPipe()
	if err != nil {
		return NewCLIConnectionError("failed to create stderr pipe", err)
	}

	// Start the command
	if err := t.cmd.Start(); err != nil {
		return NewCLIConnectionError("failed to start CLI", err)
	}

	t.connected = true

	// Start reading output from stdout
	go t.readOutput()
	// Start reading stderr for error reporting
	go t.readStderr()

	return nil
}

// Send writes a message to stdin.
func (t *SubprocessTransport) Send(msg []byte) error {
	t.mu.RLock()
	connected := t.connected
	t.mu.RUnlock()

	if !connected {
		return NewCLIConnectionError("transport not connected", nil)
	}

	t.writeLock.Lock()
	defer t.writeLock.Unlock()

	// Append newline if not present
	if len(msg) > 0 && msg[len(msg)-1] != '\n' {
		msg = append(msg, '\n')
	}

	_, err := t.stdin.Write(msg)
	if err != nil {
		return NewCLIConnectionError("failed to write to stdin", err)
	}

	return nil
}

// Receive returns the message channel.
func (t *SubprocessTransport) Receive() <-chan TransportMessage {
	return t.messages
}

// Close terminates the subprocess.
func (t *SubprocessTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.connected {
		return nil
	}

	t.connected = false
	if t.cancel != nil {
		t.cancel()
	}

	// Close stdin to signal EOF to the subprocess
	if t.stdin != nil {
		t.stdin.Close()
	}

	if t.cmd != nil && t.cmd.Process != nil {
		t.cmd.Process.Kill()
		t.cmd.Wait()
	}

	return nil
}

// IsConnected returns the connection status.
func (t *SubprocessTransport) IsConnected() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.connected
}

// IsPTYMode returns true if the transport is using PTY mode.
// Note: This transport always uses pipes, so this returns false.
func (t *SubprocessTransport) IsPTYMode() bool {
	return false
}

// SignalInputComplete signals that no more input will be sent by closing stdin.
// This allows the subprocess to detect EOF and complete processing.
func (t *SubprocessTransport) SignalInputComplete() error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.stdin != nil {
		return t.stdin.Close()
	}
	return nil
}

func (t *SubprocessTransport) readOutput() {
	defer func() {
		close(t.messages)
	}()

	scanner := bufio.NewScanner(t.stdout)
	// Increase scanner buffer for large JSON responses
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max

	for scanner.Scan() {
		select {
		case <-t.ctx.Done():
			return
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Make a copy since scanner reuses the buffer
		data := make([]byte, len(line))
		copy(data, line)

		t.messages <- TransportMessage{Data: json.RawMessage(data)}
	}

	if err := scanner.Err(); err != nil {
		t.messages <- TransportMessage{Error: NewCLIConnectionError("stdout read error", err)}
	}
}

func (t *SubprocessTransport) readStderr() {
	scanner := bufio.NewScanner(t.stderr)
	var stderrBuf strings.Builder

	for scanner.Scan() {
		select {
		case <-t.ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if line != "" {
			stderrBuf.WriteString(line)
			stderrBuf.WriteString("\n")
		}
	}

	// If there's stderr output and process has exited, report it
	if stderrBuf.Len() > 0 {
		t.mu.RLock()
		connected := t.connected
		t.mu.RUnlock()

		if !connected {
			return
		}

		// Try to get exit code
		exitCode := 0
		if t.cmd.ProcessState != nil {
			exitCode = t.cmd.ProcessState.ExitCode()
		}

		if exitCode != 0 {
			t.messages <- TransportMessage{
				Error: NewProcessError("CLI process failed", exitCode, stderrBuf.String(), nil),
			}
		}
	}
}

// FindCLI searches for the Claude CLI in standard locations.
func FindCLI() (string, error) {
	// Search order:
	// 1. CLAUDE_CLI_PATH environment variable
	// 2. Current directory
	// 3. System PATH
	// 4. Common installation locations

	// Check environment variable
	if path := os.Getenv("CLAUDE_CLI_PATH"); path != "" {
		if isExecutable(path) {
			return path, nil
		}
	}

	// Check current directory
	if cwd, err := os.Getwd(); err == nil {
		localPath := filepath.Join(cwd, cliName())
		if isExecutable(localPath) {
			return localPath, nil
		}
	}

	// Check system PATH
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}

	// Check common installation locations
	searchPaths := getCommonCLIPaths()
	for _, path := range searchPaths {
		if isExecutable(path) {
			return path, nil
		}
	}

	return "", NewCLINotFoundError(
		"Claude Code CLI not found. Install with: npm install -g @anthropic-ai/claude-code",
		strings.Join(searchPaths, ", "),
	)
}

func cliName() string {
	if runtime.GOOS == "windows" {
		return "claude.exe"
	}
	return "claude"
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return !info.IsDir()
	}
	return info.Mode()&0111 != 0
}

func getCommonCLIPaths() []string {
	home, _ := os.UserHomeDir()
	name := cliName()

	var paths []string

	switch runtime.GOOS {
	case "darwin", "linux":
		paths = []string{
			"/usr/local/bin/" + name,
			"/usr/bin/" + name,
			filepath.Join(home, ".local/bin", name),
			filepath.Join(home, ".npm-global/bin", name),
			"/opt/homebrew/bin/" + name,
		}

		// Add npm global paths
		if npmPrefix, err := exec.Command("npm", "config", "get", "prefix").Output(); err == nil {
			prefix := strings.TrimSpace(string(npmPrefix))
			paths = append(paths, filepath.Join(prefix, "bin", name))
		}

		// Add yarn global path
		if yarnPrefix, err := exec.Command("yarn", "global", "dir").Output(); err == nil {
			prefix := strings.TrimSpace(string(yarnPrefix))
			paths = append(paths, filepath.Join(prefix, "node_modules/.bin", name))
		}

	case "windows":
		paths = []string{
			filepath.Join(home, "AppData\\Roaming\\npm", name),
			filepath.Join(home, "AppData\\Local\\Yarn\\bin", name),
		}
	}

	return paths
}

// BuildCLIArgs builds command-line arguments for the Claude CLI.
func BuildCLIArgs(opts *AgentOptions) []string {
	var args []string

	// Output format for structured messages
	outputFormat := opts.OutputFormat
	if outputFormat == "" {
		outputFormat = "stream-json"
	}
	args = append(args, "--output-format", outputFormat)

	// Permission handling
	if opts.PermissionMode != "" {
		switch opts.PermissionMode {
		case PermissionModeBypassPermission:
			args = append(args, "--dangerously-skip-permissions")
		default:
			args = append(args, "--permission-mode", string(opts.PermissionMode))
		}
	}

	// Model
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	// System prompt
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}

	// Tools
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if len(opts.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
	}

	// Resource limits
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	if opts.MaxTokens > 0 {
		args = append(args, "--max-tokens", fmt.Sprintf("%d", opts.MaxTokens))
	}
	if opts.MaxThinkingTokens > 0 {
		args = append(args, "--max-thinking-tokens", fmt.Sprintf("%d", opts.MaxThinkingTokens))
	}

	// Session management
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	if opts.ContinueConversation {
		args = append(args, "--continue")
	}

	// Directories
	for _, dir := range opts.AddDirectories {
		args = append(args, "--add-dir", dir)
	}

	// MCP configuration
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}

	// Sandbox
	if opts.Sandbox != nil && opts.Sandbox.Enabled {
		args = append(args, "--sandbox")
	}

	// Debug/verbose
	// Note: --verbose is required for stream-json output format
	if opts.Debug {
		args = append(args, "--debug")
	}
	if opts.Verbose || outputFormat == "stream-json" {
		args = append(args, "--verbose")
	}

	return args
}

// BuildCLIEnv builds environment variables for the Claude CLI subprocess.
// This merges the provided options with the current process environment.
func BuildCLIEnv(opts *AgentOptions) []string {
	env := os.Environ()

	// Helper to add environment variable
	addEnv := func(key, value string) {
		if value != "" {
			env = append(env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// SDK entrypoint identifier
	addEnv(EnvClaudeCodeEntrypoint, "sdk-go")

	// API Authentication
	addEnv(EnvAnthropicAPIKey, opts.APIKey)
	addEnv(EnvClaudeAccessToken, opts.AccessToken)

	// API Endpoint
	addEnv(EnvAnthropicBaseURL, opts.BaseURL)

	// Provider selection
	switch opts.Provider {
	case APIProviderBedrock:
		addEnv(EnvClaudeCodeUseBedrock, "1")
	case APIProviderVertex:
		addEnv(EnvClaudeCodeUseVertex, "1")
	}

	// Model tiers
	addEnv(EnvClaudeSmallFastModel, opts.SmallFastModel)
	addEnv(EnvClaudeBigModel, opts.BigModel)

	// Bedrock configuration
	if opts.Bedrock != nil {
		addEnv(EnvAWSRegion, opts.Bedrock.Region)
		addEnv(EnvBedrockEndpointURL, opts.Bedrock.EndpointURL)
		addEnv(EnvAWSAccessKeyID, opts.Bedrock.AccessKeyID)
		addEnv(EnvAWSSecretAccessKey, opts.Bedrock.SecretAccessKey)
		addEnv(EnvAWSSessionToken, opts.Bedrock.SessionToken)
		addEnv(EnvAWSProfile, opts.Bedrock.Profile)
		if opts.Bedrock.CrossRegion {
			addEnv(EnvBedrockCrossRegion, "1")
		}
		if opts.Bedrock.PromptCaching {
			addEnv(EnvBedrockPromptCaching, "1")
		}
	}

	// Vertex configuration
	if opts.Vertex != nil {
		addEnv(EnvVertexProject, opts.Vertex.ProjectID)
		addEnv(EnvVertexRegion, opts.Vertex.Region)
	}

	// Proxy configuration
	if opts.Proxy != nil {
		addEnv(EnvHTTPProxy, opts.Proxy.HTTPProxy)
		addEnv(EnvHTTPSProxy, opts.Proxy.HTTPSProxy)
		addEnv(EnvNoProxy, opts.Proxy.NoProxy)
	}

	// Behavior flags
	if opts.NoTelemetry {
		addEnv(EnvClaudeCodeNoTelemetry, "1")
	}
	if opts.SkipOAuth {
		addEnv(EnvClaudeCodeSkipOAuth, "1")
	}
	if opts.Debug {
		addEnv(EnvClaudeCodeDebug, "1")
	}

	// Custom environment variables from options
	for k, v := range opts.Environment {
		addEnv(k, v)
	}

	return env
}

// StreamParser parses JSON stream messages from the CLI.
type StreamParser struct {
	messages chan MessageType
	errors   chan error
}

// NewStreamParser creates a new stream parser.
func NewStreamParser() *StreamParser {
	return &StreamParser{
		messages: make(chan MessageType, 100),
		errors:   make(chan error, 10),
	}
}

// Parse processes transport messages into typed messages.
func (p *StreamParser) Parse(ctx context.Context, transport Transport) {
	defer func() {
		close(p.messages)
		close(p.errors)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-transport.Receive():
			if !ok {
				return
			}

			if msg.Error != nil {
				p.errors <- msg.Error
				continue
			}

			parsed, err := ParseMessage(msg.Data)
			if err != nil {
				p.errors <- NewCLIJSONDecodeError("failed to parse message", string(msg.Data), err)
				continue
			}

			p.messages <- parsed
		}
	}
}

// Messages returns the parsed message channel.
func (p *StreamParser) Messages() <-chan MessageType {
	return p.messages
}

// Errors returns the error channel.
func (p *StreamParser) Errors() <-chan error {
	return p.errors
}
