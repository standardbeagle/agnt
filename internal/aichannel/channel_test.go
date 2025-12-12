package aichannel

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	ch := New()
	if ch == nil {
		t.Fatal("New() returned nil")
	}
	if ch.configured {
		t.Error("New channel should not be configured")
	}
}

func TestNewWithConfig(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent:   AgentClaude,
		Timeout: 30 * time.Second,
	})
	if ch == nil {
		t.Fatal("NewWithConfig() returned nil")
	}
	if !ch.configured {
		t.Error("Channel should be configured")
	}
}

func TestConfigure_AppliesDefaults(t *testing.T) {
	tests := []struct {
		name               string
		agent              AgentType
		expectedCommand    string
		expectedNonIntFlag string
		expectedQuietFlag  string
		expectedUseStdin   bool
	}{
		{
			name:               "Claude",
			agent:              AgentClaude,
			expectedCommand:    "claude",
			expectedNonIntFlag: "-p",
			expectedQuietFlag:  "",
			expectedUseStdin:   true,
		},
		{
			name:               "Copilot",
			agent:              AgentCopilot,
			expectedCommand:    "copilot",
			expectedNonIntFlag: "-p",
			expectedQuietFlag:  "-s",
			expectedUseStdin:   true,
		},
		{
			name:               "Gemini",
			agent:              AgentGemini,
			expectedCommand:    "gemini",
			expectedNonIntFlag: "-e",
			expectedQuietFlag:  "-q",
			expectedUseStdin:   true,
		},
		{
			name:               "Kimi",
			agent:              AgentKimi,
			expectedCommand:    "kimi-cli",
			expectedNonIntFlag: "",
			expectedQuietFlag:  "",
			expectedUseStdin:   true,
		},
		{
			name:               "Cursor",
			agent:              AgentCursor,
			expectedCommand:    "cursor-agent",
			expectedNonIntFlag: "-p",
			expectedQuietFlag:  "",
			expectedUseStdin:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewWithConfig(Config{Agent: tt.agent})
			cfg := ch.Config()

			if cfg.Command != tt.expectedCommand {
				t.Errorf("Command = %q, want %q", cfg.Command, tt.expectedCommand)
			}
			if cfg.NonInteractiveFlag != tt.expectedNonIntFlag {
				t.Errorf("NonInteractiveFlag = %q, want %q", cfg.NonInteractiveFlag, tt.expectedNonIntFlag)
			}
			if cfg.QuietFlag != tt.expectedQuietFlag {
				t.Errorf("QuietFlag = %q, want %q", cfg.QuietFlag, tt.expectedQuietFlag)
			}
			if cfg.UseStdin != tt.expectedUseStdin {
				t.Errorf("UseStdin = %v, want %v", cfg.UseStdin, tt.expectedUseStdin)
			}
		})
	}
}

func TestConfigure_CustomCommand(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent:   AgentClaude,
		Command: "/custom/path/claude",
	})
	cfg := ch.Config()

	if cfg.Command != "/custom/path/claude" {
		t.Errorf("Custom command not preserved: got %q", cfg.Command)
	}
}

func TestConfigure_DefaultTimeout(t *testing.T) {
	ch := NewWithConfig(Config{Agent: AgentClaude})
	cfg := ch.Config()

	if cfg.Timeout != 2*time.Minute {
		t.Errorf("Timeout = %v, want 2 minutes", cfg.Timeout)
	}
}

func TestConfigure_CustomTimeout(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent:   AgentClaude,
		Timeout: 5 * time.Minute,
	})
	cfg := ch.Config()

	if cfg.Timeout != 5*time.Minute {
		t.Errorf("Custom timeout not preserved: got %v", cfg.Timeout)
	}
}

func TestIsAvailable_NotConfigured(t *testing.T) {
	ch := New()
	if ch.IsAvailable() {
		t.Error("Unconfigured channel should not be available")
	}
}

func TestIsAvailable_MissingCommand(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent:   AgentCustom,
		Command: "nonexistent-ai-tool-12345",
	})
	if ch.IsAvailable() {
		t.Error("Channel with missing command should not be available")
	}
}

