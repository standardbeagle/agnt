// Package aichannel provides an interface for communicating with AI coding agents.
package aichannel

import (
	"context"
	"fmt"
	"os"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/mistral"
	"github.com/tmc/langchaingo/llms/openai"
)

// LLMProvider represents a supported LLM provider type.
type LLMProvider string

const (
	ProviderOpenAI     LLMProvider = "openai"
	ProviderAnthropic  LLMProvider = "anthropic"
	ProviderGoogle     LLMProvider = "google"
	ProviderMistral    LLMProvider = "mistral"
	ProviderDeepSeek   LLMProvider = "deepseek"
	ProviderOpenRouter LLMProvider = "openrouter"
	ProviderTogether   LLMProvider = "together"
	ProviderHyperbolic LLMProvider = "hyperbolic"
	ProviderReplicate  LLMProvider = "replicate"
	ProviderSambaNova  LLMProvider = "sambanova"
	ProviderGLM        LLMProvider = "glm"
)

// ProviderInfo contains configuration for a provider.
type ProviderInfo struct {
	// EnvKeys are environment variable names to check for API key (in order)
	EnvKeys []string
	// BaseURL for OpenAI-compatible providers
	BaseURL string
	// DefaultModel is the default model to use
	DefaultModel string
	// IsOpenAICompatible indicates if this uses the OpenAI API format
	IsOpenAICompatible bool
}

// providerRegistry maps providers to their configuration.
var providerRegistry = map[LLMProvider]ProviderInfo{
	ProviderOpenAI: {
		EnvKeys:            []string{"OPENAI_KEY", "OPENAI_API_KEY"},
		DefaultModel:       "gpt-4o-mini",
		IsOpenAICompatible: true,
	},
	ProviderAnthropic: {
		EnvKeys:      []string{"CLAUDE_KEY", "ANTHROPIC_API_KEY"},
		DefaultModel: "claude-sonnet-4-5-20250929",
	},
	ProviderGoogle: {
		EnvKeys:      []string{"GOOGLE_KEY", "GOOGLE_API_KEY"},
		DefaultModel: "gemini-1.5-flash",
	},
	ProviderMistral: {
		EnvKeys:      []string{"MISTRAL_KEY", "MISTRAL_API_KEY"},
		DefaultModel: "mistral-small-latest",
	},
	ProviderDeepSeek: {
		EnvKeys:            []string{"DEEP_SEEK_KEY", "DEEPSEEK_API_KEY"},
		BaseURL:            "https://api.deepseek.com/v1",
		DefaultModel:       "deepseek-chat",
		IsOpenAICompatible: true,
	},
	ProviderOpenRouter: {
		EnvKeys:            []string{"OPEN_ROUTER_KEY", "OPENROUTER_API_KEY"},
		BaseURL:            "https://openrouter.ai/api/v1",
		DefaultModel:       "anthropic/claude-3.5-sonnet",
		IsOpenAICompatible: true,
	},
	ProviderTogether: {
		EnvKeys:            []string{"TOGETHER_KEY", "TOGETHER_API_KEY"},
		BaseURL:            "https://api.together.xyz/v1",
		DefaultModel:       "meta-llama/Llama-3-70b-chat-hf",
		IsOpenAICompatible: true,
	},
	ProviderHyperbolic: {
		EnvKeys:            []string{"HYPERBOLIC_KEY", "HYPERBOLIC_API_KEY"},
		BaseURL:            "https://api.hyperbolic.xyz/v1",
		DefaultModel:       "meta-llama/Llama-3.3-70B-Instruct",
		IsOpenAICompatible: true,
	},
	ProviderSambaNova: {
		EnvKeys:            []string{"SAMBA_NOVA_KEY", "SAMBANOVA_API_KEY"},
		BaseURL:            "https://api.sambanova.ai/v1",
		DefaultModel:       "Meta-Llama-3.1-8B-Instruct",
		IsOpenAICompatible: true,
	},
	ProviderGLM: {
		EnvKeys:            []string{"GLM_KEY", "GLM_API_KEY"},
		BaseURL:            "https://open.bigmodel.cn/api/paas/v4",
		DefaultModel:       "glm-4-flash",
		IsOpenAICompatible: true,
	},
}

// LangChainProvider implements the Provider interface using langchaingo.
type LangChainProvider struct {
	llm      llms.Model
	provider LLMProvider
	model    string
	apiKey   string
}

// LangChainConfig configures a LangChain provider.
type LangChainConfig struct {
	// Provider is the LLM provider to use
	Provider LLMProvider
	// APIKey overrides environment variable lookup
	APIKey string
	// Model overrides the default model
	Model string
	// MaxTokens limits response length
	MaxTokens int
}

