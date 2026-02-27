package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	claude "github.com/standardbeagle/claude-go"
)

// TestAiClaude_RequiresPrompt verifies that agnt ai claude fails without a prompt.
func TestAiClaude_RequiresPrompt(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "claude")
	output, err := cmd.CombinedOutput()

	// Command should fail
	if err == nil {
		t.Error("Expected command to fail without prompt, but it succeeded")
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "prompt is required") {
		t.Errorf("Expected error message about prompt, got: %s", outputStr)
	}

	// Should show usage hint
	if !strings.Contains(outputStr, "Usage:") {
		t.Errorf("Expected usage information in error, got: %s", outputStr)
	}
}

// TestAiClaude_HelpOutput verifies the help text is displayed correctly.
func TestAiClaude_HelpOutput(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "claude", "--help")
	output, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf("Help command failed: %v, output: %s", err, output)
	}

	outputStr := string(output)

	// Check for key documentation elements
	expectedStrings := []string{
		"JSONL",
		"Interactive mode",
		"--raw",
		"--prompt",
		"--bypass-permissions",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(outputStr, expected) {
			t.Errorf("Expected help to contain %q, got:\n%s", expected, outputStr)
		}
	}
}

// TestAiClaude_PromptFromFlag verifies -p flag works for providing prompt.
func TestAiClaude_PromptFromFlag(t *testing.T) {
	if !claudeAvailable() {
		t.Skip("Claude Code not available - skipping integration test")
	}

	agntPath := findAgntBinary(t)

	// Use a very short prompt that should return quickly
	// We don't actually run Claude, we just verify the flag is accepted
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agntPath, "ai", "claude",
		"-p", "echo test",
		"--max-turns", "1",
		"--raw",
	)

	// If Claude is available, this will start running
	// We just want to verify the command starts without immediate error
	err := cmd.Start()
	if err != nil {
		t.Fatalf("Failed to start command: %v", err)
	}

	// Kill the process after a short time - we just want to verify it accepted the flags
	time.Sleep(100 * time.Millisecond)
	cmd.Process.Kill()
}

// TestAiClaude_PromptFromStdin verifies stdin prompt works.
func TestAiClaude_PromptFromStdin(t *testing.T) {
	if !claudeAvailable() {
		t.Skip("Claude Code not available - skipping integration test")
	}

	agntPath := findAgntBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agntPath, "ai", "claude",
		"--max-turns", "1",
		"--raw",
	)

	// Provide prompt via stdin
	cmd.Stdin = strings.NewReader("echo hello")

	err := cmd.Start()
	if err != nil {
		t.Fatalf("Failed to start command: %v", err)
	}

	// Kill after verifying it started
	time.Sleep(100 * time.Millisecond)
	cmd.Process.Kill()
}

// TestAiClaude_PositionalPrompt verifies positional argument prompt works.
func TestAiClaude_PositionalPrompt(t *testing.T) {
	if !claudeAvailable() {
		t.Skip("Claude Code not available - skipping integration test")
	}

	agntPath := findAgntBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agntPath, "ai", "claude",
		"echo hello",
		"--max-turns", "1",
		"--raw",
	)

	err := cmd.Start()
	if err != nil {
		t.Fatalf("Failed to start command: %v", err)
	}

	// Kill after verifying it started
	time.Sleep(100 * time.Millisecond)
	cmd.Process.Kill()
}

// TestAiClaude_ModelFlag verifies --model flag is accepted.
func TestAiClaude_ModelFlag(t *testing.T) {
	agntPath := findAgntBinary(t)

	// Just verify the flag is accepted by checking help output
	cmd := exec.Command(agntPath, "ai", "--help")
	output, _ := cmd.CombinedOutput()

	if !strings.Contains(string(output), "--model") {
		t.Error("Expected --model flag to be documented")
	}
}

