package overlay

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/aichannel"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// Summarizer aggregates system status and uses an AI channel to generate summaries.
// It uses a shared daemon.Conn for all requests.
type Summarizer struct {
	conn        *daemon.Conn
	channel     *aichannel.Channel
	debugOutput io.Writer
}

// SummarizerConfig configures the Summarizer.
type SummarizerConfig struct {
	// Agent type for AI summarization
	Agent aichannel.AgentType
	// Optional custom command (overrides agent default)
	Command string
	// Optional additional args
	Args []string
	// Timeout for AI response (default 2 minutes)
	Timeout time.Duration
	// DebugOutput is where debug messages are written (defaults to os.Stderr)
	DebugOutput io.Writer

	// --- API Mode Configuration ---
	// UseAPI forces API mode. For non-Claude agents, API mode is enabled automatically
	// since Claude Code Max plan only supports CLI, while other providers need API keys.
	// Set to true to force API mode even for Claude agent.
	UseAPI bool
	// LLMProvider specifies which provider to use (auto-detected if empty)
	LLMProvider aichannel.LLMProvider
	// APIKey overrides environment variable lookup
	APIKey string
	// Model overrides the provider's default model
	Model string
}

// NewSummarizer creates a new Summarizer using a shared connection.
// For Claude agent, uses CLI mode (required for Claude Code Max plan).
// For all other agents, automatically uses Anthropic API mode as a fallback
// since those CLIs may not be available or may require their own subscriptions.
func NewSummarizer(conn *daemon.Conn, config SummarizerConfig) *Summarizer {
	channelConfig := aichannel.Config{
		Agent:   config.Agent,
		Command: config.Command,
		Args:    config.Args,
		Timeout: config.Timeout,
		// OutputFormat will be set to "json" if agent supports it (in applyDefaults)
		// For agents that support JSON, we'll use it to extract just the final result
		OutputFormat: "json",
	}

	// Determine whether to use API mode:
	// - Claude agent: Use CLI mode (Claude Code Max plan only supports CLI)
	// - All other agents: Use Anthropic API mode as fallback
	useAPI := config.UseAPI
	if !useAPI && config.Agent != aichannel.AgentClaude {
		// Auto-enable API mode for non-Claude agents
		useAPI = true
	}

	if useAPI {
		channelConfig.UseAPI = true
		channelConfig.LLMProvider = config.LLMProvider
		channelConfig.APIKey = config.APIKey
		channelConfig.Model = config.Model
		// Set system prompt for API mode - this is the instruction prompt
		channelConfig.SystemPrompt = buildSummarySystemPrompt()
	}

	return &Summarizer{
		conn:        conn,
		channel:     aichannel.NewWithConfig(channelConfig),
		debugOutput: config.DebugOutput,
	}
}

// debugWriter returns the debug output writer, defaulting to os.Stderr.
func (s *Summarizer) debugWriter() io.Writer {
	if s.debugOutput != nil {
		return s.debugOutput
	}
	return os.Stderr
}

// IsAvailable returns true if the AI channel is available.
func (s *Summarizer) IsAvailable() bool {
	return s.channel.IsAvailable()
}

// AgentType returns the configured agent type.
func (s *Summarizer) AgentType() aichannel.AgentType {
	return s.channel.Config().Agent
}

// SummaryResult contains the result of a summarization.
type SummaryResult struct {
	Summary     string
	ProcessData []ProcessSummary
	ProxyData   []ProxySummary
	ErrorCount  int
	Duration    time.Duration
}

// ProcessSummary contains summarized info about a process.
type ProcessSummary struct {
	ID        string
	Command   string
	State     string
	HasErrors bool
	Output    string // Last N lines
}

// ProxySummary contains summarized info about a proxy.
type ProxySummary struct {
	ID         string
	TargetURL  string
	ListenAddr string
	ErrorCount int
	PageCount  int
	RecentLogs string // Recent log entries (errors, panel messages, etc.)
}

// Summarize aggregates all system data and generates a summary.
func (s *Summarizer) Summarize(ctx context.Context) (*SummaryResult, error) {
	start := time.Now()

	// Ensure connection is established
	if err := s.conn.EnsureConnected(); err != nil {
		return nil, fmt.Errorf("failed to connect to daemon: %w", err)
	}

	// Gather all data
	processes, err := s.gatherProcesses()
	if err != nil {
		return nil, fmt.Errorf("failed to gather process data: %w", err)
	}

	proxies, err := s.gatherProxies()
	if err != nil {
		return nil, fmt.Errorf("failed to gather proxy data: %w", err)
	}

	// Build context for AI
	contextData := s.buildContext(processes, proxies)

	// Generate summary via AI
	prompt := s.buildPrompt()

	// Log what we're about to do for debugging
	if s.channel.IsAPIMode() {
		config := s.channel.Config()
		provider := config.LLMProvider
		if provider == "" {
			provider = aichannel.GetDefaultProvider()
		}
		model := config.Model
		if model == "" {
			model = "default"
		}
		fmt.Fprintf(s.debugWriter(), "[agnt] Calling %s API (%s) with %d bytes of context...\r\n",
			provider, model, len(contextData))
	} else {
		fmt.Fprintf(s.debugWriter(), "[agnt] Calling %s with %d bytes of context...\r\n",
			s.channel.Config().Command, len(contextData))
	}

	// Use SendAndParse to get structured response with just the final result
	response, err := s.channel.SendAndParse(ctx, prompt, contextData)
	if err != nil {
		return nil, fmt.Errorf("AI summarization failed: %w", err)
	}

	summary := response.Result

	// Count errors
	errorCount := 0
	for _, p := range processes {
		if p.HasErrors {
			errorCount++
		}
	}
	for _, p := range proxies {
		errorCount += p.ErrorCount
	}

	return &SummaryResult{
		Summary:     summary,
		ProcessData: processes,
		ProxyData:   proxies,
		ErrorCount:  errorCount,
		Duration:    time.Since(start),
	}, nil
}