func TestIsAvailable_Echo(t *testing.T) {
	// Echo should be available on all systems
	ch := NewWithConfig(Config{
		Agent:   AgentCustom,
		Command: "echo",
	})
	if !ch.IsAvailable() {
		t.Error("Channel with 'echo' command should be available")
	}
}

func TestSend_NotConfigured(t *testing.T) {
	ch := New()
	_, err := ch.Send(context.Background(), "test", "")
	if err != ErrNotConfigured {
		t.Errorf("Expected ErrNotConfigured, got %v", err)
	}
}

func TestSend_NotAvailable(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent:   AgentCustom,
		Command: "nonexistent-ai-tool-12345",
	})
	_, err := ch.Send(context.Background(), "test", "")
	if err == nil {
		t.Error("Expected error for unavailable command")
	}
}

func TestSend_WithEcho(t *testing.T) {
	// Use echo as a mock AI agent
	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            "echo",
		NonInteractiveFlag: "", // echo doesn't need flags
	})

	result, err := ch.Send(context.Background(), "hello world", "")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if result != "hello world" {
		t.Errorf("Send() = %q, want %q", result, "hello world")
	}
}

func TestSendAndParse_TextFormat(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            "echo",
		NonInteractiveFlag: "",
		OutputFormat:       "text",
	})

	resp, err := ch.SendAndParse(context.Background(), "hello world", "")
	if err != nil {
		t.Fatalf("SendAndParse() error = %v", err)
	}

	if resp.Result != "hello world" {
		t.Errorf("Result = %q, want %q", resp.Result, "hello world")
	}
}

func TestSendAndParse_JSONFormat(t *testing.T) {
	// Use printf to output JSON
	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            "printf",
		NonInteractiveFlag: "",
		OutputFormat:       "json",
		OutputFormatFlag:   "--output-format", // Enable JSON support for custom agent
	})

	// printf outputs JSON literally
	resp, err := ch.SendAndParse(context.Background(), `{"type":"result","result":"JSON response","session_id":"test123"}`, "")
	if err != nil {
		t.Fatalf("SendAndParse() error = %v", err)
	}

	if resp.Result != "JSON response" {
		t.Errorf("Result = %q, want %q", resp.Result, "JSON response")
	}
	if resp.SessionID != "test123" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "test123")
	}
}

func TestSendAndParse_DefaultsToText(t *testing.T) {
	// No OutputFormat specified - should default to text
	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            "echo",
		NonInteractiveFlag: "",
		// OutputFormat not set
	})

	resp, err := ch.SendAndParse(context.Background(), "plain text", "")
	if err != nil {
		t.Fatalf("SendAndParse() error = %v", err)
	}

	if resp.Result != "plain text" {
		t.Errorf("Result = %q, want %q", resp.Result, "plain text")
	}
}

func TestSend_WithStdin(t *testing.T) {
	// Use cat as a mock that reads stdin
	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            "cat",
		NonInteractiveFlag: "",
		UseStdin:           true,
	})

	result, err := ch.Send(context.Background(), "", "stdin content")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if result != "stdin content" {
		t.Errorf("Send() = %q, want %q", result, "stdin content")
	}
}

func TestSend_Timeout(t *testing.T) {
	// Use sleep to test timeout
	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            "sleep",
		NonInteractiveFlag: "",
		Timeout:            100 * time.Millisecond,
	})

	_, err := ch.Send(context.Background(), "10", "")
	if err != ErrTimeout {
		t.Errorf("Expected ErrTimeout, got %v", err)
	}
}

func TestBuildArgs_Claude(t *testing.T) {
	ch := NewWithConfig(Config{Agent: AgentClaude})
	args := ch.buildArgs("test prompt")

	// Should have: -p "test prompt"
	found := false
	for i, arg := range args {
		if arg == "-p" && i+1 < len(args) && args[i+1] == "test prompt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected -p flag with prompt, got %v", args)
	}
}

func TestBuildArgs_Copilot(t *testing.T) {
	ch := NewWithConfig(Config{Agent: AgentCopilot})
	args := ch.buildArgs("test prompt")

	// Should have: -p "test prompt" -s
	hasP := false
	hasS := false
	for i, arg := range args {
		if arg == "-p" && i+1 < len(args) && args[i+1] == "test prompt" {
			hasP = true
		}
		if arg == "-s" {
			hasS = true
		}
	}
	if !hasP {
		t.Errorf("Expected -p flag with prompt, got %v", args)
	}
	if !hasS {
		t.Errorf("Expected -s (quiet) flag, got %v", args)
	}
}

