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
	overrides := map[string]agentadapter.Override{}
	if globalCfg, err := config.LoadGlobalConfig(); err != nil {
		debug.Log("run", "global adapter override load failed: %v", err)
	} else if globalCfg != nil && globalCfg.AI != nil && len(globalCfg.AI.Adapters) > 0 {
		mergeAdapterOverrides(overrides, adapterOverridesFromConfig(globalCfg.AI.Adapters))
	}
	cfg, err := loadResolvedConfig(projectPath)
	if err != nil {
		debug.Log("run", "adapter override load failed: %v", err)
	} else if cfg != nil && cfg.AI != nil && len(cfg.AI.Adapters) > 0 {
		mergeAdapterOverrides(overrides, adapterOverridesFromConfig(cfg.AI.Adapters))
	}
	if len(overrides) > 0 {
		registry.SetOverrides(overrides)
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

func mergeAdapterOverrides(dst, src map[string]agentadapter.Override) {
	for name, next := range src {
		cur := dst[name]
		cur.Disabled = cur.Disabled || next.Disabled
		if next.FlagName != "" {
			cur.FlagName = next.FlagName
		}
		if next.StdinDelay > 0 {
			cur.StdinDelay = next.StdinDelay
		}
		cur.Aliases = appendUniqueStrings(cur.Aliases, next.Aliases...)
		dst[name] = cur
	}
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	out := make([]string, 0, len(dst)+len(values))
	for _, v := range append(dst, values...) {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
