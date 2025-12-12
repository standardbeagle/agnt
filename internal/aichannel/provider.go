// Package aichannel provides an interface for communicating with AI coding agents.
package aichannel

import (
	"context"
	"errors"
)

// Common errors for providers
var (
	ErrNoAPIKey      = errors.New("API key not configured")
	ErrProviderError = errors.New("provider error")
)

// Provider represents an LLM provider that can generate completions.
type Provider interface {
	// Name returns the provider name (e.g., "anthropic", "openai")
	Name() string

	// Complete sends a prompt and returns the completion.
	// The systemPrompt provides context/instructions, userPrompt is the actual query.
	Complete(ctx context.Context, systemPrompt, userPrompt string) (*Response, error)

	// CompleteWithContext is like Complete but allows passing additional context via stdin-like input.
	CompleteWithContext(ctx context.Context, systemPrompt, userPrompt, inputContext string) (*Response, error)

	// IsConfigured returns true if the provider has necessary credentials.
	IsConfigured() bool
}

// ProviderConfig holds common configuration for API-based providers.
type ProviderConfig struct {
	// APIKey is the authentication key for the provider
	APIKey string `json:"api_key,omitempty"`

	// Model is the model to use (e.g., "claude-sonnet-4-5-20250929")
	Model string `json:"model,omitempty"`

	// MaxTokens limits the response length
	MaxTokens int `json:"max_tokens,omitempty"`

	// Temperature controls randomness (0.0-1.0)
	Temperature float64 `json:"temperature,omitempty"`

	// BaseURL overrides the default API endpoint (for proxies/self-hosted)
	BaseURL string `json:"base_url,omitempty"`
}

// DefaultProviderConfig returns sensible defaults for provider configuration.
func DefaultProviderConfig() ProviderConfig {
	return ProviderConfig{
		MaxTokens:   1024,
		Temperature: 0.7,
	}
}