// NewLangChainProvider creates a new LangChain-based provider.
func NewLangChainProvider(config LangChainConfig) (*LangChainProvider, error) {
	info, ok := providerRegistry[config.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", config.Provider)
	}

	// Find API key
	apiKey := config.APIKey
	if apiKey == "" {
		for _, envKey := range info.EnvKeys {
			apiKey = os.Getenv(envKey)
			if apiKey != "" {
				break
			}
		}
	}

	if apiKey == "" {
		return nil, fmt.Errorf("%w: no API key found for %s (tried: %v)", ErrNoAPIKey, config.Provider, info.EnvKeys)
	}

	// Determine model
	model := config.Model
	if model == "" {
		model = info.DefaultModel
	}

	// Create the LLM
	var llm llms.Model
	var err error

	if info.IsOpenAICompatible {
		opts := []openai.Option{
			openai.WithToken(apiKey),
			openai.WithModel(model),
		}
		if info.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(info.BaseURL))
		}
		llm, err = openai.New(opts...)
	} else {
		switch config.Provider {
		case ProviderAnthropic:
			llm, err = anthropic.New(
				anthropic.WithToken(apiKey),
				anthropic.WithModel(model),
			)
		case ProviderGoogle:
			llm, err = googleai.New(
				context.Background(),
				googleai.WithAPIKey(apiKey),
				googleai.WithDefaultModel(model),
			)
		case ProviderMistral:
			llm, err = mistral.New(
				mistral.WithAPIKey(apiKey),
				mistral.WithModel(model),
			)
		default:
			return nil, fmt.Errorf("provider %s not implemented", config.Provider)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create %s LLM: %w", config.Provider, err)
	}

	return &LangChainProvider{
		llm:      llm,
		provider: config.Provider,
		model:    model,
		apiKey:   apiKey,
	}, nil
}

// Name returns the provider name.
func (p *LangChainProvider) Name() string {
	return string(p.provider)
}

// IsConfigured returns true if the provider has an API key.
func (p *LangChainProvider) IsConfigured() bool {
	return p.apiKey != ""
}

// Complete sends a prompt and returns the completion.
func (p *LangChainProvider) Complete(ctx context.Context, systemPrompt, userPrompt string) (*Response, error) {
	return p.CompleteWithContext(ctx, systemPrompt, userPrompt, "")
}

// CompleteWithContext sends a prompt with additional context and returns the completion.
func (p *LangChainProvider) CompleteWithContext(ctx context.Context, systemPrompt, userPrompt, inputContext string) (*Response, error) {
	// Build messages
	var messages []llms.MessageContent

	if systemPrompt != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt))
	}

	// Build user message with optional context
	userContent := userPrompt
	if inputContext != "" {
		userContent = fmt.Sprintf("<context>\n%s\n</context>\n\n%s", inputContext, userPrompt)
	}
	messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, userContent))

	// Call the LLM
	resp, err := p.llm.GenerateContent(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderError, err)
	}

	// Extract text from response
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("%w: no response choices", ErrProviderError)
	}

	return &Response{
		Result: resp.Choices[0].Content,
	}, nil
}

// Model returns the configured model name.
func (p *LangChainProvider) Model() string {
	return p.model
}

// GetAPIKeyForProvider returns the API key for a provider from environment variables.
func GetAPIKeyForProvider(provider LLMProvider) string {
	info, ok := providerRegistry[provider]
	if !ok {
		return ""
	}
	for _, envKey := range info.EnvKeys {
		if key := os.Getenv(envKey); key != "" {
			return key
		}
	}
	return ""
}

// IsProviderConfigured checks if a provider has an API key available.
func IsProviderConfigured(provider LLMProvider) bool {
	return GetAPIKeyForProvider(provider) != ""
}

// GetAvailableProviders returns a list of providers that have API keys configured.
func GetAvailableProviders() []LLMProvider {
	var available []LLMProvider
	for provider := range providerRegistry {
		if IsProviderConfigured(provider) {
			available = append(available, provider)
		}
	}
	return available
}

// GetDefaultProvider returns the first available provider (preference order).
func GetDefaultProvider() LLMProvider {
	// Preference order for supporting roles (not Claude CLI)
	preferenceOrder := []LLMProvider{
		ProviderOpenAI,     // Most reliable
		ProviderGoogle,     // Good alternative
		ProviderDeepSeek,   // Good for code
		ProviderOpenRouter, // Fallback with many models
		ProviderTogether,
		ProviderHyperbolic,
		ProviderSambaNova,
		ProviderGLM,
		ProviderAnthropic, // Last since user has Claude CLI
		ProviderMistral,   // At the end per user preference
	}

	for _, provider := range preferenceOrder {
		if IsProviderConfigured(provider) {
			return provider
		}
	}
	return ""
}
