package agentadapter

import (
	"os/exec"
	"strings"
	"time"
)

// Override captures per-agent configuration overrides, typically sourced
// from the `ai.adapters` block in `.agnt.kdl`. Zero values mean "inherit
// the adapter's default".
type Override struct {
	// Disabled, when true, disables prompt injection for this agent
	// entirely — the adapter's BuildArgs returns baseArgs unchanged and
	// InitialStdin returns nil.
	Disabled bool

	// FlagName overrides the CLI flag used for flag-based injection
	// (e.g. "--system-prompt" instead of "--append-system-prompt").
	// Empty means "use the adapter default". Ignored by stdin-based
	// adapters.
	FlagName string

	// StdinDelay overrides the delay before injecting stdin. Zero means
	// "use the adapter default". Ignored by flag-based adapters.
	StdinDelay time.Duration
}

// Registry holds a set of [Adapter] instances and resolves a command
// string to the first matching adapter. It also applies per-adapter
// overrides supplied via configuration.
//
// The zero value is not usable — construct one via [NewRegistry] or
// [DefaultRegistry].
type Registry struct {
	adapters  []Adapter
	overrides map[string]Override
}

// NewRegistry creates an empty registry with no adapters registered.
// Prefer [DefaultRegistry] for normal usage.
func NewRegistry() *Registry {
	return &Registry{overrides: make(map[string]Override)}
}

// DefaultRegistry returns a registry pre-populated with all agents the
// legacy run.go / run_windows.go code supported. Order matters: Claude
// is checked first because its name is a proper substring of no other
// agent, but placing it first keeps lookup cheap for the common case.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(newClaudeAdapter())
	r.Register(newStdinAdapter("gemini", []string{"gemini"}))
	r.Register(newStdinAdapter("copilot", []string{"copilot"}))
	r.Register(newStdinAdapter("aider", []string{"aider"}))
	// cursor-agent must be registered before cursor so its base name
	// matches before cursor's prefix test would accept it. Matches() is
	// exact on the base name so ordering is actually defensive only,
	// but keeping the more-specific entry first documents intent.
	r.Register(newStdinAdapter("cursor-agent", []string{"cursor-agent"}))
	r.Register(newStdinAdapter("cursor", []string{"cursor"}))
	r.Register(newStdinAdapter("opencode", []string{"opencode"}))
	r.Register(newStdinAdapter("kimi-cli", []string{"kimi-cli"}))
	r.Register(newStdinAdapter("kimi", []string{"kimi"}))
	r.Register(newStdinAdapter("auggie", []string{"auggie"}))
	return r
}

// Register adds an adapter to the registry. Adapters are consulted in
// registration order during [Registry.Lookup].
func (r *Registry) Register(a Adapter) {
	r.adapters = append(r.adapters, a)
}

// SetOverrides replaces the per-adapter override map. Keys are adapter
// names (matched against [Adapter.Name]); unknown keys are ignored.
// Passing nil clears all overrides.
func (r *Registry) SetOverrides(overrides map[string]Override) {
	if overrides == nil {
		r.overrides = make(map[string]Override)
		return
	}
	// Copy so callers can't mutate after the fact.
	cp := make(map[string]Override, len(overrides))
	for k, v := range overrides {
		cp[strings.ToLower(k)] = v
	}
	r.overrides = cp
}

// Lookup finds the first adapter whose Matches returns true for the
// given command. Returns nil when no adapter matches. The returned
// adapter reflects any configured overrides for that agent.
func (r *Registry) Lookup(command string) Adapter {
	for _, a := range r.adapters {
		if a.Matches(command) {
			if ov, ok := r.overrides[a.Name()]; ok {
				return withOverride(a, ov)
			}
			return a
		}
	}
	return nil
}

// baseNameOf strips directory prefixes and, on Windows, the .exe suffix
// from a command string. Returned name is lowercased.
func baseNameOf(command string) string {
	// Strip directory components.
	if idx := strings.LastIndexAny(command, `/\`); idx != -1 {
		command = command[idx+1:]
	}
	command = strings.TrimSuffix(strings.ToLower(command), ".exe")
	return command
}

// resolveBaseName returns the base name of command, first trying the
// command as given, then — if that yields no match for `aliases` — via
// PATH resolution so shell aliases and wrappers route to the right
// adapter.
func resolveBaseName(command string, aliases []string) (string, bool) {
	base := baseNameOf(command)
	for _, alias := range aliases {
		if base == alias {
			return base, true
		}
	}
	// Try PATH lookup for the case where `command` is a wrapper or alias
	// that resolves to one of our aliases.
	if resolved, err := exec.LookPath(command); err == nil {
		rbase := baseNameOf(resolved)
		for _, alias := range aliases {
			if rbase == alias {
				return rbase, true
			}
		}
	}
	return base, false
}