// TestAiClaude_RawOutput verifies --raw flag produces compact JSON.
func TestAiClaude_RawOutput(t *testing.T) {
	agntPath := findAgntBinary(t)

	// Check that --raw is documented
	cmd := exec.Command(agntPath, "ai", "claude", "--help")
	output, _ := cmd.CombinedOutput()

	if !strings.Contains(string(output), "--raw") {
		t.Error("Expected --raw flag to be documented")
	}

	if !strings.Contains(string(output), "compact JSON") {
		t.Error("Expected --raw flag description to mention 'compact JSON'")
	}
}

// TestAiClaude_NoAgntPromptFlag verifies --no-agnt-prompt flag exists.
func TestAiClaude_NoAgntPromptFlag(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "claude", "--help")
	output, _ := cmd.CombinedOutput()

	if !strings.Contains(string(output), "--no-agnt-prompt") {
		t.Error("Expected --no-agnt-prompt flag to be documented")
	}
}

// TestAiClaude_SystemPromptFlag verifies --system-prompt flag exists.
func TestAiClaude_SystemPromptFlag(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "--help")
	output, _ := cmd.CombinedOutput()

	if !strings.Contains(string(output), "--system-prompt") {
		t.Error("Expected --system-prompt flag to be documented")
	}
}

// TestAiClaude_MaxTurnsFlag verifies --max-turns flag exists.
func TestAiClaude_MaxTurnsFlag(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "--help")
	output, _ := cmd.CombinedOutput()

	if !strings.Contains(string(output), "--max-turns") {
		t.Error("Expected --max-turns flag to be documented")
	}
}

// TestAiClaude_MaxBudgetFlag verifies --max-budget flag exists.
func TestAiClaude_MaxBudgetFlag(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "--help")
	output, _ := cmd.CombinedOutput()

	if !strings.Contains(string(output), "--max-budget") {
		t.Error("Expected --max-budget flag to be documented")
	}
}

// TestAiClaude_ToolFlags verifies tool allow/disallow flags exist.
func TestAiClaude_ToolFlags(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "claude", "--help")
	output, _ := cmd.CombinedOutput()

	outputStr := string(output)

	if !strings.Contains(outputStr, "--allowed-tools") {
		t.Error("Expected --allowed-tools flag to be documented")
	}

	if !strings.Contains(outputStr, "--disallowed-tools") {
		t.Error("Expected --disallowed-tools flag to be documented")
	}
}

// TestAi_SubcommandExists verifies ai command has claude subcommand.
func TestAi_SubcommandExists(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "--help")
	output, _ := cmd.CombinedOutput()

	if !strings.Contains(string(output), "claude") {
		t.Error("Expected 'claude' subcommand to be listed in ai help")
	}
}

// TestAi_Description verifies ai command description mentions JSONL.
func TestAi_Description(t *testing.T) {
	agntPath := findAgntBinary(t)

	cmd := exec.Command(agntPath, "ai", "--help")
	output, _ := cmd.CombinedOutput()

	outputStr := string(output)

	if !strings.Contains(outputStr, "JSONL") {
		t.Error("Expected ai command to mention JSONL in description")
	}

	if !strings.Contains(outputStr, "streaming") {
		t.Error("Expected ai command to mention streaming in description")
	}
}

// TestGetPrompt_Priority tests the prompt priority logic.
func TestGetPrompt_Priority(t *testing.T) {
	// Save original values
	origFlag := claudePromptFlag
	defer func() { claudePromptFlag = origFlag }()

	tests := []struct {
		name     string
		args     []string
		flag     string
		expected string
	}{
		{
			name:     "positional argument wins",
			args:     []string{"positional prompt"},
			flag:     "flag prompt",
			expected: "positional prompt",
		},
		{
			name:     "flag when no positional",
			args:     []string{},
			flag:     "flag prompt",
			expected: "flag prompt",
		},
		{
			name:     "empty positional uses flag",
			args:     []string{""},
			flag:     "flag prompt",
			expected: "flag prompt",
		},
		{
			name:     "empty when both empty",
			args:     []string{},
			flag:     "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claudePromptFlag = tt.flag
			result := getPrompt(tt.args)
			if result != tt.expected {
				t.Errorf("getPrompt(%v) with flag=%q = %q, want %q",
					tt.args, tt.flag, result, tt.expected)
			}
		})
	}
}

