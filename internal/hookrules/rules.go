// Package hookrules implements regex-based rules for the `agnt hook`
// subcommand family that redirects raw Bash usage and user prompts toward
// agnt MCP tools.
//
// The CLI hot path is:
//
//	check-bash: read PreToolUse JSON → find Bash tool → match command against
//	  BashRules → emit decision (allow/warn/block) within a 1s budget.
//	check-prompt: read UserPromptSubmit JSON → match prompt against
//	  PromptRules → emit a <system-reminder> on stdout if matched.
//
// Rules are compiled once (via sync.OnceValue) and re-used for the life of
// the process. A rule set is the union of built-in defaults and any overrides
// loaded from .agnt.kdl's `hook-rules` block.
//
// Design rule: on any error (regex compile failure, config parse failure,
// daemon down for scope guard) the package returns a "no-op allow" decision.
// Breaking the agent's tool call is worse than failing to intercept.
package hookrules

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Action is the decision emitted by matching a Bash rule.
type Action string

const (
	// ActionAllow means the command was not matched or is explicitly allowed.
	ActionAllow Action = "allow"
	// ActionSoftWarn emits a stderr nudge but still exits 0, so the tool call
	// proceeds. Used for patterns that are often fine but sometimes better
	// served by an MCP tool.
	ActionSoftWarn Action = "soft-warn"
	// ActionBlock emits exit code 2 with a stderr redirect, preventing the
	// tool call from executing. Used for patterns that have a clean MCP
	// equivalent (dev-server start, kill, proxy log tail, etc.).
	ActionBlock Action = "block"
)

// BashRule is a compiled pattern against the Bash command string with an
// associated action and optional replacement text cited in the stderr
// message. Replacement is free-form — typically an agnt MCP tool name with
// a hint of parameters — so operators can see exactly what to call instead.
type BashRule struct {
	// Pattern is the compiled regex matched against the Bash command string.
	Pattern *regexp.Regexp
	// Raw is the source pattern string — preserved so `rules list` and
	// `rules test` output cite the human-readable form, not the compiled
	// regex syntax.
	Raw string
	// Action is the decision emitted on a match.
	Action Action
	// Replacement is a short citation of the recommended MCP invocation.
	// Empty means "no replacement; see docs."
	Replacement string
	// Reason is optional prose appended to the stderr message. Use to
	// explain *why* the command was redirected when the replacement alone
	// is not self-explanatory.
	Reason string
}

// PromptRule is a compiled pattern against the user's prompt text with an
// associated system-reminder body injected when it matches. Prompt rules
// never block — they only nudge.
type PromptRule struct {
	// Pattern is the compiled regex matched against the prompt text. All
	// patterns are case-insensitive by default because prompt wording is
	// colloquial, not a command line.
	Pattern *regexp.Regexp
	// Raw is the source pattern string for `rules list`.
	Raw string
	// Reminder is the body of the <system-reminder> emitted on match.
	Reminder string
}

// RuleSet is the merged collection of Bash and prompt rules used by the
// check-bash / check-prompt subcommands. Rules are evaluated in order;
// first match wins for Bash (so higher-priority patterns should be listed
// first in both builtin defaults and the KDL override).
type RuleSet struct {
	BashRules   []BashRule
	PromptRules []PromptRule
	// BypassEnv is the env var whose presence (set to any non-empty value)
	// causes check-bash / check-prompt to fail-open. Defaults to
	// "AGNT_HOOK_BYPASS"; configurable via .agnt.kdl.
	BypassEnv string
}

// Decision is the output of MatchBash. The zero value is a valid "allow"
// decision so callers can treat a nil-error, empty-pattern input as a
// no-op.
type Decision struct {
	Action      Action
	Rule        *BashRule // nil when Action == ActionAllow
	Replacement string
	Reason      string
}

// MatchBash runs the command string against each rule in order and returns
// the first match's decision. An empty command is always Allow (no rule
// fires) — callers typically get here from a malformed PreToolUse payload
// and the fail-open contract takes over.
func (rs *RuleSet) MatchBash(command string) Decision {
	if rs == nil || command == "" {
		return Decision{Action: ActionAllow}
	}
	for i := range rs.BashRules {
		r := &rs.BashRules[i]
		if r.Pattern == nil {
			continue
		}
		if r.Pattern.MatchString(command) {
			return Decision{
				Action:      r.Action,
				Rule:        r,
				Replacement: r.Replacement,
				Reason:      r.Reason,
			}
		}
	}
	return Decision{Action: ActionAllow}
}

