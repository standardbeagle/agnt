package aichannel

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNewAnthropicProvider(t *testing.T) {
	provider := NewAnthropicProvider(ProviderConfig{})

	if provider == nil {
		t.Fatal("Expected non-nil provider")
	}

	if provider.Name() != "anthropic" {
		t.Errorf("Expected name 'anthropic', got %q", provider.Name())
	}
}

func TestAnthropicProvider_DefaultModel(t *testing.T) {
	provider := NewAnthropicProvider(ProviderConfig{})

	if provider.Model() != AnthropicModelSonnet {
		t.Errorf("Expected default model %q, got %q", AnthropicModelSonnet, provider.Model())
	}
}

func TestAnthropicProvider_CustomModel(t *testing.T) {
	provider := NewAnthropicProvider(ProviderConfig{
		Model: AnthropicModelHaiku,
	})

	if provider.Model() != AnthropicModelHaiku {
		t.Errorf("Expected model %q, got %q", AnthropicModelHaiku, provider.Model())
	}
}

func TestAnthropicProvider_SetModel(t *testing.T) {
	provider := NewAnthropicProvider(ProviderConfig{})

	provider.SetModel(AnthropicModelOpus)

	if provider.Model() != AnthropicModelOpus {
		t.Errorf("Expected model %q, got %q", AnthropicModelOpus, provider.Model())
	}
}

func TestAnthropicProvider_IsConfigured_NoKey(t *testing.T) {
	// Temporarily unset the env var
	original := os.Getenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_API_KEY")
	defer func() {
		if original != "" {
			os.Setenv("ANTHROPIC_API_KEY", original)
		}
	}()

	provider := NewAnthropicProvider(ProviderConfig{})

	if provider.IsConfigured() {
		t.Error("Expected IsConfigured() to return false when no API key is set")
	}
}

func TestAnthropicProvider_IsConfigured_WithKey(t *testing.T) {
	provider := NewAnthropicProvider(ProviderConfig{
		APIKey: "test-key",
	})

	if !provider.IsConfigured() {
		t.Error("Expected IsConfigured() to return true when API key is provided")
	}
}

func TestAnthropicProvider_IsConfigured_EnvKey(t *testing.T) {
	// Set env var temporarily
	original := os.Getenv("ANTHROPIC_API_KEY")
	os.Setenv("ANTHROPIC_API_KEY", "test-env-key")
	defer func() {
		if original != "" {
			os.Setenv("ANTHROPIC_API_KEY", original)
		} else {
			os.Unsetenv("ANTHROPIC_API_KEY")
		}
	}()

	provider := NewAnthropicProvider(ProviderConfig{})

	if !provider.IsConfigured() {
		t.Error("Expected IsConfigured() to return true when ANTHROPIC_API_KEY env var is set")
	}
}

func TestAnthropicProvider_Complete_NoAPIKey(t *testing.T) {
	// Temporarily unset the env var
	original := os.Getenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_API_KEY")
	defer func() {
		if original != "" {
			os.Setenv("ANTHROPIC_API_KEY", original)
		}
	}()

	provider := NewAnthropicProvider(ProviderConfig{})

	_, err := provider.Complete(context.Background(), "system", "prompt")
	if err != ErrNoAPIKey {
		t.Errorf("Expected ErrNoAPIKey, got %v", err)
	}
}

func TestAnthropicProvider_CompleteWithContext_NoAPIKey(t *testing.T) {
	// Temporarily unset the env var
	original := os.Getenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_API_KEY")
	defer func() {
		if original != "" {
			os.Setenv("ANTHROPIC_API_KEY", original)
		}
	}()

	provider := NewAnthropicProvider(ProviderConfig{})

	_, err := provider.CompleteWithContext(context.Background(), "system", "prompt", "context")
	if err != ErrNoAPIKey {
		t.Errorf("Expected ErrNoAPIKey, got %v", err)
	}
}

// TestAnthropicProvider_Integration tests real API calls.
// Skip unless ANTHROPIC_API_KEY is set and CI is not set.
func TestAnthropicProvider_Integration(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}
	if os.Getenv("CI") != "" {
		t.Skip("Skipping integration test in CI")
	}

	t.Run("Complete", func(t *testing.T) {
		t.Skip("Manual test - uncomment to run with real API")

		provider := NewAnthropicProvider(ProviderConfig{
			Model: AnthropicModelHaiku, // Use cheapest model for tests
		})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp, err := provider.Complete(ctx, "You are a helpful assistant.", "Say 'hello' and nothing else.")
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}

		if resp.Result == "" {
			t.Error("Expected non-empty result")
		}

		t.Logf("Result: %s", resp.Result)
		t.Logf("SessionID: %s", resp.SessionID)
	})

	t.Run("CompleteWithContext", func(t *testing.T) {
		t.Skip("Manual test - uncomment to run with real API")

		provider := NewAnthropicProvider(ProviderConfig{
			Model: AnthropicModelHaiku,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		resp, err := provider.CompleteWithContext(ctx,
			"You are a helpful assistant.",
			"Summarize the context.",
			"This is a test context with some information.")
		if err != nil {
			t.Fatalf("CompleteWithContext() error = %v", err)
		}

		if resp.Result == "" {
			t.Error("Expected non-empty result")
		}

		t.Logf("Result: %s", resp.Result)
	})
}

func TestAnthropicModelConstants(t *testing.T) {
	// Verify model constants are properly defined
	models := []string{
		AnthropicModelSonnet,
		AnthropicModelHaiku,
		AnthropicModelOpus,
	}

	for _, model := range models {
		if model == "" {
			t.Error("Model constant should not be empty")
		}
		if len(model) < 10 {
			t.Errorf("Model %q seems too short to be valid", model)
		}
	}
}