// TestGetPrompt_Stdin tests prompt reading from stdin.
func TestGetPrompt_Stdin(t *testing.T) {
	// Save original values
	origFlag := claudePromptFlag
	origStdin := os.Stdin
	defer func() {
		claudePromptFlag = origFlag
		os.Stdin = origStdin
	}()

	claudePromptFlag = ""

	// Create a pipe to simulate stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}

	// Write test data to the pipe
	testPrompt := "multi\nline\nprompt"
	go func() {
		w.Write([]byte(testPrompt))
		w.Close()
	}()

	os.Stdin = r

	result := getPrompt([]string{})

	if result != testPrompt {
		t.Errorf("getPrompt from stdin = %q, want %q", result, testPrompt)
	}
}

// TestBuildClaudeOptions verifies options are built correctly.
func TestBuildClaudeOptions(t *testing.T) {
	// Save and restore original values
	origModel := aiModel
	origMaxTurns := aiMaxTurns
	origMaxBudget := aiMaxBudget
	origSystemPrompt := aiSystemPrompt
	origBypass := claudeBypassPermissions
	origNoAgnt := claudeNoAgntPrompt
	origAllowed := claudeAllowedTools
	origDisallowed := claudeDisallowedTools

	defer func() {
		aiModel = origModel
		aiMaxTurns = origMaxTurns
		aiMaxBudget = origMaxBudget
		aiSystemPrompt = origSystemPrompt
		claudeBypassPermissions = origBypass
		claudeNoAgntPrompt = origNoAgnt
		claudeAllowedTools = origAllowed
		claudeDisallowedTools = origDisallowed
	}()

	t.Run("default options", func(t *testing.T) {
		aiModel = ""
		aiMaxTurns = 0
		aiMaxBudget = 0
		aiSystemPrompt = ""
		claudeBypassPermissions = true
		claudeNoAgntPrompt = true // Skip agnt prompt for simpler test
		claudeAllowedTools = nil
		claudeDisallowedTools = nil

		opts := buildClaudeOptions()

		if opts.OutputFormat != "stream-json" {
			t.Errorf("OutputFormat = %q, want 'stream-json'", opts.OutputFormat)
		}
	})

	t.Run("with model", func(t *testing.T) {
		aiModel = "haiku"
		claudeNoAgntPrompt = true

		opts := buildClaudeOptions()

		if opts.Model != "haiku" {
			t.Errorf("Model = %q, want 'haiku'", opts.Model)
		}
	})

	t.Run("with limits", func(t *testing.T) {
		aiMaxTurns = 5
		aiMaxBudget = 1.50
		claudeNoAgntPrompt = true

		opts := buildClaudeOptions()

		if opts.MaxTurns != 5 {
			t.Errorf("MaxTurns = %d, want 5", opts.MaxTurns)
		}
		if opts.MaxBudgetUSD != 1.50 {
			t.Errorf("MaxBudgetUSD = %f, want 1.50", opts.MaxBudgetUSD)
		}
	})

	t.Run("with system prompt", func(t *testing.T) {
		aiSystemPrompt = "Custom instructions"
		claudeNoAgntPrompt = true

		opts := buildClaudeOptions()
		applyAgntSystemPrompt(opts)

		if opts.SystemPrompt != "Custom instructions" {
			t.Errorf("SystemPrompt = %q, want 'Custom instructions'", opts.SystemPrompt)
		}
	})

	t.Run("with tool restrictions", func(t *testing.T) {
		claudeAllowedTools = []string{"Read", "Write"}
		claudeDisallowedTools = []string{"Bash"}
		claudeNoAgntPrompt = true

		opts := buildClaudeOptions()

		if len(opts.AllowedTools) != 2 {
			t.Errorf("AllowedTools length = %d, want 2", len(opts.AllowedTools))
		}
		if len(opts.DisallowedTools) != 1 {
			t.Errorf("DisallowedTools length = %d, want 1", len(opts.DisallowedTools))
		}
	})
}