// MatchPrompt returns the list of prompt rule reminders that match the
// input text. Unlike MatchBash, prompt matching is additive — multiple
// reminders can fire on a single prompt (e.g., a prompt mentioning both
// "start the server" and "check errors" should pick up both nudges).
func (rs *RuleSet) MatchPrompt(prompt string) []PromptRule {
	if rs == nil || prompt == "" {
		return nil
	}
	var hits []PromptRule
	for _, r := range rs.PromptRules {
		if r.Pattern == nil {
			continue
		}
		if r.Pattern.MatchString(prompt) {
			hits = append(hits, r)
		}
	}
	return hits
}

// BuiltinRuleSet returns the default rule set compiled from the hardcoded
// patterns below. Compilation happens once on first call; panics in compile
// are caught as static tests (see rules_test.go TestBuiltinRuleSet).
//
// The current catalog is intentionally small (nine Bash patterns, two
// prompt patterns) so the regression corpus stays manageable and the
// rules remain easy for a human to scan. New patterns should land as
// KDL-side overrides first and only get promoted to builtins after they
// have enough production signal.
func BuiltinRuleSet() *RuleSet {
	return builtinRuleSetOnce()
}

var builtinRuleSetOnce = sync.OnceValue(func() *RuleSet {
	rs := &RuleSet{BypassEnv: DefaultBypassEnv}
	for _, spec := range builtinBashSpecs {
		rule, err := compileBashSpec(spec)
		if err != nil {
			// Builtin patterns are hand-authored literals; a compile
			// failure here is a programming error caught by
			// TestBuiltinRuleSet. Panic so we never ship a broken
			// catalog.
			panic(fmt.Sprintf("hookrules: builtin bash spec %q: %v", spec.Raw, err))
		}
		rs.BashRules = append(rs.BashRules, rule)
	}
	for _, spec := range builtinPromptSpecs {
		rule, err := compilePromptSpec(spec)
		if err != nil {
			panic(fmt.Sprintf("hookrules: builtin prompt spec %q: %v", spec.Raw, err))
		}
		rs.PromptRules = append(rs.PromptRules, rule)
	}
	return rs
})

// DefaultBypassEnv is the env var whose presence short-circuits the hook
// interceptor. Exported so the CLI layer can reference it in help text.
const DefaultBypassEnv = "AGNT_HOOK_BYPASS"

// BypassMarker is the inline comment marker that, when present anywhere in
// a Bash command, causes the interceptor to fail-open for that single
// invocation. "# agnt-allow" is unambiguous (unlikely to collide with real
// shell usage) and visible in the command string the daemon logs.
const BypassMarker = "# agnt-allow"

// CommandHasBypassMarker returns true if the command string contains the
// inline bypass marker. The check is a plain Contains — we do NOT try to
// parse shell comments because the marker is opt-in and the exit contract
// must be fast.
func CommandHasBypassMarker(cmd string) bool {
	return strings.Contains(cmd, BypassMarker)
}

// bashSpec is the uncompiled form of a Bash rule. Split out so both the
// builtin catalog and the KDL loader share one construction path.
type bashSpec struct {
	Raw         string
	Action      Action
	Replacement string
	Reason      string
}

// promptSpec mirrors bashSpec for prompt rules.
type promptSpec struct {
	Raw      string
	Reminder string
}

func compileBashSpec(s bashSpec) (BashRule, error) {
	if s.Raw == "" {
		return BashRule{}, fmt.Errorf("empty bash pattern")
	}
	re, err := regexp.Compile(s.Raw)
	if err != nil {
		return BashRule{}, fmt.Errorf("compile %q: %w", s.Raw, err)
	}
	action := s.Action
	if action == "" {
		action = ActionBlock
	}
	return BashRule{
		Pattern:     re,
		Raw:         s.Raw,
		Action:      action,
		Replacement: s.Replacement,
		Reason:      s.Reason,
	}, nil
}

func compilePromptSpec(s promptSpec) (PromptRule, error) {
	if s.Raw == "" {
		return PromptRule{}, fmt.Errorf("empty prompt pattern")
	}
	// Prompts are natural language; case-insensitive match is almost
	// always what the operator wants. Users who need case sensitivity
	// can prefix with (?-i).
	re, err := regexp.Compile("(?i)" + s.Raw)
	if err != nil {
		return PromptRule{}, fmt.Errorf("compile %q: %w", s.Raw, err)
	}
	return PromptRule{
		Pattern:  re,
		Raw:      s.Raw,
		Reminder: s.Reminder,
	}, nil
}

