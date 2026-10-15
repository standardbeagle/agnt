package hookrules

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// LoadForProject returns the merged rule set for the given project path.
// Built-in rules are always present; KDL overrides (from `.agnt.kdl`
// `hook-rules` block) are appended so a user-provided rule evaluates AFTER
// builtins, and among themselves in rule-name order (see byName). A user who needs to override a builtin pattern should do it by
// negating via a more specific pattern earlier in evaluation order — which
// today means editing the builtin catalog, since override-before-builtin
// semantics would require per-project reordering we have deliberately not
// built.
//
// Errors loading config fall through silently to builtin-only. The callers
// (check-bash / check-prompt) have a 1s fail-open budget and must not
// surface config errors as hook failures.
func LoadForProject(projectDir string) *RuleSet {
	base := BuiltinRuleSet()
	if projectDir == "" {
		return base
	}
	cfg, err := config.LoadAgntConfig(projectDir)
	if err != nil || cfg == nil || cfg.HookRules == nil {
		return base
	}
	return MergeOverrides(base, cfg.HookRules)
}

// byName returns the rule names of a .agnt.kdl pattern block in byte order.
// The block is parsed into a map, which keeps no document order, so the
// order user rules are appended (and so which of two overlapping bash rules
// wins, first match winning) is fixed by name rather than left to map
// iteration, which differs from run to run.
func byName[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// MergeOverrides returns a new RuleSet that combines the builtin base with
// the parsed KDL override block. It does not mutate base. Invalid patterns
// in the override are silently skipped — consistent with the fail-open
// contract — but LoadAndValidate (below) surfaces them for `rules` dry-run.
func MergeOverrides(base *RuleSet, override *config.HookRulesConfig) *RuleSet {
	if override == nil {
		return base
	}
	rs := &RuleSet{
		BashRules:   append([]BashRule(nil), base.BashRules...),
		PromptRules: append([]PromptRule(nil), base.PromptRules...),
		BypassEnv:   base.BypassEnv,
	}
	if override.BypassEnv != "" {
		rs.BypassEnv = override.BypassEnv
	}
	for _, name := range byName(override.BashPatterns) {
		r := override.BashPatterns[name]
		if r == nil || r.Pattern == "" {
			continue
		}
		spec := bashSpec{
			Raw:         r.Pattern,
			Action:      Action(r.Action),
			Replacement: r.Replacement,
			Reason:      r.Reason,
		}
		rule, err := compileBashSpec(spec)
		if err != nil {
			// Best-effort skip — invalid regex in user config must not
			// poison the fail-open hot path. `rules` subcommand surfaces
			// these to the operator; debug.Log leaves a trace meanwhile.
			debug.Log("hookrules", "skipping invalid bash-pattern %q: %v", name, err)
			continue
		}
		rs.BashRules = append(rs.BashRules, rule)
	}
	for _, name := range byName(override.PromptPatterns) {
		r := override.PromptPatterns[name]
		if r == nil || r.Pattern == "" {
			continue
		}
		spec := promptSpec{
			Raw:      r.Pattern,
			Reminder: r.Reminder,
		}
		rule, err := compilePromptSpec(spec)
		if err != nil {
			// Best-effort skip — see bash-pattern note above.
			debug.Log("hookrules", "skipping invalid prompt-pattern %q: %v", name, err)
			continue
		}
		rs.PromptRules = append(rs.PromptRules, rule)
	}
	return rs
}

// LoadAndValidate is the non-silent loader for the `rules` subcommand. It
// returns the merged rule set plus any per-pattern errors so the CLI can
// surface them to the operator. Unlike LoadForProject, it does not swallow
// regex compile failures — they come back in the errors slice.
func LoadAndValidate(projectDir string) (*RuleSet, []error) {
	base := BuiltinRuleSet()
	if projectDir == "" {
		return base, nil
	}
	cfg, err := config.LoadAgntConfig(projectDir)
	if err != nil {
		return base, []error{fmt.Errorf("load .agnt.kdl: %w", err)}
	}
	if cfg == nil || cfg.HookRules == nil {
		return base, nil
	}
	var errs []error
	rs := &RuleSet{
		BashRules:   append([]BashRule(nil), base.BashRules...),
		PromptRules: append([]PromptRule(nil), base.PromptRules...),
		BypassEnv:   base.BypassEnv,
	}
	if cfg.HookRules.BypassEnv != "" {
		rs.BypassEnv = cfg.HookRules.BypassEnv
	}
	for _, name := range byName(cfg.HookRules.BashPatterns) {
		r := cfg.HookRules.BashPatterns[name]
		if r == nil || r.Pattern == "" {
			errs = append(errs, fmt.Errorf("bash-pattern %q: empty pattern", name))
			continue
		}
		spec := bashSpec{
			Raw:         r.Pattern,
			Action:      Action(r.Action),
			Replacement: r.Replacement,
			Reason:      r.Reason,
		}
		rule, err := compileBashSpec(spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("bash-pattern %q: %w", name, err))
			continue
		}
		rs.BashRules = append(rs.BashRules, rule)
	}
	for _, name := range byName(cfg.HookRules.PromptPatterns) {
		r := cfg.HookRules.PromptPatterns[name]
		if r == nil || r.Pattern == "" {
			errs = append(errs, fmt.Errorf("prompt-pattern %q: empty pattern", name))
			continue
		}
		spec := promptSpec{
			Raw:      r.Pattern,
			Reminder: r.Reminder,
		}
		rule, err := compilePromptSpec(spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("prompt-pattern %q: %w", name, err))
			continue
		}
		rs.PromptRules = append(rs.PromptRules, rule)
	}
	return rs, errs
}

// ScopeGuardActive reports whether hook interception should apply for the
// given project directory. The intent is: avoid false positives in random
// unrelated directories. Interception applies when ANY of these is true:
//
//   - `.agnt.kdl` exists at or above projectDir
//   - projectDir is non-empty (weaker fallback — the caller passed a real
//     project path, so we trust it)
//
// The "daemon has a process/proxy for this path" check described in the
// task is intentionally omitted here: the scope-guard runs on the hot path
// and must not block on an IPC round-trip to a potentially wedged daemon.
// The 50ms daemon-connect deadline called out in the task is a stricter
// requirement than what we'd get from a best-effort guard, so we punt on
// it entirely. The .agnt.kdl presence check is cheap and catches the
// common case (user opened a non-agnt repo in Claude Code).
//
// When projectDir is empty (Claude Code did not pass --project-path), we
// return true — fail-closed on scope would silently disable interception
// for the majority of real invocations where the hook wiring simply
// forgot the flag.
func ScopeGuardActive(projectDir string) bool {
	if projectDir == "" {
		return true
	}
	if path := config.FindAgntConfigFile(projectDir); path != "" {
		return true
	}
	// No .agnt.kdl anywhere in the parent chain — this is probably not an
	// agnt-managed project, so stay out of the way.
	return false
}

// EnsureRulesDocPath returns the absolute path to the project-local
// docs/hook-rules.md file if present, empty string otherwise. Used by the
// `rules list` subcommand to include a doc pointer in its output.
func EnsureRulesDocPath(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	p := filepath.Join(projectDir, "docs", "hook-rules.md")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
