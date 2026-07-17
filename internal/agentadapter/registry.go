package agentadapter

import (
	"os/exec"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
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

	// Aliases are extra command base names that resolve to this adapter.
	// They let a project map an opaque wrapper / shell-alias target (e.g.
	// "cdsp") onto a known agent ("claude") so it gets the right injection
	// mechanism instead of falling through to the universal stdin adapter.
	Aliases []string
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
	// aliases maps a command base name to the canonical adapter name it
	// should resolve to (from Override.Aliases). Consulted first in Lookup so
	// an explicit project mapping wins over the built-in Matches logic.
	aliases map[string]string
}

// NewRegistry creates an empty registry with no adapters registered.
// Prefer [DefaultRegistry] for normal usage.
func NewRegistry() *Registry {
	return &Registry{overrides: make(map[string]Override), aliases: make(map[string]string)}
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
		r.aliases = make(map[string]string)
		return
	}
	// Copy so callers can't mutate after the fact, and rebuild the alias index
	// (command base name → adapter name) from each override's Aliases.
	cp := make(map[string]Override, len(overrides))
	al := make(map[string]string)
	for k, v := range overrides {
		name := strings.ToLower(k)
		cp[name] = v
		for _, alias := range v.Aliases {
			if base := baseNameOf(alias); base != "" {
				if prev, dup := al[base]; dup {
					debug.Log("agentadapter", "alias %q redeclared by %q: was %q, last wins", base, name, prev)
				}
				al[base] = name
			}
		}
	}
	r.overrides = cp
	r.aliases = al
}

// Lookup finds the first adapter whose Matches returns true for the
// given command. Returns nil when no adapter matches. The returned
// adapter reflects any configured overrides for that agent.
func (r *Registry) Lookup(command string) Adapter {
	// An explicit project alias (config `ai.adapters.<name>.aliases`) wins over
	// the built-in name/PATH matching: it maps an opaque wrapper command onto a
	// known adapter so it gets the right injection mechanism.
	if name, ok := r.aliases[baseNameOf(command)]; ok {
		for _, a := range r.adapters {
			if a.Name() == name {
				if ov, ok := r.overrides[name]; ok {
					return withOverride(a, ov)
				}
				return a
			}
		}
		// Alias points at an unregistered adapter name (config typo). Fall
		// through to built-in matching, but surface the dead mapping.
		debug.Log("agentadapter", "alias %q resolves to unknown adapter %q; falling through", baseNameOf(command), name)
	}
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

// Universal returns a stdin-based adapter for an unrecognized command, so any
// tool launched under `agnt run` still receives the agnt prompt (via stdin
// after the default delay) rather than silently getting nothing. The verb, not
// the agent identity, decides that injection happens; this is the fallback when
// [Registry.Lookup] finds no registered match. The adapter's name is derived
// from the command's base name for logging.
func Universal(command string) Adapter {
	base := baseNameOf(command)
	if base == "" {
		base = "agent"
	}
	return newStdinAdapter(base, []string{base})
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
