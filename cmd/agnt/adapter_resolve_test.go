package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAgentAdapter_ConfigAliasUsesClaudeAdapter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(func() { configOverride = "" })

	kdl := `ai {
    adapters {
        claude {
            aliases "cdsp"
        }
    }
}`
	if err := os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := resolveAgentAdapter("cdsp", dir)
	if adapter == nil {
		t.Fatal("resolveAgentAdapter(cdsp) = nil, want claude adapter")
	}
	if adapter.Name() != "claude" {
		t.Fatalf("resolveAgentAdapter(cdsp).Name() = %q, want claude", adapter.Name())
	}

	args := adapter.BuildArgs([]string{"--model", "sonnet"}, "PROMPT")
	if len(args) < 2 || args[len(args)-2] != "--append-system-prompt" || args[len(args)-1] != "PROMPT" {
		t.Fatalf("aliased claude must inject via flag, got %v", args)
	}
	if stdin := adapter.InitialStdin("PROMPT"); stdin != nil {
		t.Fatalf("aliased claude must not emit stdin, got %q", string(stdin))
	}
}

func TestResolveAgentAdapter_GlobalConfigAliasUsesClaudeAdapter(t *testing.T) {
	projectDir := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Cleanup(func() { configOverride = "" })

	configDir := filepath.Join(configHome, "agnt")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kdl := `ai {
    adapters {
        claude {
            aliases "cdsp"
        }
    }
}`
	if err := os.WriteFile(filepath.Join(configDir, "config.kdl"), []byte(kdl), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := resolveAgentAdapter("cdsp", projectDir)
	if adapter == nil {
		t.Fatal("resolveAgentAdapter(cdsp) = nil, want claude adapter")
	}
	if adapter.Name() != "claude" {
		t.Fatalf("resolveAgentAdapter(cdsp).Name() = %q, want claude", adapter.Name())
	}
	if stdin := adapter.InitialStdin("PROMPT"); stdin != nil {
		t.Fatalf("global alias must use Claude flag injection, got stdin %q", string(stdin))
	}
}
