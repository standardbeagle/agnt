package aichannel

import (
	"os"
	"testing"
)

func TestProviderRegistry(t *testing.T) {
	// Ensure all providers have required fields
	for provider, info := range providerRegistry {
		if len(info.EnvKeys) == 0 {
			t.Errorf("Provider %s has no env keys", provider)
		}
		if info.DefaultModel == "" {
			t.Errorf("Provider %s has no default model", provider)
		}
	}
}

func TestGetAPIKeyForProvider(t *testing.T) {
	// Test with no env vars set
	envVars := []string{"TEST_KEY_1", "TEST_KEY_2"}
	for _, key := range envVars {
		os.Unsetenv(key)
	}

	// Temporarily modify registry for testing
	origInfo := providerRegistry[ProviderOpenAI]
	providerRegistry[ProviderOpenAI] = ProviderInfo{
		EnvKeys:      envVars,
		DefaultModel: "test-model",
	}
	defer func() {
		providerRegistry[ProviderOpenAI] = origInfo
	}()

	// No key should be found
	key := GetAPIKeyForProvider(ProviderOpenAI)
	if key != "" {
		t.Errorf("Expected empty key, got %q", key)
	}

	// Set first env var
	os.Setenv("TEST_KEY_1", "first-key")
	defer os.Unsetenv("TEST_KEY_1")

	key = GetAPIKeyForProvider(ProviderOpenAI)
	if key != "first-key" {
		t.Errorf("Expected 'first-key', got %q", key)
	}
}

func TestIsProviderConfigured(t *testing.T) {
	// Save original values
	originals := make(map[string]string)
	for _, key := range []string{"OPENAI_KEY", "OPENAI_API_KEY"} {
		originals[key] = os.Getenv(key)
	}
	defer func() {
		for key, val := range originals {
			if val != "" {
				os.Setenv(key, val)
			} else {
				os.Unsetenv(key)
			}
		}
	}()

	// Check if OpenAI is currently configured (from .env)
	if IsProviderConfigured(ProviderOpenAI) {
		t.Log("OpenAI provider is configured via environment")
	}
}

func TestGetAvailableProviders(t *testing.T) {
	providers := GetAvailableProviders()
	t.Logf("Available providers: %v", providers)
}

func TestGetDefaultProvider(t *testing.T) {
	provider := GetDefaultProvider()
	if provider != "" {
		t.Logf("Default provider: %s", provider)
	} else {
		t.Log("No default provider available (no API keys set)")
	}
}

func TestNewLangChainProvider_UnknownProvider(t *testing.T) {
	_, err := NewLangChainProvider(LangChainConfig{
		Provider: "unknown-provider",
	})
	if err == nil {
		t.Error("Expected error for unknown provider")
	}
}

func TestNewLangChainProvider_NoAPIKey(t *testing.T) {
	// Save and unset env vars
	envVars := []string{"OPENAI_KEY", "OPENAI_API_KEY"}
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

	_, err := NewLangChainProvider(LangChainConfig{
		Provider: ProviderOpenAI,
		// No APIKey
	})
	if err == nil {
		t.Error("Expected error when no API key available")
	}
}

func TestNewLangChainProvider_WithExplicitKey(t *testing.T) {
	provider, err := NewLangChainProvider(LangChainConfig{
		Provider: ProviderOpenAI,
		APIKey:   "test-key",
		Model:    "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	if provider.Name() != "openai" {
		t.Errorf("Name() = %q, want %q", provider.Name(), "openai")
	}

	if !provider.IsConfigured() {
		t.Error("Provider should be configured")
	}

	if provider.Model() != "gpt-4o-mini" {
		t.Errorf("Model() = %q, want %q", provider.Model(), "gpt-4o-mini")
	}
}

func TestLangChainProvider_OpenAICompatible(t *testing.T) {
	// Test that OpenAI-compatible providers use the right base URL
	compatibleProviders := []struct {
		provider LLMProvider
		baseURL  string
	}{
		{ProviderDeepSeek, "https://api.deepseek.com/v1"},
		{ProviderOpenRouter, "https://openrouter.ai/api/v1"},
		{ProviderTogether, "https://api.together.xyz/v1"},
	}

	for _, tc := range compatibleProviders {
		info := providerRegistry[tc.provider]
		if !info.IsOpenAICompatible {
			t.Errorf("Provider %s should be OpenAI compatible", tc.provider)
		}
		if info.BaseURL != tc.baseURL {
			t.Errorf("Provider %s BaseURL = %q, want %q", tc.provider, info.BaseURL, tc.baseURL)
		}
	}
}
