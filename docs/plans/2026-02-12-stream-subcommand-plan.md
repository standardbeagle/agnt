# agnt stream Subcommand Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `agnt stream claude` subcommand that acts as transparent bidirectional JSONL middleware between a client and Claude Code, with browser event injection via the existing overlay socket.

**Architecture:** Spawns `claude -p` with `--input-format stream-json --output-format stream-json` using pipes (no PTY). Three goroutines handle stdin relay, stdin writing, and stdout relay. A channel serializes client stdin and overlay events into Claude's stdin pipe. See `docs/plans/2026-02-12-stream-subcommand-design.md` for full design.

**Tech Stack:** Go 1.24.2, cobra CLI, gorilla/websocket (existing dep), Unix domain sockets

---

### Task 1: Create stream command skeleton with cobra registration

**Files:**
- Create: `cmd/agnt/stream.go`
- Modify: `cmd/agnt/main.go:55-60` (add streamCmd registration)

**Step 1: Write the failing test**

Create `cmd/agnt/stream_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamCmd_Exists(t *testing.T) {
	// Verify stream command is registered
	cmd, _, err := rootCmd.Find([]string{"stream"})
	assert.NoError(t, err)
	assert.Equal(t, "stream", cmd.Name())
}

func TestStreamCmd_RequiresSubcommand(t *testing.T) {
	// stream without subcommand should show help (not crash)
	cmd, _, err := rootCmd.Find([]string{"stream"})
	assert.NoError(t, err)
	assert.NotNil(t, cmd)
}
```

**Step 2: Run test to verify it fails**

Run: `go test -v -run TestStreamCmd ./cmd/agnt/`
Expected: FAIL - "stream" command not found

**Step 3: Create stream.go with command definition and stream_claude.go with Claude subcommand**

Create `cmd/agnt/stream.go`:

```go
package main

import (
	"github.com/spf13/cobra"
)

var streamCmd = &cobra.Command{
	Use:   "stream",
	Short: "Bidirectional JSONL middleware for AI coding tools",
	Long: `Transparent JSONL middleware that pipes stream-json between a client and an AI tool.

Unlike 'agnt run' which wraps tools in a PTY with terminal overlay, 'agnt stream'
uses direct pipes for clean structured data passthrough. Browser events from agnt
proxies are injected as user messages into the stdin stream.

Use this when you need to:
  - Build a client that communicates with Claude via structured JSON
  - Use agnt as middleware with browser event injection
  - Avoid terminal artifacts in the data stream

Subcommands:
  claude    Stream with Claude Code using stream-json format`,
}

// Shared stream flags
var (
	streamModel          string
	streamMaxTurns       int
	streamMaxBudget      float64
	streamSystemPrompt   string
	streamSession        string
	streamNoOverlay      bool
	streamNoAgntPrompt   bool
	streamSkipAutostart  bool
)

func init() {
	streamCmd.PersistentFlags().StringVar(&streamModel, "model", "", "Model to use (sonnet, opus, haiku)")
	streamCmd.PersistentFlags().IntVar(&streamMaxTurns, "max-turns", 0, "Maximum conversation turns (0 = unlimited)")
	streamCmd.PersistentFlags().Float64Var(&streamMaxBudget, "max-budget", 0, "Maximum budget in USD (0 = unlimited)")
	streamCmd.PersistentFlags().StringVar(&streamSystemPrompt, "system-prompt", "", "Additional system prompt to append")
	streamCmd.PersistentFlags().StringVar(&streamSession, "session", "", "Session code for overlay identification (auto-generated if not set)")
	streamCmd.PersistentFlags().BoolVar(&streamNoOverlay, "no-overlay", false, "Disable overlay socket (pure pipe, no browser events)")
	streamCmd.PersistentFlags().BoolVar(&streamNoAgntPrompt, "no-agnt-prompt", false, "Skip agnt system prompt injection")
	streamCmd.PersistentFlags().BoolVar(&streamSkipAutostart, "no-autostart", false, "Skip auto-starting scripts and proxies from .agnt.kdl")

	streamCmd.AddCommand(streamClaudeCmd)
}
```

