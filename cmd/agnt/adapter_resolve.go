package main

import (
	"time"

	"github.com/standardbeagle/agnt/internal/agentadapter"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// resolveAgentAdapter builds an [agentadapter.Adapter] for command,
// loading per-agent overrides from `.agnt.kdl` via the `ai.adapters`
// block. Returns nil when command does not match any registered agent.
//
// Config load errors are non-fatal — the config was already validated
// at startup, so failures here fall back to the default registry.
//
// Shared between the unix (`run.go`) and windows (`run_windows.go`)
// entrypoints; neither file carries a build tag that excludes this
// helper, so keeping it in one place avoids drift between platforms.
func resolveAgentAdapter(command, projectPath string) agentadapter.Adapter {
	registry := agentadapter.DefaultRegistry()
	cfg, err := loadResolvedConfig(projectPath)
	if err != nil {
		debug.Log("run", "adapter override load failed: %v", err)
	} else if cfg != nil && cfg.AI != nil && len(cfg.AI.Adapters) > 0 {
		registry.SetOverrides(adapterOverridesFromConfig(cfg.AI.Adapters))
	}
	// Verb-driven: any command launched under `agnt run` gets agnt prompt
	// injection. An unrecognized command falls back to the universal
	// stdin-based adapter rather than returning nil (which would silently
	// inject nothing).
	if adapter := registry.Lookup(command); adapter != nil {
		return adapter
	}
	return agentadapter.Universal(command)
}

// adapterOverridesFromConfig converts the KDL-parsed `ai.adapters` map
// into the internal [agentadapter.Override] map. Nil entries are
// skipped so a bare `adapters { claude {} }` block doesn't crash.
func adapterOverridesFromConfig(in map[string]*config.AIAdapterConfig) map[string]agentadapter.Override {
	out := make(map[string]agentadapter.Override, len(in))
	for name, cfg := range in {
		if cfg == nil {
			continue
		}
		ov := agentadapter.Override{
			Disabled: cfg.Disabled,
			FlagName: cfg.FlagName,
			Aliases:  cfg.Aliases,
		}
		if cfg.StdinDelayMs > 0 {
			ov.StdinDelay = time.Duration(cfg.StdinDelayMs) * time.Millisecond
		}
		out[name] = ov
	}
	return out
}