func (s *Summarizer) gatherProcesses() ([]ProcessSummary, error) {
	// Get process list using request builder
	result, err := s.conn.Request(protocol.VerbProc, protocol.SubVerbList).
		WithJSON(protocol.DirectoryFilter{Global: true}).
		JSON()
	if err != nil {
		return nil, err
	}

	processesRaw, ok := result["processes"].([]interface{})
	if !ok {
		return nil, nil
	}

	processes := make([]ProcessSummary, 0, len(processesRaw))
	for _, p := range processesRaw {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		summary := ProcessSummary{}
		if id, ok := pm["id"].(string); ok {
			summary.ID = id
		}
		if cmd, ok := pm["command"].(string); ok {
			summary.Command = cmd
		}
		if state, ok := pm["state"].(string); ok {
			summary.State = state
		}

		// Fetch last 50 lines of output
		if summary.ID != "" {
			output, err := s.conn.Request(protocol.VerbProc, protocol.SubVerbOutput, summary.ID).
				WithArgs("stream=combined", "tail=50").
				String()
			if err == nil {
				summary.Output = output
				// Check for error patterns in output
				summary.HasErrors = containsErrorPatterns(output)
			}
		}

		processes = append(processes, summary)
	}

	return processes, nil
}

func (s *Summarizer) gatherProxies() ([]ProxySummary, error) {
	// Get proxy list using request builder
	result, err := s.conn.Request(protocol.VerbProxy, protocol.SubVerbList).
		WithJSON(protocol.DirectoryFilter{Global: true}).
		JSON()
	if err != nil {
		return nil, err
	}

	proxiesRaw, ok := result["proxies"].([]interface{})
	if !ok {
		return nil, nil
	}

	proxies := make([]ProxySummary, 0, len(proxiesRaw))
	for _, p := range proxiesRaw {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		summary := ProxySummary{}
		if id, ok := pm["id"].(string); ok {
			summary.ID = id
		}
		if target, ok := pm["target_url"].(string); ok {
			summary.TargetURL = target
		}
		if listen, ok := pm["listen_addr"].(string); ok {
			summary.ListenAddr = listen
		}

		// Get stats
		if stats, ok := pm["stats"].(map[string]interface{}); ok {
			if errCount, ok := stats["error_count"].(float64); ok {
				summary.ErrorCount = int(errCount)
			}
		}

		// Get page count using request builder
		pagesResult, err := s.conn.Request(protocol.VerbCurrentPage, protocol.SubVerbList, summary.ID).JSON()
		if err == nil {
			if sessions, ok := pagesResult["sessions"].([]interface{}); ok {
				summary.PageCount = len(sessions)
			}
		}

		// Get recent proxy logs (errors, panel messages, custom logs)
		// Include multiple log types that are useful for summarization
		logFilter := protocol.LogQueryFilter{
			Types: []string{"error", "panel_message", "custom", "sketch"},
			Limit: 20, // Last 20 relevant entries
		}
		logsResult, err := s.conn.Request(protocol.VerbProxyLog, protocol.SubVerbQuery, summary.ID).
			WithJSON(logFilter).
			JSON()
		if err == nil {
			summary.RecentLogs = formatProxyLogs(logsResult)
		}

		proxies = append(proxies, summary)
	}

	return proxies, nil
}

// formatProxyLogs formats proxy log entries for the AI context.
func formatProxyLogs(result map[string]interface{}) string {
	entries, ok := result["entries"].([]interface{})
	if !ok || len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, entry := range entries {
		em, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}

		logType, _ := em["type"].(string)
		timestamp, _ := em["timestamp"].(string)

		switch logType {
		case "error":
			message, _ := em["message"].(string)
			sb.WriteString(fmt.Sprintf("  [ERROR %s] %s\n", timestamp, message))
		case "panel_message":
			message, _ := em["message"].(string)
			sb.WriteString(fmt.Sprintf("  [PANEL %s] %s\n", timestamp, message))
		case "custom":
			level, _ := em["level"].(string)
			message, _ := em["message"].(string)
			sb.WriteString(fmt.Sprintf("  [%s %s] %s\n", strings.ToUpper(level), timestamp, message))
		case "sketch":
			sb.WriteString(fmt.Sprintf("  [SKETCH %s] User created a sketch/wireframe\n", timestamp))
		}
	}

	return sb.String()
}

