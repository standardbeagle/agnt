package config

import (
	"fmt"
	"math"
	"sort"
	"strings"

	kdl "github.com/sblinch/kdl-go"
	"github.com/sblinch/kdl-go/document"
)

// Parse-time validation for .agnt.kdl, beyond what struct unmarshalling gives.
//
// Two things the unmarshaller cannot do on its own:
//
//   - Closed sets. A string field accepts any string, and the consumers of
//     `project.port-conflict`, `alerts.preset` and `alerts.auto-forward`'s
//     `sources` / `severity` treat a value they do not
//     know as "none of my cases", so a typo changed behaviour without a word.
//   - Number shape. kdl-go truncates a float into an int field with no error
//     (`ports 3000.7` became 3000), and an int field has no range. Those are
//     checked on the parsed document, where the literal is still visible.
//
// The same document walk reports keys that parse but that nothing reads, so
// a config relying on one says so in the session log instead of doing nothing.

// validPortConflictPolicies is the closed set for `project.port-conflict`.
// Kept in step with the switch in internal/daemon/daemon_autostart.go.
var validPortConflictPolicies = []string{"prompt", "auto-kill", "skip", "fail"}

// validAlertPresets is the closed set for `alerts.preset` (PresetPushConfig).
var validAlertPresets = []string{"claude-code", "universal"}

// validAutoForwardSources is the closed set for `alerts.auto-forward.sources`
// (AutoForwardConfig.ShouldForwardSource; autoForwardSource in cmd/agnt
// classifies every event as one of these).
var validAutoForwardSources = []string{"browser", "http"}

// validAutoForwardSeverities is the closed set for
// `alerts.auto-forward.severity` (autoForwardPolicy in cmd/agnt). "warn" is
// accepted as an alias: it always behaved as "warning" (only "error" is
// special-cased), so rejecting it would fail configs that load today.
var validAutoForwardSeverities = []string{"error", "warning", "warn"}

// validAlertPatternSeverities is the closed set for
// `alerts.patterns.<id>.severity` (empty means error). Kept in step with
// compileCustomAlertPatterns in cmd/agnt, which maps anything else to error.
var validAlertPatternSeverities = []string{"error", "warning", "info"}

// validShimActions is the closed set for `shims.rules.<n>.action` (empty means
// route). Kept in step with shims.ValidateAction; internal/shims cannot be
// imported here (it imports config), so TestShimActionsAcceptedByConfig in
// that package pins the two lists together.
var validShimActions = []string{"route", "restart-watch", "reroute", "quiesce", "ignore", "block", "pass"}

// validHookActions is the closed set for `hook-rules.bash-patterns.<n>.action`
// (empty means block), matched case-insensitively as docs/hook-rules.md
// promises. Kept in step with the hookrules Action constants.
var validHookActions = []string{"allow", "soft-warn", "block"}

// validateAgntEnums rejects values outside the closed sets above.
func validateAgntEnums(cfg *AgntConfig) error {
	if cfg.Alerts != nil && cfg.Alerts.AutoForward != nil {
		af := cfg.Alerts.AutoForward
		for _, s := range af.Sources {
			if !containsString(validAutoForwardSources, s) {
				return fmt.Errorf("alerts.auto-forward: unknown source %q (accepted: %s)",
					s, strings.Join(validAutoForwardSources, ", "))
			}
		}
		if af.Severity != "" && !containsString(validAutoForwardSeverities, af.Severity) {
			return fmt.Errorf("alerts.auto-forward: unknown severity %q (accepted: %s)",
				af.Severity, strings.Join(validAutoForwardSeverities, ", "))
		}
	}
	if cfg.Project != nil && cfg.Project.PortConflict != "" {
		if !containsString(validPortConflictPolicies, cfg.Project.PortConflict) {
			return fmt.Errorf("project: unknown port-conflict %q (accepted: %s)",
				cfg.Project.PortConflict, strings.Join(validPortConflictPolicies, ", "))
		}
	}
	if cfg.Alerts != nil && cfg.Alerts.Preset != "" {
		if !containsString(validAlertPresets, cfg.Alerts.Preset) {
			return fmt.Errorf("alerts: unknown preset %q (accepted: %s)",
				cfg.Alerts.Preset, strings.Join(validAlertPresets, ", "))
		}
	}
	if cfg.Alerts != nil {
		for _, id := range sortedKeys(cfg.Alerts.Patterns) {
			if p := cfg.Alerts.Patterns[id]; p != nil && p.Severity != "" && !containsString(validAlertPatternSeverities, p.Severity) {
				return fmt.Errorf("alerts.patterns.%s: unknown severity %q (accepted: %s)",
					id, p.Severity, strings.Join(validAlertPatternSeverities, ", "))
			}
		}
	}
	if cfg.Shims != nil {
		for _, name := range sortedKeys(cfg.Shims.Rules) {
			if r := cfg.Shims.Rules[name]; r != nil && r.Action != "" && !containsString(validShimActions, r.Action) {
				return fmt.Errorf("shims.rules.%s: unknown action %q (accepted: %s)",
					name, r.Action, strings.Join(validShimActions, ", "))
			}
		}
	}
	if cfg.HookRules != nil {
		for _, name := range sortedKeys(cfg.HookRules.BashPatterns) {
			r := cfg.HookRules.BashPatterns[name]
			if r == nil || r.Action == "" {
				continue
			}
			// The hook matches the action exactly; store the canonical form.
			r.Action = strings.ToLower(r.Action)
			if !containsString(validHookActions, r.Action) {
				return fmt.Errorf("hook-rules.bash-patterns.%s: unknown action %q (accepted: %s)",
					name, r.Action, strings.Join(validHookActions, ", "))
			}
		}
	}
	return nil
}