func TestBuildArgs_WithExtraArgs(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent: AgentClaude,
		Args:  []string{"--model", "opus"},
	})
	args := ch.buildArgs("test")

	// Extra args should come first
	if len(args) < 2 || args[0] != "--model" || args[1] != "opus" {
		t.Errorf("Extra args not at beginning: %v", args)
	}
}

func TestBuildArgs_OutputFormat(t *testing.T) {
	ch := NewWithConfig(Config{
		Agent:        AgentClaude,
		OutputFormat: "json",
	})
	args := ch.buildArgs("test")

	found := false
	for i, arg := range args {
		if arg == "--output-format" && i+1 < len(args) && args[i+1] == "json" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected --output-format json, got %v", args)
	}
}

func TestBuildArgs_OutputFormat_UnsupportedAgent(t *testing.T) {
	// Copilot doesn't support --output-format
	ch := NewWithConfig(Config{
		Agent:        AgentCopilot,
		OutputFormat: "json",
	})
	args := ch.buildArgs("test")

	// Should NOT have --output-format flag
	for _, arg := range args {
		if arg == "--output-format" {
			t.Errorf("Copilot should not have --output-format flag, got %v", args)
		}
	}
}

func TestSupportsJSONOutput(t *testing.T) {
	tests := []struct {
		agent    AgentType
		expected bool
	}{
		{AgentClaude, true},
		{AgentGemini, true},
		{AgentCopilot, false},
		{AgentKimi, false},
		{AgentAider, false},
		{AgentOpenCode, false},
		{AgentCursor, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.agent), func(t *testing.T) {
			ch := NewWithConfig(Config{Agent: tt.agent})
			if ch.SupportsJSONOutput() != tt.expected {
				t.Errorf("SupportsJSONOutput() = %v, want %v", ch.SupportsJSONOutput(), tt.expected)
			}
		})
	}
}

func TestGetKnownAgents(t *testing.T) {
	agents := GetKnownAgents()
	if len(agents) == 0 {
		t.Error("GetKnownAgents() returned empty list")
	}

	// Check that known agents are in the list
	expected := map[AgentType]bool{
		AgentClaude:  true,
		AgentCopilot: true,
		AgentGemini:  true,
	}

	for agent := range expected {
		found := false
		for _, a := range agents {
			if a == agent {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected agent %s not in list", agent)
		}
	}
}

func TestDetectAvailableAgents(t *testing.T) {
	// This just tests that the function runs without error
	// The result depends on what's installed
	agents := DetectAvailableAgents()
	t.Logf("Detected available agents: %v", agents)
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Plain text",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "CSI color sequence",
			input:    "\x1b[31mRed\x1b[0m Text",
			expected: "Red Text",
		},
		{
			name:     "Cursor movement",
			input:    "\x1b[2J\x1b[HHello",
			expected: "Hello",
		},
		{
			name:     "Show cursor",
			input:    "Text\x1b[?25h",
			expected: "Text",
		},
		{
			name:     "Carriage return removal",
			input:    "Line1\r\nLine2",
			expected: "Line1\nLine2",
		},
		{
			name:     "OSC sequence with BEL",
			input:    "\x1b]0;Title\x07Content",
			expected: "Content",
		},
		{
			name:     "Multiple sequences",
			input:    "\x1b[32mGreen\x1b[0m and \x1b[34mBlue\x1b[0m",
			expected: "Green and Blue",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripANSI(tt.input)
			if result != tt.expected {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestConfigure_ClaudePTY(t *testing.T) {
	ch := New()
	ch.Configure(Config{Agent: AgentClaude})

	if !ch.config.UsePTY {
		t.Error("Claude should have UsePTY enabled by default")
	}
}

func TestSend_WithPTY(t *testing.T) {
	// Test PTY mode with echo command
	ch := NewWithConfig(Config{
		Agent:              AgentCustom,
		Command:            "echo",
		NonInteractiveFlag: "",
		UsePTY:             true,
		Timeout:            5 * time.Second,
	})

	result, err := ch.Send(context.Background(), "hello from PTY", "")
	if err != nil {
		t.Fatalf("Send with PTY failed: %v", err)
	}

	if result != "hello from PTY" {
		t.Errorf("Expected 'hello from PTY', got %q", result)
	}
}

// API Mode Tests

func TestIsAPIMode(t *testing.T) {
	t.Run("CLI mode", func(t *testing.T) {
		ch := NewWithConfig(Config{
			Agent: AgentClaude,
		})
		if ch.IsAPIMode() {
			t.Error("Expected CLI mode, got API mode")
		}
	})

	t.Run("API mode", func(t *testing.T) {
		ch := NewWithConfig(Config{
			UseAPI: true,
			APIKey: "test-key",
		})
		if !ch.IsAPIMode() {
			t.Error("Expected API mode, got CLI mode")
		}
	})
}

func TestConfigure_APIMode(t *testing.T) {
	ch := NewWithConfig(Config{
		UseAPI:       true,
		APIKey:       "test-key",
		Model:        "claude-haiku-3-5-20241022",
		MaxTokens:    512,
		SystemPrompt: "You are a test assistant.",
	})

	cfg := ch.Config()
	if !cfg.UseAPI {
		t.Error("UseAPI should be true")
	}
	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "test-key")
	}
	if cfg.Model != "claude-haiku-3-5-20241022" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-haiku-3-5-20241022")
	}
	if cfg.MaxTokens != 512 {
		t.Errorf("MaxTokens = %d, want %d", cfg.MaxTokens, 512)
	}
	if cfg.SystemPrompt != "You are a test assistant." {
		t.Errorf("SystemPrompt = %q, want %q", cfg.SystemPrompt, "You are a test assistant.")
	}
}

