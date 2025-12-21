package aichannel

import (
	"strings"
	"testing"
	"time"

	gocontext "context"
)

func TestGetAdapter(t *testing.T) {
	tests := []struct {
		agent    AgentType
		wantType string
	}{
		{AgentClaude, "*aichannel.ClaudeAdapter"},
		{AgentCopilot, "*aichannel.CopilotAdapter"},
		{AgentGemini, "*aichannel.GeminiAdapter"},
		{AgentAider, "*aichannel.AiderAdapter"},
		{AgentOpenCode, "*aichannel.GenericAdapter"},
		{AgentKimi, "*aichannel.GenericAdapter"},
	}

	for _, tt := range tests {
		t.Run(string(tt.agent), func(t *testing.T) {
			adapter := GetAdapter(tt.agent)
			if adapter == nil {
				t.Fatal("GetAdapter returned nil")
			}
		})
	}
}

func TestClaudeAdapter_BuildCommand(t *testing.T) {
	adapter := &ClaudeAdapter{}
	cfg := Config{
		Command: "claude",
		Model:   "haiku",
		Timeout: 30 * time.Second,
	}

	prompt := "Analyze this"
	inputContext := "some context data"
	systemPrompt := "You are a helpful assistant"

	cmd, err := adapter.BuildCommand(gocontext.Background(), cfg, prompt, inputContext, systemPrompt)
	if err != nil {
		t.Fatalf("BuildCommand failed: %v", err)
	}

	// Check command path
	if cmd.Path == "" {
		t.Error("Command path is empty")
	}

	// Check args contain key elements
	args := strings.Join(cmd.Args, " ")

	if !strings.Contains(args, "--model haiku") {
		t.Error("Args should contain --model haiku")
	}

	if !strings.Contains(args, "--system-prompt") {
		t.Error("Args should contain --system-prompt")
	}

	if !strings.Contains(args, "-p") {
		t.Error("Args should contain -p flag")
	}
}

func TestClaudeAdapter_BuildFullPrompt(t *testing.T) {
	adapter := &ClaudeAdapter{}

	tests := []struct {
		name         string
		prompt       string
		inputContext string
		wantContains []string
	}{
		{
			name:         "no context",
			prompt:       "Hello",
			inputContext: "",
			wantContains: []string{"Hello"},
		},
		{
			name:         "with context",
			prompt:       "Summarize this",
			inputContext: "some data here",
			wantContains: []string{"<context>", "some data here", "</context>", "Summarize this"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.buildFullPrompt(tt.prompt, tt.inputContext)
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("Result should contain %q, got: %s", want, result)
				}
			}
		})
	}
}

func TestClaudeAdapter_RequiresPTY(t *testing.T) {
	adapter := &ClaudeAdapter{}
	if !adapter.RequiresPTY() {
		t.Error("ClaudeAdapter should require PTY")
	}
}

func TestClaudeAdapter_StdinData(t *testing.T) {
	adapter := &ClaudeAdapter{}
	config := Config{}

	// Claude adapter should never use stdin (all data goes in -p)
	data := adapter.StdinData(config, "prompt", "context", "system")
	if data != "" {
		t.Errorf("ClaudeAdapter StdinData should be empty, got: %s", data)
	}
}

func TestGenericAdapter_RequiresPTY(t *testing.T) {
	adapter := &GenericAdapter{agentType: AgentOpenCode}
	if adapter.RequiresPTY() {
		t.Error("GenericAdapter should not require PTY")
	}
}

func TestGenericAdapter_StdinData(t *testing.T) {
	adapter := &GenericAdapter{agentType: AgentCustom}

	// With UseStdin true, should return context
	config := Config{UseStdin: true}
	data := adapter.StdinData(config, "prompt", "my context", "system")
	if data != "my context" {
		t.Errorf("Expected 'my context', got: %s", data)
	}

	// With UseStdin false, should return empty
	config = Config{UseStdin: false}
	data = adapter.StdinData(config, "prompt", "my context", "system")
	if data != "" {
		t.Errorf("Expected empty string, got: %s", data)
	}
}

func TestCopilotAdapter_BuildFullPrompt(t *testing.T) {
	adapter := &CopilotAdapter{}

	result := adapter.buildFullPrompt("Do this", "here is context")

	if !strings.Contains(result, "Context:") {
		t.Error("Copilot prompt should include 'Context:'")
	}
	if !strings.Contains(result, "Request:") {
		t.Error("Copilot prompt should include 'Request:'")
	}
}

func TestGeminiAdapter_BuildFullPrompt(t *testing.T) {
	adapter := &GeminiAdapter{}

	result := adapter.buildFullPrompt("Analyze", "data here")

	if !strings.Contains(result, "<context>") {
		t.Error("Gemini prompt should use <context> tags")
	}
	if !strings.Contains(result, "</context>") {
		t.Error("Gemini prompt should close context tag")
	}
}

func TestAiderAdapter_RequiresPTY(t *testing.T) {
	adapter := &AiderAdapter{}
	if adapter.RequiresPTY() {
		t.Error("AiderAdapter should not require PTY")
	}
}

func TestAdapterParseOutput(t *testing.T) {
	tests := []struct {
		name    string
		adapter AgentAdapter
		input   string
		want    string
	}{
		{
			name:    "claude strips ANSI",
			adapter: &ClaudeAdapter{},
			input:   "\x1b[31mred\x1b[0m text",
			want:    "red text",
		},
		{
			name:    "generic trims whitespace",
			adapter: &GenericAdapter{},
			input:   "  hello world  \n",
			want:    "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.adapter.ParseOutput(tt.input)
			if got != tt.want {
				t.Errorf("ParseOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}