func containsString(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// intRule bounds one integer-valued node. Every argument of the node must be
// a whole number in [min, max].
type intRule struct {
	min, max int64
}

var (
	// 0 is the "unset" value for every port key, so it stays legal.
	portRule = intRule{0, 65535}
	// A listening script port is a real port; 0 is not one.
	scriptPortRule = intRule{1, 65535}
	// Whole number, any sign: a non-positive count restores the default, which
	// is documented behaviour, so only the fractional form is refused.
	countRule = intRule{math.MinInt32, math.MaxInt32}
)

// proxyIntRules and friends map a key inside the named block to its rule.
var (
	proxyIntRules = map[string]intRule{
		"listen-port":   portRule,
		"port":          portRule,
		"fallback-port": portRule,
		"max-log-size":  countRule,
	}
	scriptIntRules     = map[string]intRule{"ports": scriptPortRule}
	automationIntRules = map[string]intRule{"max-sessions": countRule}
)

// inspectAgntDocument walks the raw KDL document. It returns an error for a
// number that is fractional or out of range, and one warning per declared key
// that has no effect. data has already been unmarshalled successfully, so a
// parse failure here is not expected; it is returned rather than ignored.
func inspectAgntDocument(data string) (warnings []string, err error) {
	doc, err := kdl.Parse(strings.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse KDL config: %w", err)
	}
	for _, top := range doc.Nodes {
		switch nodeName(top) {
		case "scripts":
			for _, script := range top.Children {
				if err := checkIntNodes(script.Children, scriptIntRules, "scripts."+nodeName(script)); err != nil {
					return nil, err
				}
			}
		case "proxies":
			for _, proxy := range top.Children {
				where := "proxies." + nodeName(proxy)
				if err := checkIntNodes(proxy.Children, proxyIntRules, where); err != nil {
					return nil, err
				}
				if hasChild(proxy, "websocket") {
					warnings = append(warnings, where+".websocket has no effect: WebSocket upgrades are always proxied")
				}
			}
		case "automation":
			if err := checkIntNodes(top.Children, automationIntRules, "automation"); err != nil {
				return nil, err
			}
		case "alerts":
			if hasChild(top, "incident-pipeline") {
				warnings = append(warnings, "alerts.incident-pipeline has no effect: it is deprecated and the incident pipeline is always active")
			}
			for _, c := range top.Children {
				if nodeName(c) == "auto-forward" && hasChild(c, "debounce") {
					warnings = append(warnings, "alerts.auto-forward.debounce has no effect: nothing reads it; forwarded errors are deduplicated and batched by the alert queue")
				}
			}
		case "ai":
			if hasChild(top, "skill") {
				warnings = append(warnings, "ai.skill has no effect: nothing reads it")
			}
			if hasChild(top, "env") {
				warnings = append(warnings, "ai.env has no effect: nothing reads it; set the variables in the environment agnt runs in")
			}
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

func nodeName(n *document.Node) string {
	if n == nil || n.Name == nil {
		return ""
	}
	return n.Name.ValueString()
}

func hasChild(n *document.Node, name string) bool {
	for _, c := range n.Children {
		if nodeName(c) == name {
			return true
		}
	}
	return false
}

// checkIntNodes applies rules to the matching nodes among children.
func checkIntNodes(children []*document.Node, rules map[string]intRule, where string) error {
	for _, c := range children {
		rule, ok := rules[nodeName(c)]
		if !ok {
			continue
		}
		for _, arg := range c.Arguments {
			if err := checkIntValue(arg.Value, rule, where+"."+nodeName(c)); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkIntValue accepts a whole number inside the rule's range. Non-numeric
// values are left to the unmarshaller, which already rejects them by type.
func checkIntValue(v interface{}, rule intRule, where string) error {
	var n int64
	switch val := v.(type) {
	case int:
		n = int64(val)
	case int64:
		n = val
	case float64:
		if val != math.Trunc(val) {
			return fmt.Errorf("%s must be a whole number (got %v)", where, val)
		}
		if val < float64(rule.min) || val > float64(rule.max) {
			return fmt.Errorf("%s must be between %d and %d (got %v)", where, rule.min, rule.max, val)
		}
		return nil
	default:
		return nil
	}
	if n < rule.min || n > rule.max {
		return fmt.Errorf("%s must be between %d and %d (got %d)", where, rule.min, rule.max, n)
	}
	return nil
}
