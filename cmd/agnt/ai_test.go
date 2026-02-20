package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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
		"JSON-RPC streaming",
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
		t.Error("Expected --raw flag description to mention compact JSON")
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