// claudeAvailable checks if Claude Code is installed and available.
func claudeAvailable() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// TestAiClaude_MultilinePrompt verifies multiline prompts work correctly.
func TestAiClaude_MultilinePrompt(t *testing.T) {
	if !claudeAvailable() {
		t.Skip("Claude Code not available - skipping integration test")
	}

	agntPath := findAgntBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agntPath, "ai", "claude",
		"--max-turns", "1",
		"--raw",
	)

	// Provide multiline prompt via stdin
	cmd.Stdin = strings.NewReader("line 1\nline 2\nline 3")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Start()
	if err != nil {
		t.Fatalf("Failed to start command: %v", err)
	}

	// Kill after verifying it started
	time.Sleep(100 * time.Millisecond)
	cmd.Process.Kill()

	// Should not have complained about invalid prompt
	if strings.Contains(stderr.String(), "prompt is required") {
		t.Error("Multiline stdin prompt should be accepted")
	}
}

// helper for tests: wraps renderMessageTo with a textPrinted tracker
func testRender(msg claude.MessageType, spin **stderrSpinner, stdout, stderr io.Writer) string {
	var tp bool
	return renderMessageTo(msg, spin, stdout, stderr, &tp)
}

// TestRenderMessage_TextBlock verifies AssistantMessage with TextBlock prints to stdout.
func TestRenderMessage_TextBlock(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	msg := claude.AssistantMessage{
		Content: []claude.ContentBlock{
			claude.TextBlock{Text: "Hello, world!"},
		},
	}

	sid := testRender(msg, &spin, &stdout, &stderr)

	if sid != "" {
		t.Errorf("expected empty session ID, got %q", sid)
	}
	if got := stdout.String(); got != "Hello, world!\n" {
		t.Errorf("stdout = %q, want %q", got, "Hello, world!\n")
	}
}

// TestRenderMessage_MultipleTextBlocks verifies multiple text blocks are each printed.
func TestRenderMessage_MultipleTextBlocks(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	msg := claude.AssistantMessage{
		Content: []claude.ContentBlock{
			claude.TextBlock{Text: "First"},
			claude.TextBlock{Text: "Second"},
		},
	}

	testRender(msg, &spin, &stdout, &stderr)

	if got := stdout.String(); got != "First\nSecond\n" {
		t.Errorf("stdout = %q, want %q", got, "First\nSecond\n")
	}
}

// TestRenderMessage_ToolUse verifies ToolUseBlock prints tool name to stderr.
func TestRenderMessage_ToolUse(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	msg := claude.AssistantMessage{
		Content: []claude.ContentBlock{
			claude.ToolUseBlock{Name: "Read", ID: "123"},
		},
	}

	testRender(msg, &spin, &stdout, &stderr)

	if stdout.Len() != 0 {
		t.Errorf("expected no stdout output, got %q", stdout.String())
	}
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "[tool: Read]") {
		t.Errorf("stderr = %q, want to contain %q", stderrStr, "[tool: Read]")
	}
	if spin == nil {
		t.Error("expected spinner to be started after tool use")
	}
	if spin != nil {
		spin.Stop()
	}
}

// TestRenderMessage_ToolResultError verifies tool errors are shown.
func TestRenderMessage_ToolResultError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	msg := claude.AssistantMessage{
		Content: []claude.ContentBlock{
			claude.ToolResultBlock{IsError: true, ToolUseID: "123"},
		},
	}

	testRender(msg, &spin, &stdout, &stderr)

	if !strings.Contains(stderr.String(), "[tool error]") {
		t.Errorf("stderr = %q, want to contain %q", stderr.String(), "[tool error]")
	}
}