Create `cmd/agnt/stream_claude.go`:

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/debug"
)

// Stream-specific Claude flags
var (
	streamBypassPermissions bool
	streamAllowedTools      []string
	streamDisallowedTools   []string
	streamMCPConfig         string
)

var streamClaudeCmd = &cobra.Command{
	Use:   "claude [-- extra-claude-args]",
	Short: "Stream with Claude Code using bidirectional JSONL",
	Long: `Bidirectional JSONL middleware for Claude Code.

Spawns Claude Code with --input-format stream-json and --output-format stream-json,
piping stdin/stdout through without deserialization. Browser events from agnt proxies
are injected as user messages into Claude's stdin stream.

Input: JSONL on stdin (stream-json format)
Output: JSONL on stdout (stream-json format, byte-identical to Claude's output)

Examples:
  agnt stream claude
  agnt stream claude --model opus
  agnt stream claude --no-overlay
  agnt stream claude -- --allowedTools "Read,Write"`,
	Run: runStreamClaude,
}

func init() {
	streamClaudeCmd.Flags().BoolVar(&streamBypassPermissions, "bypass-permissions", true, "Bypass permission checks")
	streamClaudeCmd.Flags().StringSliceVar(&streamAllowedTools, "allowed-tools", nil, "Tools to allow (comma-separated)")
	streamClaudeCmd.Flags().StringSliceVar(&streamDisallowedTools, "disallowed-tools", nil, "Tools to disallow (comma-separated)")
	streamClaudeCmd.Flags().StringVar(&streamMCPConfig, "mcp-config", "", "Path to MCP config file")
}

func runStreamClaude(cmd *cobra.Command, args []string) {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	// Build claude CLI args
	claudeArgs := buildStreamClaudeArgs(args)

	exitCode, err := runStreamPipe(ctx, "claude", claudeArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}

// buildStreamClaudeArgs constructs CLI args for the claude process.
func buildStreamClaudeArgs(extraArgs []string) []string {
	args := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json"}

	// System prompt
	if !streamNoAgntPrompt {
		socketPath, _ := rootCmd.Flags().GetString("socket")
		if prompt := buildAgntSystemPrompt(socketPath); prompt != "" {
			args = append(args, "--append-system-prompt", prompt)
		}
	}
	if streamSystemPrompt != "" {
		args = append(args, "--append-system-prompt", streamSystemPrompt)
	}

	// Model
	if streamModel != "" {
		args = append(args, "--model", streamModel)
	}

	// Resource limits
	if streamMaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", streamMaxTurns))
	}
	if streamMaxBudget > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", streamMaxBudget))
	}

	// Permissions
	if streamBypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}

	// Tool configuration
	if len(streamAllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, streamAllowedTools...)
	}
	if len(streamDisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, streamDisallowedTools...)
	}

	// MCP config
	if streamMCPConfig != "" {
		args = append(args, "--mcp-config", streamMCPConfig)
	}

	// Extra args after --
	args = append(args, extraArgs...)

	return args
}

