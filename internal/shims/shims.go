// Package shims implements per-project shell command wrappers: tiny scripts
// in <project>/.agnt/bin that shadow commands like npm/kill/lsof on PATH for
// agnt-managed shells and route invocations through the daemon, so agents
// get managed processes plus feedback pointing at the MCP tools.
//
// The package is fail-open by contract: when the daemon is unreachable or
// the shell is not agnt-managed, the shim execs the real binary and the user
// never notices the wrapper. Cleanup runs on graceful daemon shutdown, on
// last-session teardown, via a startup sweep, and via an external watcher
// process that removes stale bin dirs if agnt is killed.
package shims

import (
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
)

// Action is the routing decision for a shimmed invocation.
type Action string

const (
	// ActionRoute runs the command through the managed-process pipeline.
	ActionRoute Action = "route"
	// ActionRestartWatch runs the command, then restarts the watch script.
	ActionRestartWatch Action = "restart-watch"
	// ActionReroute runs the command in Dir instead of the project root so
	// a running watch is not disturbed (e.g. out-of-tree build).
	ActionReroute Action = "reroute"
	// ActionQuiesce stops the watch script, runs the command, restarts it.
	ActionQuiesce Action = "quiesce"
	// ActionIgnore acknowledges the command without running anything.
	ActionIgnore Action = "ignore"
	// ActionBlock refuses the command and instructs the agent.
	ActionBlock Action = "block"
	// ActionPass execs the real binary as if no shim existed.
	ActionPass Action = "pass"
)

// CommandClass groups shimmed commands by how ActionRoute executes them.
type CommandClass int

const (
	// ClassDevServer is a long-lived dev server / watch (npm run dev, vite,
	// go run, cargo run). Routed commands start-or-reuse a managed process.
	ClassDevServer CommandClass = iota
	// ClassOneShot is a bounded build/test command (npm run build, go test,
	// make). Routed commands run managed and wait for exit.
	ClassOneShot
	// ClassKill is the kill/killall/pkill family. Routed only when every
	// target resolves to a daemon-managed process.
	ClassKill
	// ClassPort is the lsof/fuser family. Routed to a port/managed-process
	// report pointing at proc cleanup_port.
	ClassPort
	// ClassGeneric is anything else installed as a shim. Defaults to pass.
	ClassGeneric
)

// Command names that get wrapper scripts in .agnt/bin. Keep this list tight:
// every entry shadows a real binary in managed shells, so a mistake here
// breaks basic tooling. Fail-open keeps the blast radius at "wrapper is a
// no-op", but the list should still only cover commands with a clear agnt
// routing story.
func CommandNames() []string {
	return []string{
		"npm", "pnpm", "yarn", "bun",
		"vite", "next",
		"go", "cargo", "make",
		"kill", "killall", "pkill",
		"lsof", "fuser",
	}
}

// DevScriptNames is the canonical list of script names treated as
// long-lived dev servers / watches. classifyPkgRunner uses it to class
// `npm run dev`-style invocations, and WatchScriptName uses it (in this
// order) to pick the default watch script — one list so the two cannot
// drift.
var DevScriptNames = []string{"dev", "watch", "start", "serve"}

// shimMarker is the first-line fingerprint of an agnt-managed shim script.
// Ensure uses it to decide whether an existing file is ours (rewrite) or
// user content (leave alone).
const shimMarker = "agnt-shim v1"

// Classify determines the command class from the invoked name and args.
// argv excludes argv[0]; cmd is the shimmed name ("npm", "kill", ...).
func Classify(cmd string, args []string) CommandClass {
	joined := strings.Join(args, " ")
	switch cmd {
	case "npm", "pnpm", "yarn", "bun":
		return classifyPkgRunner(joined)
	case "vite":
		return ClassDevServer
	case "next":
		if hasWord(args, "dev") || hasWord(args, "start") {
			return ClassDevServer
		}
		return ClassOneShot
	case "go":
		if len(args) > 0 && args[0] == "run" {
			return ClassDevServer
		}
		if len(args) > 0 && (args[0] == "build" || args[0] == "test" || args[0] == "vet") {
			return ClassOneShot
		}
		return ClassGeneric
	case "cargo":
		if len(args) > 0 && args[0] == "run" {
			return ClassDevServer
		}
		if len(args) > 0 && (args[0] == "build" || args[0] == "test" || args[0] == "check") {
			return ClassOneShot
		}
		return ClassGeneric
	case "make":
		return ClassOneShot
	case "kill", "killall", "pkill":
		return ClassKill
	case "lsof", "fuser":
		return ClassPort
	}
	return ClassGeneric
}