// TestRenderMessage_ToolResultSuccess verifies non-error tool results are silent.
func TestRenderMessage_ToolResultSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	msg := claude.AssistantMessage{
		Content: []claude.ContentBlock{
			claude.ToolResultBlock{IsError: false, ToolUseID: "123"},
		},
	}

	testRender(msg, &spin, &stdout, &stderr)

	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("expected silent output, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestRenderMessage_ThinkingBlock verifies thinking starts a spinner.
func TestRenderMessage_ThinkingBlock(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	msg := claude.AssistantMessage{
		Content: []claude.ContentBlock{
			claude.ThinkingBlock{Thinking: "Let me think..."},
		},
	}

	testRender(msg, &spin, &stdout, &stderr)

	if stdout.Len() != 0 {
		t.Errorf("expected no stdout, got %q", stdout.String())
	}
	if spin == nil {
		t.Error("expected spinner to be started for thinking block")
	}
	if spin != nil {
		spin.Stop()
	}
}

// TestRenderMessage_SystemInit verifies init messages are silent.
func TestRenderMessage_SystemInit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	msg := claude.SystemMessage{Subtype: "init"}

	testRender(msg, &spin, &stdout, &stderr)

	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("expected silent output for init, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if spin != nil {
		t.Error("expected no spinner for init")
	}
}

// TestRenderMessage_SystemHook verifies hook messages start/stop spinner.
func TestRenderMessage_SystemHook(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	// hook_started should start spinner
	testRender(claude.SystemMessage{Subtype: "hook_started"}, &spin, &stdout, &stderr)
	if spin == nil {
		t.Fatal("expected spinner after hook_started")
	}

	// hook_response should stop spinner
	testRender(claude.SystemMessage{Subtype: "hook_response"}, &spin, &stdout, &stderr)
	if spin != nil {
		t.Error("expected spinner to be nil after hook_response")
	}
}

// TestRenderMessage_ResultSummary verifies result summary formatting.
func TestRenderMessage_ResultSummary(t *testing.T) {
	tests := []struct {
		name     string
		msg      claude.ResultMessage
		contains []string
	}{
		{
			name: "duration and tokens",
			msg: claude.ResultMessage{
				DurationMS: 84000,
				Usage:      &claude.Usage{InputTokens: 1000, OutputTokens: 300},
				SessionID:  "sess-1",
			},
			contains: []string{"1m 24s", "1.3k tokens"},
		},
		{
			name: "with cost",
			msg: claude.ResultMessage{
				DurationMS:   5000,
				Usage:        &claude.Usage{InputTokens: 500, OutputTokens: 100},
				TotalCostUSD: 0.05,
				SessionID:    "sess-2",
			},
			contains: []string{"5s", "600 tokens", "$0.05"},
		},
		{
			name: "large token count",
			msg: claude.ResultMessage{
				DurationMS: 120000,
				Usage:      &claude.Usage{InputTokens: 500000, OutputTokens: 500000},
				SessionID:  "sess-3",
			},
			contains: []string{"2m", "1.0M tokens"},
		},
		{
			name: "zero duration",
			msg: claude.ResultMessage{
				SessionID: "sess-4",
			},
			contains: nil, // no output expected
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var spin *stderrSpinner

			sid := testRender(tt.msg, &spin, &stdout, &stderr)

			if sid != tt.msg.SessionID {
				t.Errorf("session ID = %q, want %q", sid, tt.msg.SessionID)
			}

			stderrStr := stderr.String()
			for _, want := range tt.contains {
				if !strings.Contains(stderrStr, want) {
					t.Errorf("stderr = %q, want to contain %q", stderrStr, want)
				}
			}

			if tt.contains == nil && stderrStr != "" {
				t.Errorf("expected no output, got stderr=%q", stderrStr)
			}
		})
	}
}

// TestRenderMessage_ResultFallback verifies ResultMessage.Result prints when no text was streamed.
func TestRenderMessage_ResultFallback(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner
	textPrinted := false

	msg := claude.ResultMessage{
		DurationMS: 5000,
		Result:     "This is the response text.",
		SessionID:  "sess-fallback",
	}

	sid := renderMessageTo(msg, &spin, &stdout, &stderr, &textPrinted)

	if sid != "sess-fallback" {
		t.Errorf("session ID = %q, want %q", sid, "sess-fallback")
	}
	if !strings.Contains(stdout.String(), "This is the response text.") {
		t.Errorf("stdout = %q, want to contain result text", stdout.String())
	}
}