// builtinBashSpecs is the core catalog. Ordering matters: first match wins,
// so more specific patterns come before generic ones.
//
// The nine patterns cover the high-frequency raw-Bash moves that have a
// clean agnt MCP replacement. Expansion beyond nine should go via
// .agnt.kdl override first.
var builtinBashSpecs = []bashSpec{
	{
		// `npm run dev`, `pnpm run dev`, `yarn dev`, `bun run dev` — by far
		// the most common pattern where the agent should be using
		// `agnt.run {script_name: "dev"}` so the daemon can track the
		// process, attach a proxy, and surface errors.
		Raw:         `(?m)(^|[;&|]\s*)(npm|pnpm|yarn|bun)\s+(run\s+)?(dev|start|serve)\b`,
		Action:      ActionBlock,
		Replacement: `agnt.run {script_name: "dev"}`,
		Reason:      "agnt manages dev-server lifecycle, attaches a proxy, and captures errors the plain shell invocation hides.",
	},
	{
		// Go dev server: `go run ./cmd/server` and friends. Block so the
		// agent goes through `proc run` where state is tracked.
		Raw:         `(?m)(^|[;&|]\s*)go\s+run\b`,
		Action:      ActionBlock,
		Replacement: `agnt.proc {action: "run", name: "<script-name>"}`,
		Reason:      "Unmanaged go run cannot be restarted or error-tracked by agnt.",
	},
	{
		// `kill <pid>` / `killall <name>` — the agent should use
		// `agnt.proc {action: "stop", ...}` so the daemon can clean up
		// proxies and session state.
		Raw:         `(?m)(^|[;&|]\s*)(kill|killall|pkill)\b`,
		Action:      ActionBlock,
		Replacement: `agnt.proc {action: "stop", name: "<script-name>"}`,
		Reason:      "Raw kill bypasses agnt state; pgid-linked children and proxies will leak.",
	},
	{
		// `lsof -i :<port>` / `lsof -iTCP` — port introspection that
		// should go through `proc cleanup_port` or `proc status`.
		Raw:         `(?m)(^|[;&|]\s*)lsof\s+-i\b`,
		Action:      ActionSoftWarn,
		Replacement: `agnt.proc {action: "cleanup_port", port: <port>}`,
		Reason:      "agnt already tracks port ownership for managed processes.",
	},
	{
		// `ss -tlnp` / `netstat -lnp` — similar to lsof, surface agnt's
		// own view instead.
		Raw:         `(?m)(^|[;&|]\s*)(ss|netstat)\s+.*-.*l`,
		Action:      ActionSoftWarn,
		Replacement: `agnt.proc {action: "list"}`,
		Reason:      "agnt.proc list shows managed listeners with their daemon-side state.",
	},
	{
		// `tail -f <logfile>` — proxy logs already flow through the
		// daemon ring buffer, and log-file tail misses browser errors.
		Raw:         `(?m)(^|[;&|]\s*)tail\s+-f\b`,
		Action:      ActionBlock,
		Replacement: `agnt.proxylog {action: "query"} or agnt.watch {target: "all"}`,
		Reason:      "agnt streams structured events (errors, interactions, HTTP) that tail -f cannot see.",
	},
	{
		// `curl http://localhost:<port>/...` — this is almost always the
		// agent poking at the dev server it just started. Soft-warn so
		// health checks and deliberate API pokes aren't blocked.
		Raw:         `(?m)(^|[;&|]\s*)curl\s+.*\blocalhost\b`,
		Action:      ActionSoftWarn,
		Replacement: `agnt.proxy {action: "exec", code: "..."}`,
		Reason:      "agnt.proxy exec runs in a real browser context and captures JS errors.",
	},
	{
		// `grep -i error ...` across log files — redirect to get_errors
		// which already aggregates and deduplicates.
		Raw:         `(?m)(^|[;&|]\s*)grep\s+.*\b[Ee]rror\b`,
		Action:      ActionSoftWarn,
		Replacement: `agnt.get_errors {}`,
		Reason:      "agnt.get_errors aggregates process + proxy errors with dedup.",
	},
	{
		// `ps aux | grep` — looking for our own managed process. Point
		// at `proc status` which knows the PID and lifecycle state.
		Raw:         `(?m)(^|[;&|]\s*)ps\s+.*\|\s*grep\b`,
		Action:      ActionSoftWarn,
		Replacement: `agnt.proc {action: "status", name: "<script-name>"}`,
		Reason:      "agnt.proc status is authoritative for managed processes.",
	},
}

// builtinPromptSpecs are the two baseline intent-based reminders.
// Additional prompt rules should come from the KDL override block.
var builtinPromptSpecs = []promptSpec{
	{
		Raw:      `start.*(server|dev|app)|launch.*(server|dev|app)|run.*(dev|server)`,
		Reminder: "Use the agnt.run or agnt.proc MCP tool to start dev servers so the daemon attaches a proxy and tracks state. Avoid `npm run dev` / `go run` in plain Bash.",
	},
	{
		Raw:      `(check|look at|view|tail).*(logs?|errors?)`,
		Reminder: "Use agnt.get_errors for aggregated error views or agnt.watch for live streaming, instead of tail -f / grep on log files.",
	},
}
