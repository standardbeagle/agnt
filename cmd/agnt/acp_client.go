//go:build unix

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// acpClient implements acp.Client. It renders streamed session updates to
// stdout/stderr (mirroring the ai-claude renderer), auto-approves tool
// permission requests in lightest mode, and services filesystem callbacks.
type acpClient struct {
	autoApprove bool

	mu          sync.Mutex
	out         io.Writer // agent text sink (stdout, or overlay raw writer)
	err         io.Writer // status/spinner sink (stderr, or overlay raw writer)
	spin        *stderrSpinner
	textPrinted bool

	// Terminal registry: agent-requested commands run under us when the
	// terminal capability is advertised. See acp_terminal.go.
	termMu    sync.Mutex
	terminals map[string]*acpTerminal
	termSeq   int
}

var _ acp.Client = (*acpClient)(nil)

func newACPClient(autoApprove bool) *acpClient {
	c := &acpClient{
		autoApprove: autoApprove,
		out:         os.Stdout,
		err:         os.Stderr,
		terminals:   make(map[string]*acpTerminal),
	}
	return c
}

// setWriters redirects rendered output (used by the overlay REPL to route
// through the raw-mode writer instead of os.Stdout/os.Stderr).
func (c *acpClient) setWriters(out, errw io.Writer) {
	c.mu.Lock()
	c.out, c.err = out, errw
	c.mu.Unlock()
}

// errWriter returns the current status sink.
func (c *acpClient) errWriter() io.Writer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// setSpinner installs the spinner for the next turn and resets per-turn
// render state.
func (c *acpClient) setSpinner(s *stderrSpinner) {
	c.mu.Lock()
	c.spin = s
	c.textPrinted = false
	c.mu.Unlock()
}

// stopSpinner stops and clears the active spinner if any.
func (c *acpClient) stopSpinner() {
	c.mu.Lock()
	if c.spin != nil {
		c.spin.Stop()
		c.spin = nil
	}
	c.mu.Unlock()
}

// swapSpinner stops the current spinner and starts a new one with the given
// label (used for tool-call / thinking transitions).
func (c *acpClient) swapSpinner(label string) {
	c.mu.Lock()
	if c.spin != nil {
		c.spin.Stop()
	}
	c.spin = newStderrSpinner(label, c.err)
	c.mu.Unlock()
}

// SessionUpdate renders a streamed update. Agent text goes to stdout;
// thoughts, tool activity, and plans go to stderr as status.
func (c *acpClient) SessionUpdate(_ context.Context, params acp.SessionNotification) error {
	u := params.Update
	switch {
	case u.AgentMessageChunk != nil:
		if t := u.AgentMessageChunk.Content.Text; t != nil {
			c.stopSpinner()
			c.mu.Lock()
			fmt.Fprint(c.out, t.Text)
			c.textPrinted = true
			c.mu.Unlock()
		}
	case u.AgentThoughtChunk != nil:
		c.swapSpinner("Thinking...")
	case u.ToolCall != nil:
		title := u.ToolCall.Title
		c.swapSpinner(fmt.Sprintf("[tool: %s]", title))
	case u.ToolCallUpdate != nil:
		// Status churn — keep the spinner; surface errors only.
		if u.ToolCallUpdate.Status != nil && *u.ToolCallUpdate.Status == acp.ToolCallStatusFailed {
			c.stopSpinner()
			fmt.Fprintf(c.errWriter(), "\r\033[K[tool failed: %s]\n", u.ToolCallUpdate.ToolCallId)
		}
	case u.Plan != nil:
		c.swapSpinner("Planning...")
	}
	return nil
}

