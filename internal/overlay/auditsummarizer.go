package overlay

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/aichannel"
)

// AuditSummarizer uses an AI channel to generate high-quality audit reports.
type AuditSummarizer struct {
	channel *aichannel.Channel
	timeout time.Duration
}

// AuditSummarizerConfig configures the audit summarizer.
type AuditSummarizerConfig struct {
	// Agent type for AI summarization (default: uses API mode with Anthropic)
	Agent aichannel.AgentType
	// Optional custom command (overrides agent default)
	Command string
	// Optional additional args
	Args []string
	// Timeout for AI response (default 30 seconds)
	Timeout time.Duration

	// --- API Mode Configuration ---
	// UseAPI forces API mode (default: true for audit summarization)
	UseAPI bool
	// LLMProvider specifies which provider to use (auto-detected if empty)
	LLMProvider aichannel.LLMProvider
	// APIKey overrides environment variable lookup
	APIKey string
	// Model overrides the provider's default model
	Model string
}

// NewAuditSummarizer creates a new audit summarizer.
func NewAuditSummarizer(config AuditSummarizerConfig) *AuditSummarizer {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Default to API mode for audit summarization (faster than CLI)
	useAPI := config.UseAPI
	if !useAPI && config.Agent == "" {
		useAPI = true
	}

	channelConfig := aichannel.Config{
		Agent:        config.Agent,
		Command:      config.Command,
		Args:         config.Args,
		Timeout:      timeout,
		UseAPI:       useAPI,
		LLMProvider:  config.LLMProvider,
		APIKey:       config.APIKey,
		Model:        config.Model,
		SystemPrompt: buildAuditSystemPrompt(),
	}

	return &AuditSummarizer{
		channel: aichannel.NewWithConfig(channelConfig),
		timeout: timeout,
	}
}

// IsAvailable returns true if the AI channel is available.
func (s *AuditSummarizer) IsAvailable() bool {
	return s.channel.IsAvailable()
}

// AuditData represents the audit data to summarize.
type AuditData struct {
	AuditType string          `json:"auditType"`
	Label     string          `json:"label"`
	Summary   string          `json:"summary"`
	Result    json.RawMessage `json:"result"`
}

// SummarizeAudit generates a high-quality report from audit data.
func (s *AuditSummarizer) SummarizeAudit(ctx context.Context, audit AuditData, userMessage string) (string, error) {
	if !s.IsAvailable() {
		// Fallback: return a basic formatted summary
		return s.fallbackSummary(audit, userMessage), nil
	}

	// Build context with audit data
	contextData := s.buildAuditContext(audit)

	// Build the prompt
	prompt := s.buildPrompt(audit.AuditType, userMessage)

	// Use timeout from config
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	response, err := s.channel.SendAndParse(ctx, prompt, contextData)
	if err != nil {
		// On error, fallback to basic summary
		return s.fallbackSummary(audit, userMessage), nil
	}

	return response.Result, nil
}

// buildAuditSystemPrompt returns the system prompt for audit summarization.
func buildAuditSystemPrompt() string {
	return `You are an expert web accessibility and security auditor. Your role is to analyze audit results and produce clear, actionable reports for AI coding assistants.

CRITICAL RULES:
1. Be concise but thorough - the report should be 5-15 lines max
2. Prioritize by severity: critical > serious > moderate > minor
3. Focus on actionable fixes, not just problem descriptions
4. Include specific selectors/elements when available
5. Group related issues together
6. Never include raw JSON - always produce human-readable prose

OUTPUT FORMAT:
Start with a grade/severity assessment (1 line)
Then list top 3-5 issues with:
- Issue type and count
- Specific fix instructions
- Target selector if available

Example good output:
"⚠️ ACCESSIBILITY: Grade C - 3 critical issues require immediate attention

1. **Missing alt text** (4 instances) - images lack descriptions
   Fix: Add descriptive alt attributes to <img> elements
   Targets: img.hero-image, img.product-thumb

2. **Color contrast** (2 instances) - text fails WCAG AA
   Fix: Increase contrast ratio to 4.5:1 minimum
   Targets: .subtitle, .footer-text

3. **Missing form labels** (1 instance)
   Fix: Associate <label> elements with form inputs
   Target: input#email"

Keep the report focused and actionable. The AI receiving this will implement fixes.`
}

func (s *AuditSummarizer) buildAuditContext(audit AuditData) string {
	// Pretty-print the audit result for context
	var prettyResult []byte
	if len(audit.Result) > 0 {
		var parsed interface{}
		if err := json.Unmarshal(audit.Result, &parsed); err == nil {
			prettyResult, _ = json.MarshalIndent(parsed, "", "  ")
		} else {
			prettyResult = audit.Result
		}
	}

	// Limit context size to avoid token overflow
	resultStr := string(prettyResult)
	if len(resultStr) > 8000 {
		resultStr = resultStr[:8000] + "\n... (truncated)"
	}

	return fmt.Sprintf(`=== AUDIT DATA ===
Type: %s
Label: %s
Quick Summary: %s

Full Results:
%s`, audit.AuditType, audit.Label, audit.Summary, resultStr)
}

func (s *AuditSummarizer) buildPrompt(auditType, userMessage string) string {
	base := fmt.Sprintf("Analyze this %s audit and produce a clear, actionable report.", auditType)
	if userMessage != "" {
		base += fmt.Sprintf("\n\nUser's request context: %s", userMessage)
	}
	return base
}

func (s *AuditSummarizer) fallbackSummary(audit AuditData, userMessage string) string {
	// Basic fallback when LLM is not available
	text := fmt.Sprintf("**%s Audit**: %s", audit.Label, audit.Summary)
	if userMessage != "" {
		text = userMessage + "\n\n" + text
	}
	return text
}
