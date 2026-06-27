package tools

import (
	"regexp"
	"sync"
)

// hintRule pairs a compiled regex with the advisory message to emit when it matches.
type hintRule struct {
	re      *regexp.Regexp
	message string
}

// compiledHints holds the rule table, compiled once on first use.
var compiledHints []hintRule
var compileOnce sync.Once

func getHints() []hintRule {
	compileOnce.Do(func() {
		rules := []struct {
			pattern string
			message string
		}{
			{
				pattern: `getBoundingClientRect\(`,
				message: "use __devtool.getPosition — replaces getBoundingClientRect()",
			},
			{
				pattern: `getComputedStyle\(`,
				message: "use __devtool.getComputed — replaces getComputedStyle()",
			},
			{
				pattern: `querySelectorAll[^)]*\)\.length`,
				message: "use __devtool.auditDOMComplexity — replaces querySelectorAll(...).length",
			},
			{
				pattern: `tabindex`,
				message: "use __devtool.getTabOrder — skip manual tabindex walk",
			},
			{
				pattern: `addEventListener\(['"]click`,
				message: "use __devtool.interactions.getHistory — replaces addEventListener(\"click\")",
			},
			{
				pattern: `new MutationObserver\(`,
				message: "use __devtool.mutations.getHistory — replaces new MutationObserver()",
			},
			{
				pattern: `0\.2126`,
				message: "use __devtool.getContrast — skip manual luminance math",
			},
			{
				pattern: `\.innerHTML`,
				message: "use __devtool.captureDOM — replaces .innerHTML serialization",
			},
			{
				pattern: `\.value\b`,
				message: "use __devtool.captureState — replaces .value gather loop",
			},
			{
				pattern: `performance\.getEntries`,
				message: "use __devtool.captureNetwork — replaces performance.getEntries()",
			},
			{
				// Catches agents about to "fix" layering by writing a z-index.
				pattern: `(?i)z-?index`,
				message: "use __devtool.getStacking — find the real stackingRoot + rootTrigger before changing z-index (bumping it rarely fixes cross-stacking-context bugs)",
			},
			{
				pattern: `(?i)position\s*[:=]\s*['"]?fixed`,
				message: "use __devtool.getContainer — check trappedBy before debugging fixed positioning (an ancestor transform/filter can trap a fixed element)",
			},
		}

		compiled := make([]hintRule, 0, len(rules))
		for _, r := range rules {
			re := regexp.MustCompile(r.pattern)
			compiled = append(compiled, hintRule{re: re, message: r.message})
		}
		compiledHints = compiled
	})
	return compiledHints
}

// ScanForHints scans js for raw DOM patterns that duplicate __devtool helpers.
// Returns a slice of advisory hint messages; empty slice means no hints.
func ScanForHints(js string) []string {
	var hints []string
	for _, rule := range getHints() {
		if rule.re.MatchString(js) {
			hints = append(hints, rule.message)
		}
	}
	return hints
}