func (s *Summarizer) buildContext(processes []ProcessSummary, proxies []ProxySummary) string {
	var sb strings.Builder

	sb.WriteString("=== SYSTEM STATUS ===\n\n")

	// Processes section
	sb.WriteString("== PROCESSES ==\n")
	if len(processes) == 0 {
		sb.WriteString("No running processes.\n")
	} else {
		for _, p := range processes {
			sb.WriteString(fmt.Sprintf("\n--- Process: %s ---\n", p.ID))
			sb.WriteString(fmt.Sprintf("Command: %s\n", p.Command))
			sb.WriteString(fmt.Sprintf("State: %s\n", p.State))
			if p.Output != "" {
				sb.WriteString("Output (last 50 lines):\n")
				sb.WriteString(p.Output)
				sb.WriteString("\n")
			}
		}
	}

	// Proxies section
	sb.WriteString("\n== PROXIES ==\n")
	if len(proxies) == 0 {
		sb.WriteString("No running proxies.\n")
	} else {
		for _, p := range proxies {
			sb.WriteString(fmt.Sprintf("\n--- Proxy: %s ---\n", p.ID))
			sb.WriteString(fmt.Sprintf("Target: %s\n", p.TargetURL))
			sb.WriteString(fmt.Sprintf("Listen: %s\n", p.ListenAddr))
			sb.WriteString(fmt.Sprintf("Error Count: %d\n", p.ErrorCount))
			sb.WriteString(fmt.Sprintf("Active Pages: %d\n", p.PageCount))
			if p.RecentLogs != "" {
				sb.WriteString("Recent Logs (errors, messages):\n")
				sb.WriteString(p.RecentLogs)
			}
		}
	}

	return sb.String()
}

// buildSummarySystemPrompt returns the system prompt for API mode.
func buildSummarySystemPrompt() string {
	return `You are a concise system status summarizer.

CRITICAL RULES:
1. ONLY summarize the data provided to you - DO NOT scan files, explore the codebase, or use any external information
2. Be extremely concise (2-5 lines max) - this will be displayed in a small terminal indicator
3. Base your summary ENTIRELY on the process output and proxy logs provided

Format:
- If healthy: "✓ All systems OK" (single line)
- If issues: Brief bullet points, max 3-4 items

Focus ONLY on:
• Active errors or failures from the provided output
• Critical state changes visible in the data
• Actionable issues mentioned in the logs

DO NOT:
• Read or scan any files
• Explore the codebase
• Add explanations or context beyond what's in the data
• Include full stack traces
• Suggest fixes unless trivially obvious from the data
• Include process/proxy IDs unless relevant to an error

Example good response:
"✓ 2 processes running, 1 proxy active"

Example good error response:
"• test-server: EADDRINUSE port 3000
• proxy dev: 3 frontend errors"`
}

func (s *Summarizer) buildPrompt() string {
	// For API mode, the system prompt contains the instructions,
	// so we just need to ask for the analysis
	if s.channel.IsAPIMode() {
		return "Analyze ONLY the provided system status data and summarize. Do not scan files or explore the codebase."
	}

	// For CLI mode, include full instructions in the prompt
	return `Analyze the system status and provide a VERY BRIEF summary (2-5 lines max).

CRITICAL RULES:
1. ONLY summarize the data provided to you - DO NOT scan files, explore the codebase, or use any external information
2. Be extremely concise (2-5 lines max) - this will be displayed in a small terminal indicator
3. Base your summary ENTIRELY on the process output and proxy logs provided

Format:
- If healthy: "✓ All systems OK" (single line)
- If issues: Brief bullet points, max 3-4 items

Focus ONLY on:
• Active errors or failures from the provided output
• Critical state changes visible in the data
• Actionable issues mentioned in the logs

DO NOT:
• Read or scan any files
• Explore the codebase
• Add explanations or context beyond what's in the data
• Include full stack traces
• Suggest fixes unless trivially obvious from the data
• Include process/proxy IDs unless relevant to an error

Example good response:
"✓ 2 processes running, 1 proxy active"

Example good error response:
"• test-server: EADDRINUSE port 3000
• proxy dev: 3 frontend errors"`
}

// containsErrorPatterns checks if output contains common error patterns.
func containsErrorPatterns(output string) bool {
	lower := strings.ToLower(output)
	patterns := []string{
		"error",
		"exception",
		"failed",
		"panic",
		"fatal",
		"segfault",
		"timeout",
		"refused",
		"denied",
		"not found",
		"undefined",
		"null pointer",
		"stack trace",
		"traceback",
	}

	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