// runStreamPipe spawns a command with pipes and runs the bidirectional stream relay.
// Returns the process exit code.
func runStreamPipe(ctx context.Context, command string, args []string) (int, error) {
	// Find the command
	cmdPath, err := exec.LookPath(command)
	if err != nil {
		return 1, fmt.Errorf("command not found: %s", command)
	}

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	cmd.Stderr = os.Stderr

	// Set up pipes
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return 1, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 1, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("failed to start %s: %w", command, err)
	}

	debug.Log("stream", "Started %s (pid=%d) with args: %v", command, cmd.Process.Pid, args)

	// Channel for stdin multiplexing (client stdin + overlay events)
	stdinCh := make(chan []byte, 64)

	// --- Overlay setup ---
	var overlayServer *StreamOverlay
	producerCount := 1 // client stdin is always a producer
	if !streamNoOverlay {
		producerCount = 2
		sessionCode := streamSession
		if sessionCode == "" {
			sessionCode = generateSessionCode(command)
		}

		overlayServer = newStreamOverlay(stdinCh)
		socketPath := ""
		if defaultPath := DefaultOverlaySocketPath(); defaultPath != "" {
			dir := filepath.Dir(defaultPath)
			socketPath = filepath.Join(dir, fmt.Sprintf("devtool-overlay-%s.sock", sessionCode))
		}
		if err := overlayServer.Start(ctx, socketPath); err != nil {
			debug.Log("stream", "Overlay start failed (non-critical): %v", err)
			producerCount = 1 // fall back to stdin-only
		} else {
			defer overlayServer.Stop()

			// Register with daemon
			projectPath, _ := os.Getwd()
			daemonSocketPath, _ := rootCmd.Flags().GetString("socket")
			daemonHandle := startDaemonSession(ctx, daemonSessionConfig{
				SessionCode:     sessionCode,
				OverlayEndpoint: overlayServer.SocketPath(),
				ProjectPath:     projectPath,
				Command:         command,
				CmdArgs:         args,
				SocketPath:      daemonSocketPath,
				SkipAutostart:   streamSkipAutostart,
			}, func(errs []string) {
				for _, e := range errs {
					fmt.Fprintf(os.Stderr, "[agnt] autostart error: %s\n", e)
				}
			})
			defer daemonHandle.Close()
		}
	}

	// --- Goroutines ---
	var wg sync.WaitGroup

	// Producer WaitGroup - tracks when all stdin producers are done
	var producerWg sync.WaitGroup
	producerWg.Add(producerCount)

	// 1. Client stdin relay (producer)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer producerWg.Done()
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB max line
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			select {
			case stdinCh <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	// 2. Overlay producer done signal
	if overlayServer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer producerWg.Done()
			<-ctx.Done() // overlay runs until context is cancelled
		}()
	}

	// 3. Channel closer - waits for all producers, then closes channel
	wg.Add(1)
	go func() {
		defer wg.Done()
		producerWg.Wait()
		close(stdinCh)
	}()

	// 4. Stdin writer (consumer - sole owner of stdinPipe)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdinPipe.Close()
		newline := []byte("\n")
		for line := range stdinCh {
			if _, err := stdinPipe.Write(line); err != nil {
				debug.Log("stream", "stdin write error: %v", err)
				return
			}
			if _, err := stdinPipe.Write(newline); err != nil {
				debug.Log("stream", "stdin newline write error: %v", err)
				return
			}
		}
	}()

	// 5. Stdout relay (line-aware passthrough)
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB max line
		newline := []byte("\n")
		for scanner.Scan() {
			line := scanner.Bytes()
			os.Stdout.Write(line)
			os.Stdout.Write(newline)
		}
	}()

	// Wait for stdout to close (process exit) or context cancellation
	select {
	case <-done:
		// Process exited
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGINT)
		}
		<-done
	}

	// Cancel context to shut down producers
	cancel()

	// Wait for process
	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return 1, err
		}
	}

	return exitCode, nil
}
```

Add to `cmd/agnt/main.go` line 61 (after `rootCmd.AddCommand(aiCmd)`):

```go
rootCmd.AddCommand(streamCmd)
```

**Step 4: Run test to verify it passes**

Run: `go test -v -run TestStreamCmd ./cmd/agnt/`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/agnt/stream.go cmd/agnt/stream_claude.go cmd/agnt/stream_test.go cmd/agnt/main.go
git commit -m "feat: add agnt stream claude subcommand skeleton

Bidirectional JSONL middleware that pipes stream-json between a client
and Claude Code without PTY wrapping."
```

---

### Task 2: Implement StreamOverlay for browser event injection

**Files:**
- Create: `cmd/agnt/stream_overlay.go`

