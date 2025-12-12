// Package aichannel provides an interface for communicating with AI coding agents.
package aichannel

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicProvider implements the Provider interface using the Anthropic API.
type AnthropicProvider struct {
	client anthropic.Client
	config ProviderConfig
}

// Default Anthropic models
const (
	AnthropicModelSonnet = "claude-sonnet-4-5-20250929"
	AnthropicModelHaiku  = "claude-haiku-3-5-20241022"
	AnthropicModelOpus   = "claude-opus-4-5-20251101"
)

// NewAnthropicProvider creates a new Anthropic API provider.
// If apiKey is empty, it will try environment variables in order:
// ANTHROPIC_API_KEY, CLAUDE_KEY
func NewAnthropicProvider(config ProviderConfig) *AnthropicProvider {
	// Use environment variable if no API key provided
	apiKey := config.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("CLAUDE_KEY")
	}

	// Set defaults
	if config.Model == "" {
		config.Model = AnthropicModelSonnet
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 1024
	}

	// Build client options
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if config.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(config.BaseURL))
	}

	return &AnthropicProvider{
		client: anthropic.NewClient(opts...),
		config: config,
	}
}

// Name returns the provider name.
func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

// IsConfigured returns true if the provider has an API key.
func (p *AnthropicProvider) IsConfigured() bool {
	// Check if API key is available (either from config or env)
	return p.config.APIKey != "" ||
		os.Getenv("ANTHROPIC_API_KEY") != "" ||
		os.Getenv("CLAUDE_KEY") != ""
}

// Complete sends a prompt and returns the completion.
func (p *AnthropicProvider) Complete(ctx context.Context, systemPrompt, userPrompt string) (*Response, error) {
	return p.CompleteWithContext(ctx, systemPrompt, userPrompt, "")
}

// CompleteWithContext sends a prompt with additional context and returns the completion.
func (p *AnthropicProvider) CompleteWithContext(ctx context.Context, systemPrompt, userPrompt, inputContext string) (*Response, error) {
	if !p.IsConfigured() {
		return nil, ErrNoAPIKey
	}

	// Build user message content
	var userContent string
	if inputContext != "" {
		userContent = fmt.Sprintf("<context>\n%s\n</context>\n\n%s", inputContext, userPrompt)
	} else {
		userContent = userPrompt
	}

	// Build message params
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.config.Model),
		MaxTokens: int64(p.config.MaxTokens),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userContent)),
		},
	}

	// Add system prompt if provided
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	// Make the API call
	message, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderError, err)
	}

	// Extract text from response
	var resultText strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			resultText.WriteString(block.Text)
		}
	}

	return &Response{
		Result:    strings.TrimSpace(resultText.String()),
		SessionID: message.ID,
		// Anthropic doesn't provide cost directly, but we can estimate from usage
		// message.Usage.InputTokens and message.Usage.OutputTokens
	}, nil
}

// Model returns the configured model name.
func (p *AnthropicProvider) Model() string {
	return p.config.Model
}

// SetModel updates the model to use.
func (p *AnthropicProvider) SetModel(model string) {
	p.config.Model = model
}