// classifyPkgRunner handles npm/pnpm/yarn/bun. Dev scripts are long-lived;
// build/test/lint are one-shot; everything else (install, config, ...)
// passes through.
func classifyPkgRunner(joined string) CommandClass {
	fields := strings.Fields(joined)
	// Strip a leading "run" so `npm run dev` and `yarn dev` classify alike.
	if len(fields) > 0 && fields[0] == "run" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ClassGeneric
	}
	for _, name := range DevScriptNames {
		if fields[0] == name {
			return ClassDevServer
		}
	}
	switch fields[0] {
	case "build", "test", "lint", "typecheck", "type-check", "check":
		return ClassOneShot
	}
	return ClassGeneric
}

func hasWord(args []string, word string) bool {
	for _, a := range args {
		if a == word {
			return true
		}
	}
	return false
}

// Decision is the resolved routing for one invocation.
type Decision struct {
	Action Action
	// Dir is the reroute working directory (project-relative), only set
	// for ActionReroute.
	Dir string
	// RuleName is the matched config rule (empty for class defaults).
	RuleName string
}

// Resolve picks the action for one invocation. User rules are evaluated
// first; the most specific matching glob wins (longest match string, then
// fewest wildcards, then name for determinism). With no rule match the
// class default applies: route for dev-server/one-shot/kill/port, pass for
// generic. A nil or empty rules map is the common path and must stay cheap.
func Resolve(cmd string, args []string, cfg *config.ShimsConfig) Decision {
	cmdline := cmd
	if len(args) > 0 {
		cmdline = cmd + " " + strings.Join(args, " ")
	}
	if cfg != nil && len(cfg.Rules) > 0 {
		if name, rule, ok := matchRule(cmdline, cfg.Rules); ok {
			action := Action(rule.Action)
			if action == "" {
				action = ActionRoute
			}
			return Decision{Action: action, Dir: rule.Dir, RuleName: name}
		}
	}
	if Classify(cmd, args) == ClassGeneric {
		return Decision{Action: ActionPass}
	}
	return Decision{Action: ActionRoute}
}

// matchRule finds the most specific matching rule. Glob `*` matches any
// substring (including empty); all other bytes are literal. Specificity is
// the count of NON-wildcard characters (a longer literal anchor beats a
// pattern padded with `*`); ties break on fewer wildcards, then rule name —
// fully deterministic despite map iteration order.
func matchRule(cmdline string, rules map[string]*config.ShimRule) (string, *config.ShimRule, bool) {
	bestScore := -1
	bestWildcards := 0
	var bestName string
	var best *config.ShimRule
	for name, rule := range rules {
		if rule == nil || rule.Match == "" {
			continue
		}
		if !globMatch(rule.Match, cmdline) {
			continue
		}
		wild := strings.Count(rule.Match, "*")
		score := len(rule.Match) - wild
		if score > bestScore ||
			(score == bestScore && wild < bestWildcards) ||
			(score == bestScore && wild == bestWildcards && name < bestName) {
			bestScore, bestWildcards, bestName, best = score, wild, name, rule
		}
	}
	return bestName, best, best != nil
}

// globMatch implements single-char-class globbing: `*` matches any run of
// characters, everything else is literal. Deliberately NOT filepath.Match —
// command lines contain spaces and filepath semantics (separator rules)
// would surprise.
func globMatch(pattern, s string) bool {
	// Iterative two-pointer with backtracking on `*`.
	px, sx := 0, 0
	starPx, starSx := -1, 0
	for sx < len(s) {
		if px < len(pattern) && pattern[px] == s[sx] {
			px++
			sx++
			continue
		}
		if px < len(pattern) && pattern[px] == '*' {
			starPx = px
			starSx = sx
			px++
			continue
		}
		if starPx != -1 {
			px = starPx + 1
			starSx++
			sx = starSx
			continue
		}
		return false
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// WatchScriptName resolves the watch script for restart-watch/quiesce:
// explicit config first, else the first configured script from
// DevScriptNames.
func WatchScriptName(cfg *config.AgntConfig) string {
	if cfg == nil {
		return ""
	}
	if cfg.Shims != nil && cfg.Shims.WatchScript != "" {
		return cfg.Shims.WatchScript
	}
	for _, name := range DevScriptNames {
		if _, ok := cfg.Scripts[name]; ok {
			return name
		}
	}
	return ""
}

// ValidateAction reports whether s is a known action name.
func ValidateAction(s string) error {
	switch Action(s) {
	case ActionRoute, ActionRestartWatch, ActionReroute, ActionQuiesce,
		ActionIgnore, ActionBlock, ActionPass:
		return nil
	}
	return fmt.Errorf("unknown shim action %q (want route, restart-watch, reroute, quiesce, ignore, block, pass)", s)
}