**Step 1: Write the failing test**

Add to `cmd/agnt/stream_test.go`:

```go
func TestStreamOverlay_FormatUserMessage(t *testing.T) {
	text := "Fix the button styling"
	msg := formatStreamUserMessage(text)

	// Should be valid JSON
	var parsed map[string]interface{}
	err := json.Unmarshal(msg, &parsed)
	assert.NoError(t, err)

	// Verify structure: {"type":"user","message":{"role":"user","content":"..."}}
	assert.Equal(t, "user", parsed["type"])
	message, ok := parsed["message"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "user", message["role"])
	assert.Equal(t, text, message["content"])
}

func TestStreamOverlay_SendsToChannel(t *testing.T) {
	ch := make(chan []byte, 10)
	so := newStreamOverlay(ch)

	so.injectEvent("panel_message", "Test message from browser")

	select {
	case msg := <-ch:
		var parsed map[string]interface{}
		err := json.Unmarshal(msg, &parsed)
		assert.NoError(t, err)
		assert.Equal(t, "user", parsed["type"])
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message on channel")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -v -run TestStreamOverlay ./cmd/agnt/`
Expected: FAIL - undefined functions

**Step 3: Implement StreamOverlay**

Create `cmd/agnt/stream_overlay.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// StreamOverlay receives browser events and injects them as stream-json
// user messages into the stdin channel.
type StreamOverlay struct {
	stdinCh    chan<- []byte
	socketPath string
	server     *http.Server
	listener   net.Listener
}

func newStreamOverlay(stdinCh chan<- []byte) *StreamOverlay {
	return &StreamOverlay{
		stdinCh: stdinCh,
	}
}

// SocketPath returns the socket path the overlay is listening on.
func (so *StreamOverlay) SocketPath() string {
	return so.socketPath
}

// Start begins listening on the Unix socket for browser events.
func (so *StreamOverlay) Start(ctx context.Context, socketPath string) error {
	if socketPath == "" {
		socketPath = DefaultOverlaySocketPath()
	}
	so.socketPath = socketPath

	// Remove stale socket
	if _, err := os.Stat(socketPath); err == nil {
		os.Remove(socketPath)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to create overlay socket: %w", err)
	}
	so.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/health", so.handleHealth)
	mux.HandleFunc("/event", so.handleEvent)
	mux.HandleFunc("/type", so.handleType)

	so.server = &http.Server{Handler: mux}

	go func() {
		if err := so.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("StreamOverlay server error: %v", err)
		}
	}()

	debug.Log("stream", "Overlay listening on %s", socketPath)
	return nil
}

// Stop shuts down the overlay server.
func (so *StreamOverlay) Stop() {
	if so.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = so.server.Shutdown(ctx)
	}
	if so.listener != nil {
		so.listener.Close()
	}
	os.Remove(so.socketPath)
}

func (so *StreamOverlay) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"mode":        "stream",
		"socket_path": so.socketPath,
	})
}

func (so *StreamOverlay) handleType(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg TypeMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// In stream mode, convert type messages to stream-json user messages
	so.injectEvent("type", msg.Text)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (so *StreamOverlay) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event ProxyEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	so.processProxyEvent(event)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// processProxyEvent converts a proxy event into formatted text and injects it.
func (so *StreamOverlay) processProxyEvent(event ProxyEvent) {
	var text string

	switch event.Type {
	case "panel_message":
		text = so.formatPanelMessageEvent(event)
	case "sketch":
		text = so.formatSketchEvent(event)
	case "design_state":
		text = so.formatDesignStateEvent(event)
	case "design_request":
		text = so.formatDesignRequestEvent(event)
	case "design_chat":
		text = so.formatDesignChatEvent(event)
	default:
		debug.Log("stream", "Unhandled proxy event type: %s", event.Type)
		return
	}

	if text != "" {
		so.injectEvent(event.Type, text)
	}
}

// injectEvent formats text as a stream-json user message and sends to the stdin channel.
func (so *StreamOverlay) injectEvent(eventType, text string) {
	msg := formatStreamUserMessage(text)
	select {
	case so.stdinCh <- msg:
		debug.Log("stream", "Injected %s event (%d bytes)", eventType, len(msg))
	default:
		debug.Log("stream", "WARNING: stdin channel full, dropping %s event", eventType)
	}
}

// formatStreamUserMessage creates a stream-json user message envelope.
func formatStreamUserMessage(text string) []byte {
	msg := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role":    "user",
			"content": text,
		},
	}
	data, _ := json.Marshal(msg)
	return data
}

// --- Event formatters (reuse logic from overlay.go) ---

func (so *StreamOverlay) formatPanelMessageEvent(event ProxyEvent) string {
	var data struct {
		Message     string `json:"message"`
		Attachments []struct {
			Type     string          `json:"type"`
			ID       string          `json:"id"`
			Selector string          `json:"selector"`
			Tag      string          `json:"tag"`
			Text     string          `json:"text"`
			Summary  string          `json:"summary"`
			Area     *screenshotArea `json:"area"`
			Data     json.RawMessage `json:"data"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return ""
	}

	text := "from agnt current page: " + data.Message

	// Format attachments (simplified - no audit summarization in stream mode)
	if len(data.Attachments) > 0 {
		text += "\n\n[Attachments]\n"
		for i, att := range data.Attachments {
			text += fmt.Sprintf("%d. %s", i+1, att.Type)
			if att.Selector != "" {
				text += fmt.Sprintf(": %s", att.Selector)
			}
			if att.Summary != "" {
				text += fmt.Sprintf(" - %s", att.Summary)
			}
			// Extract file path
			if len(att.Data) > 0 {
				var fields map[string]interface{}
				if json.Unmarshal(att.Data, &fields) == nil {
					if fp, ok := fields["file_path"].(string); ok {
						text += fmt.Sprintf("\n   -> %s", fp)
					}
				}
			}
			text += "\n"
		}
	}

	return text
}