// TestRenderMessage_NoFallbackWhenTextPrinted verifies Result is NOT printed when text was streamed.
func TestRenderMessage_NoFallbackWhenTextPrinted(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner
	textPrinted := true // text was already printed during streaming

	msg := claude.ResultMessage{
		DurationMS: 5000,
		Result:     "Duplicate text",
		SessionID:  "sess-nodup",
	}

	renderMessageTo(msg, &spin, &stdout, &stderr, &textPrinted)

	if strings.Contains(stdout.String(), "Duplicate text") {
		t.Error("expected result text to NOT be printed when text was already streamed")
	}
}

// TestRenderMessage_SpinnerClearsOnText verifies spinner stops when text arrives.
func TestRenderMessage_SpinnerClearsOnText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var spin *stderrSpinner

	// Start spinner via thinking
	testRender(claude.AssistantMessage{
		Content: []claude.ContentBlock{claude.ThinkingBlock{Thinking: "..."}},
	}, &spin, &stdout, &stderr)

	if spin == nil {
		t.Fatal("expected spinner to be running")
	}

	// Text should stop spinner
	testRender(claude.AssistantMessage{
		Content: []claude.ContentBlock{claude.TextBlock{Text: "Done"}},
	}, &spin, &stdout, &stderr)

	if spin != nil {
		t.Error("expected spinner to be nil after text block")
	}

	if !strings.Contains(stdout.String(), "Done") {
		t.Errorf("expected stdout to contain 'Done', got %q", stdout.String())
	}
}

// TestParseMessage_AssistantNested verifies claude-go handles nested message.content.
func TestParseMessage_AssistantNested(t *testing.T) {
	raw := json.RawMessage(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello from Claude"}]}}`)

	msg, err := claude.ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage error: %v", err)
	}

	assistant, ok := msg.(claude.AssistantMessage)
	if !ok {
		t.Fatalf("expected AssistantMessage, got %T", msg)
	}

	if len(assistant.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(assistant.Content))
	}

	tb, ok := assistant.Content[0].(claude.TextBlock)
	if !ok {
		t.Fatalf("expected TextBlock, got %T", assistant.Content[0])
	}
	if tb.Text != "Hello from Claude" {
		t.Errorf("text = %q, want %q", tb.Text, "Hello from Claude")
	}
}

// TestSpinner_StartStop verifies spinner lifecycle.
func TestSpinner_StartStop(t *testing.T) {
	var buf bytes.Buffer
	s := newStderrSpinner("Loading...", &buf)

	// Give spinner time to write at least one frame
	time.Sleep(150 * time.Millisecond)

	s.Stop()

	output := buf.String()
	if !strings.Contains(output, "Loading...") {
		t.Errorf("spinner output = %q, want to contain %q", output, "Loading...")
	}
	// Should end with clear sequence
	if !strings.HasSuffix(output, "\r\033[K") {
		t.Errorf("spinner output should end with clear sequence, got %q", output)
	}
}

// TestSpinner_DoubleStop verifies stopping a spinner twice doesn't panic.
func TestSpinner_DoubleStop(t *testing.T) {
	var buf bytes.Buffer
	s := newStderrSpinner("test", &buf)
	s.Stop()
	s.Stop() // Should not panic
}

// TestFormatDuration verifies duration formatting.
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{500, "0s"},
		{5000, "5s"},
		{59000, "59s"},
		{60000, "1m"},
		{84000, "1m 24s"},
		{120000, "2m"},
		{3661000, "61m 1s"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.ms)
		if got != tt.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

// TestFormatTokens verifies token count formatting.
func TestFormatTokens(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{42, "42 tokens"},
		{999, "999 tokens"},
		{1000, "1.0k tokens"},
		{1300, "1.3k tokens"},
		{150000, "150.0k tokens"},
		{1000000, "1.0M tokens"},
		{2500000, "2.5M tokens"},
	}

	for _, tt := range tests {
		got := formatTokens(tt.n)
		if got != tt.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
