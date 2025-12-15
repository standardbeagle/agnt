package overlay

import (
	"os"
	"testing"

	"github.com/standardbeagle/agnt/internal/aichannel"
)

func TestNewSummarizer(t *testing.T) {
	config := SummarizerConfig{
		SocketPath: "/tmp/test.sock",
		Agent:      aichannel.AgentClaude,
	}

	s := NewSummarizer(config)
	if s == nil {
		t.Fatal("NewSummarizer returned nil")
	}

	if s.AgentType() != aichannel.AgentClaude {
		t.Errorf("AgentType = %v, want %v", s.AgentType(), aichannel.AgentClaude)
	}

	// Verify JSON output format is configured for extracting final response
	cfg := s.channel.Config()
	if cfg.OutputFormat != "json" {
		t.Errorf("OutputFormat = %q, want %q", cfg.OutputFormat, "json")
	}

	// Claude should support JSON output
	if !cfg.SupportsJSON {
		t.Error("Claude should support JSON output")
	}
}

func TestNewSummarizer_NonJSONAgent(t *testing.T) {
	// Test with an agent that doesn't support JSON (e.g., Copilot)
	// Non-Claude agents should auto-enable API mode
	config := SummarizerConfig{
		SocketPath: "/tmp/test.sock",
		Agent:      aichannel.AgentCopilot,
	}

	s := NewSummarizer(config)
	if s == nil {
		t.Fatal("NewSummarizer returned nil")
	}

	cfg := s.channel.Config()
	// Non-Claude agents should automatically use API mode
	if !cfg.UseAPI {
		t.Error("Non-Claude agent should automatically use API mode")
	}
}

func TestNewSummarizer_ClaudeUsesCliMode(t *testing.T) {
	// Claude agent should use CLI mode (Claude Code Max plan only supports CLI)
	config := SummarizerConfig{
		SocketPath: "/tmp/test.sock",
		Agent:      aichannel.AgentClaude,
	}

	s := NewSummarizer(config)
	if s == nil {
		t.Fatal("NewSummarizer returned nil")
	}

	cfg := s.channel.Config()
	// Claude should NOT use API mode by default
	if cfg.UseAPI {
		t.Error("Claude agent should use CLI mode by default")
	}
}

func TestNewSummarizer_ClaudeCanForceAPIMode(t *testing.T) {
	// Even Claude can be forced to API mode if explicitly requested
	config := SummarizerConfig{
		SocketPath: "/tmp/test.sock",
		Agent:      aichannel.AgentClaude,
		UseAPI:     true,
		APIKey:     "test-key",
	}

	s := NewSummarizer(config)
	if s == nil {
		t.Fatal("NewSummarizer returned nil")
	}

	cfg := s.channel.Config()
	if !cfg.UseAPI {
		t.Error("Claude should use API mode when explicitly requested")
	}
}

func TestNewSummarizer_NonClaudeAgentsUseAPIMode(t *testing.T) {
	// Test that various non-Claude agents automatically use API mode
	agents := []aichannel.AgentType{
		aichannel.AgentCopilot,
		aichannel.AgentGemini,
		aichannel.AgentOpenCode,
		aichannel.AgentKimi,
		aichannel.AgentAider,
	}

	for _, agent := range agents {
		t.Run(string(agent), func(t *testing.T) {
			config := SummarizerConfig{
				SocketPath: "/tmp/test.sock",
				Agent:      agent,
			}

			s := NewSummarizer(config)
			cfg := s.channel.Config()

			if !cfg.UseAPI {
				t.Errorf("Agent %s should automatically use API mode", agent)
			}
		})
	}
}

func TestSummarizer_IsAvailable_NotInstalled(t *testing.T) {
	// Save and unset ALL provider env vars to ensure no API fallback
	envVars := []string{
		"ANTHROPIC_API_KEY", "CLAUDE_KEY",
		"OPENAI_KEY", "OPENAI_API_KEY",
		"GOOGLE_KEY", "GOOGLE_API_KEY",
		"MISTRAL_KEY", "MISTRAL_API_KEY",
		"DEEP_SEEK_KEY", "DEEPSEEK_API_KEY",
		"OPEN_ROUTER_KEY", "OPENROUTER_API_KEY",
		"TOGETHER_KEY", "TOGETHER_API_KEY",
		"HYPERBOLIC_KEY", "HYPERBOLIC_API_KEY",
		"SAMBA_NOVA_KEY", "SAMBANOVA_API_KEY",
		"GLM_KEY", "GLM_API_KEY",
	}
	originals := make(map[string]string)
	for _, key := range envVars {
		originals[key] = os.Getenv(key)
		os.Unsetenv(key)
	}
	defer func() {
		for key, val := range originals {
			if val != "" {
				os.Setenv(key, val)
			}
		}
	}()

	config := SummarizerConfig{
		SocketPath: "/tmp/test.sock",
		Agent:      aichannel.AgentCustom,
	}
	config.Command = "nonexistent-ai-tool-12345"

	s := NewSummarizer(config)
	if s.IsAvailable() {
		t.Error("IsAvailable should return false for non-existent command without API keys")
	}
}