func (so *StreamOverlay) formatSketchEvent(event ProxyEvent) string {
	var data struct {
		FilePath     string `json:"file_path"`
		ElementCount int    `json:"element_count"`
		Description  string `json:"description"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return ""
	}
	if data.Description != "" {
		return fmt.Sprintf("%s\n\n[Sketch: %s with %d elements]", data.Description, data.FilePath, data.ElementCount)
	}
	return fmt.Sprintf("[Sketch saved: %s with %d elements]", data.FilePath, data.ElementCount)
}

func (so *StreamOverlay) formatDesignStateEvent(event ProxyEvent) string {
	var data struct {
		Selector string `json:"selector"`
		Metadata struct {
			Tag     string   `json:"tag"`
			ID      string   `json:"id"`
			Classes []string `json:"classes"`
			Text    string   `json:"text"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return ""
	}
	// Reuse the existing format method (it's on Overlay, but we can call the standalone)
	// For stream mode, use a simplified version
	return fmt.Sprintf("[Design Mode: Element selected: %s (%s)]", data.Selector, data.Metadata.Tag)
}

func (so *StreamOverlay) formatDesignRequestEvent(event ProxyEvent) string {
	var data struct {
		Selector          string `json:"selector"`
		AlternativesCount int    `json:"alternatives_count"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return ""
	}
	return fmt.Sprintf("[Design Mode: More alternatives requested for %s (existing: %d)]", data.Selector, data.AlternativesCount)
}

func (so *StreamOverlay) formatDesignChatEvent(event ProxyEvent) string {
	var data struct {
		Message  string `json:"message"`
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return ""
	}
	return fmt.Sprintf("[Design Refinement: %q for element %s]", data.Message, data.Selector)
}
```

**Step 4: Run test to verify it passes**

Run: `go test -v -run TestStreamOverlay ./cmd/agnt/`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/agnt/stream_overlay.go cmd/agnt/stream_test.go
git commit -m "feat: add StreamOverlay for browser event injection in stream mode

Receives proxy events on Unix socket, formats as stream-json user
messages, and sends to stdin channel for Claude injection."
```

---

### Task 3: Integration test with a mock process

**Files:**
- Modify: `cmd/agnt/stream_test.go`

**Step 1: Write integration test**

Add to `cmd/agnt/stream_test.go`:

```go
func TestRunStreamPipe_BasicPassthrough(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// Create a simple echo script that reads stdin lines and echoes them as JSONL
	scriptContent := `#!/bin/bash
read -r line
echo "$line"
echo '{"type":"result","result":"done"}'
`
	tmpScript, err := os.CreateTemp("", "stream-test-*.sh")
	require.NoError(t, err)
	defer os.Remove(tmpScript.Name())
	_, err = tmpScript.WriteString(scriptContent)
	require.NoError(t, err)
	tmpScript.Close()
	os.Chmod(tmpScript.Name(), 0755)

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Provide input on stdin
	oldStdin := os.Stdin
	stdinR, stdinW, _ := os.Pipe()
	os.Stdin = stdinR

	// Write a message then close stdin
	go func() {
		stdinW.WriteString(`{"type":"user","message":{"role":"user","content":"hello"}}` + "\n")
		stdinW.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Temporarily disable overlay for this test
	origNoOverlay := streamNoOverlay
	streamNoOverlay = true
	defer func() { streamNoOverlay = origNoOverlay }()

	exitCode, err := runStreamPipe(ctx, tmpScript.Name(), nil)

	// Restore
	os.Stdout = oldStdout
	os.Stdin = oldStdin
	w.Close()

	// Read captured output
	var buf bytes.Buffer
	io.Copy(&buf, r)

	assert.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Contains(t, buf.String(), `{"type":"user"`)
}
```

**Step 2: Run test**

Run: `go test -v -run TestRunStreamPipe ./cmd/agnt/`
Expected: PASS (verifies stdin→process→stdout passthrough)

**Step 3: Commit**

```bash
git add cmd/agnt/stream_test.go
git commit -m "test: add integration test for stream pipe passthrough"
```

---

### Task 4: Fix compilation and wire everything together

**Step 1: Verify the full build compiles**

Run: `go build ./cmd/agnt/`

Fix any compilation errors (missing imports like `path/filepath`, unused variables, etc.).

The `stream_claude.go` references `filepath.Dir` and `filepath.Join` which need `"path/filepath"` in imports. The `cancel()` function call inside `runStreamPipe` needs the context cancel from the parent - restructure if needed.

**Step 2: Run all tests**

Run: `go test ./cmd/agnt/ -v -run "TestStream"`
Expected: All stream tests PASS

**Step 3: Run full test suite to check no regressions**

Run: `go test ./...`
Expected: All existing tests still PASS

**Step 4: Commit**

```bash
git add -A
git commit -m "fix: resolve compilation issues in stream subcommand"
```

---

### Task 5: Manual verification and smoke test

**Step 1: Build and verify help output**

```bash
go build -o agnt ./cmd/agnt/
./agnt stream --help
./agnt stream claude --help
```

Expected: Help text shows correct usage, flags, and examples.

**Step 2: Smoke test with echo script (no Claude needed)**

Create a simple test script that reads JSONL stdin and echoes:

```bash
echo '{"type":"user","message":{"role":"user","content":"hello"}}' | ./agnt stream claude --no-overlay 2>/dev/null
```

This will fail because `claude` isn't available in the test environment, but verifies the command parses flags correctly and attempts to start the process.

**Step 3: Commit final state**

```bash
git add -A
git commit -m "feat: complete agnt stream claude subcommand

Bidirectional JSONL middleware between clients and Claude Code.
Spawns claude with --input-format stream-json --output-format stream-json
using direct pipes (no PTY). Browser events injected via overlay socket."
```