func TestIsAvailable_APIMode_Configured(t *testing.T) {
	ch := NewWithConfig(Config{
		UseAPI: true,
		APIKey: "test-key",
	})

	if !ch.IsAvailable() {
		t.Error("API mode channel with key should be available")
	}
}

func TestIsAvailable_APIMode_NoKey(t *testing.T) {
	// Save and unset ALL provider env vars
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

	ch := NewWithConfig(Config{
		UseAPI: true,
		// No APIKey provided and no env vars
	})

	if ch.IsAvailable() {
		t.Error("API mode channel without key should not be available")
	}
}

func TestSend_APIMode_NoProvider(t *testing.T) {
	// Create a channel without proper API setup
	ch := &Channel{
		config: Config{
			UseAPI: true,
		},
		configured: true,
		provider:   nil, // No provider
	}

	_, err := ch.Send(context.Background(), "test", "")
	if err == nil {
		t.Error("Expected error when provider is nil")
	}
}

func TestSendAndParse_APIMode_NoProvider(t *testing.T) {
	// Create a channel without proper API setup
	ch := &Channel{
		config: Config{
			UseAPI: true,
		},
		configured: true,
		provider:   nil, // No provider
	}

	_, err := ch.SendAndParse(context.Background(), "test", "")
	if err == nil {
		t.Error("Expected error when provider is nil")
	}
}

func TestCreateProvider(t *testing.T) {
	// Test with explicit provider
	ch := NewWithConfig(Config{
		UseAPI:      true,
		LLMProvider: ProviderOpenAI,
		APIKey:      "test-key",
		Model:       "gpt-4o-mini",
		MaxTokens:   512,
	})

	if ch.provider == nil {
		t.Fatal("Expected provider to be created")
	}

	if ch.provider.Name() != "openai" {
		t.Errorf("Provider name = %q, want %q", ch.provider.Name(), "openai")
	}
}

func TestCreateProvider_AutoDetect(t *testing.T) {
	// Test that provider is auto-detected when API keys are available
	ch := NewWithConfig(Config{
		UseAPI: true,
		APIKey: "test-key", // This won't be used if env vars are set
	})

	// Provider should be created if any env vars are set
	available := GetAvailableProviders()
	if len(available) > 0 {
		if ch.provider == nil {
			t.Fatal("Expected provider to be created when env vars are available")
		}
		// Should match default provider
		defaultProvider := GetDefaultProvider()
		if ch.provider.Name() != string(defaultProvider) {
			t.Logf("Provider auto-detected: %s (default: %s)", ch.provider.Name(), defaultProvider)
		}
	}
}