// RequestPermission auto-approves in lightest mode (prefer an allow option),
// otherwise prompts on stderr/stdin.
func (c *acpClient) RequestPermission(_ context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if c.autoApprove {
		for _, o := range params.Options {
			if o.Kind == acp.PermissionOptionKindAllowOnce || o.Kind == acp.PermissionOptionKindAllowAlways {
				return selectOption(o.OptionId), nil
			}
		}
		if len(params.Options) > 0 {
			return selectOption(params.Options[0].OptionId), nil
		}
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}},
		}, nil
	}

	c.stopSpinner()
	w := c.errWriter()
	title := ""
	if params.ToolCall.Title != nil {
		title = *params.ToolCall.Title
	}
	fmt.Fprintf(w, "\n🔐 Permission: %s\n", title)
	for i, opt := range params.Options {
		fmt.Fprintf(w, "   %d. %s (%s)\n", i+1, opt.Name, opt.Kind)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprint(w, "Choose: ")
		line, _ := reader.ReadString('\n')
		idx := -1
		_, _ = fmt.Sscanf(strings.TrimSpace(line), "%d", &idx)
		if idx >= 1 && idx <= len(params.Options) {
			return selectOption(params.Options[idx-1].OptionId), nil
		}
		fmt.Fprintln(w, "Invalid option.")
	}
}

func selectOption(id acp.PermissionOptionId) acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: id},
		},
	}
}

// ReadTextFile serves a filesystem read for the agent (absolute paths only),
// honoring the optional 1-based line/limit window.
func (c *acpClient) ReadTextFile(_ context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.ReadTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}
	b, err := os.ReadFile(params.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("read %s: %w", params.Path, err)
	}
	content := string(b)
	if params.Line != nil || params.Limit != nil {
		lines := strings.Split(content, "\n")
		start := 0
		if params.Line != nil && *params.Line > 0 {
			start = min(max(*params.Line-1, 0), len(lines))
		}
		end := len(lines)
		if params.Limit != nil && *params.Limit > 0 && start+*params.Limit < end {
			end = start + *params.Limit
		}
		content = strings.Join(lines[start:end], "\n")
	}
	return acp.ReadTextFileResponse{Content: content}, nil
}

// WriteTextFile serves a filesystem write for the agent (absolute paths only).
func (c *acpClient) WriteTextFile(_ context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.WriteTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}
	if dir := filepath.Dir(params.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return acp.WriteTextFileResponse{}, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(params.Path, []byte(params.Content), 0o644); err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("write %s: %w", params.Path, err)
	}
	return acp.WriteTextFileResponse{}, nil
}

// Terminal methods (CreateTerminal, TerminalOutput, WaitForTerminalExit,
// KillTerminal, ReleaseTerminal) live in acp_terminal.go.

// runACPRepl runs the cooked-mode multi-turn REPL (non-TTY stdin or
// --no-overlay). Each stdin line — and each injected daemon message — is one
// prompt turn; the agnt context rides only the first turn. Exit with /exit,
// /quit, or Ctrl+D.
func runACPRepl(ctx context.Context, conn *acp.ClientSideConnection, client *acpClient, sessionID acp.SessionId, systemContext string, gate *acpAlertGate) error {
	fmt.Fprintln(os.Stderr, "agnt acp - interactive mode")
	fmt.Fprintln(os.Stderr, "Type /exit or /quit to exit, Ctrl+D for EOF.")
	if systemContext != "" {
		fmt.Fprintln(os.Stderr, "[agnt context injected on first turn]")
	}

	stdinCh := make(chan string)
	go func() {
		defer close(stdinCh)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			stdinCh <- scanner.Text()
		}
	}()

	firstTurn := true
	runTurn := func(text string) {
		turnContext := ""
		if firstTurn {
			turnContext = systemContext
			firstTurn = false
		}
		if err := acpPromptTurn(ctx, conn, client, sessionID, text, turnContext, true); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		fmt.Fprintln(os.Stderr)
	}

	for {
		fmt.Fprint(os.Stderr, "> ")
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-stdinCh:
			if !ok {
				fmt.Fprintln(os.Stderr)
				return nil
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if line == "/exit" || line == "/quit" {
				return nil
			}
			runTurn(line)
		case <-gate.Ready():
			// Idle between turns: inject the coalesced, deduped alert batch.
			// A nil gate returns a nil channel, so this case never fires.
			if msg, ok := gate.Pull(); ok {
				fmt.Fprintf(os.Stderr, "\r\033[K[alerts]\n%s\n", msg)
				runTurn(msg)
			}
		}
	}
}

// readAllStdin reads stdin to EOF (used when a prompt is piped in).
func readAllStdin() string {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}