func TestContainsErrorPatterns(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "empty",
			output:   "",
			expected: false,
		},
		{
			name:     "no errors",
			output:   "INFO: Server started on port 3000\nDEBUG: Connected to database",
			expected: false,
		},
		{
			name:     "contains error",
			output:   "Error: Connection refused",
			expected: true,
		},
		{
			name:     "contains exception",
			output:   "Unhandled exception in main thread",
			expected: true,
		},
		{
			name:     "contains panic",
			output:   "panic: runtime error: invalid memory address",
			expected: true,
		},
		{
			name:     "contains failed",
			output:   "Test failed: expected 5 got 3",
			expected: true,
		},
		{
			name:     "contains timeout",
			output:   "Operation timeout after 30s",
			expected: true,
		},
		{
			name:     "case insensitive",
			output:   "ERROR: something went wrong",
			expected: true,
		},
		{
			name:     "contains traceback",
			output:   "Traceback (most recent call last):",
			expected: true,
		},
		{
			name:     "contains stack trace",
			output:   "Stack trace:\n\tat main.go:42",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsErrorPatterns(tt.output)
			if result != tt.expected {
				t.Errorf("containsErrorPatterns(%q) = %v, want %v", tt.output, result, tt.expected)
			}
		})
	}
}

func TestSummarizer_BuildContext(t *testing.T) {
	s := NewSummarizer(SummarizerConfig{
		Agent: aichannel.AgentClaude,
	})

	processes := []ProcessSummary{
		{
			ID:        "test-1",
			Command:   "npm run dev",
			State:     "running",
			HasErrors: false,
			Output:    "Server started on port 3000",
		},
	}

	proxies := []ProxySummary{
		{
			ID:         "dev",
			TargetURL:  "http://localhost:3000",
			ListenAddr: ":8080",
			ErrorCount: 0,
			PageCount:  2,
		},
	}

	context := s.buildContext(processes, proxies)

	// Check that context contains expected sections
	if !contains(context, "=== SYSTEM STATUS ===") {
		t.Error("Context missing system status header")
	}
	if !contains(context, "== PROCESSES ==") {
		t.Error("Context missing processes section")
	}
	if !contains(context, "== PROXIES ==") {
		t.Error("Context missing proxies section")
	}
	if !contains(context, "test-1") {
		t.Error("Context missing process ID")
	}
	if !contains(context, "npm run dev") {
		t.Error("Context missing process command")
	}
	if !contains(context, "http://localhost:3000") {
		t.Error("Context missing proxy target")
	}
}

func TestSummarizer_BuildContext_Empty(t *testing.T) {
	s := NewSummarizer(SummarizerConfig{
		Agent: aichannel.AgentClaude,
	})

	context := s.buildContext(nil, nil)

	if !contains(context, "No running processes") {
		t.Error("Context should mention no processes")
	}
	if !contains(context, "No running proxies") {
		t.Error("Context should mention no proxies")
	}
}

func TestSummarizer_BuildPrompt(t *testing.T) {
	s := NewSummarizer(SummarizerConfig{
		Agent: aichannel.AgentClaude,
	})

	prompt := s.buildPrompt()

	if prompt == "" {
		t.Error("buildPrompt returned empty string")
	}

	// Check for key instructions (updated for succinct prompt)
	if !contains(prompt, "Analyze") {
		t.Error("Prompt should mention analysis")
	}
	if !contains(prompt, "error") {
		t.Error("Prompt should mention errors")
	}
	if !contains(prompt, "concise") {
		t.Error("Prompt should mention being concise")
	}
	if !contains(prompt, "BRIEF") {
		t.Error("Prompt should emphasize brevity")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
