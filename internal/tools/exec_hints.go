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
				message: "Consider __devtool.getPosition instead of getBoundingClientRect()",
			},
			{
				pattern: `getComputedStyle\(`,
				message: "Consider __devtool.getComputed instead of getComputedStyle()",
			},
			{
				pattern: `querySelectorAll[^)]*\)\.length`,
				message: "Consider __devtool.auditDOMComplexity instead of querySelectorAll(...).length",
			},
			{
				pattern: `tabindex`,
				message: "Consider __devtool.getTabOrder instead of manual tabindex walk",
			},
			{
				pattern: `addEventListener\(['"]click`,
				message: "Consider __devtool.interactions.getHistory instead of addEventListener(\"click\", ...)",
			},
			{
				pattern: `new MutationObserver\(`,
				message: "Consider __devtool.mutations.getHistory instead of new MutationObserver()",
			},
			{
				pattern: `0\.2126`,
				message: "Consider __devtool.getContrast instead of manual luminance/contrast ratio math",
			},
			{
				pattern: `\.innerHTML`,
				message: "Consider __devtool.captureDOM instead of .innerHTML serialization",
			},
			{
				pattern: `\.value\b`,
				message: "Consider __devtool.captureState instead of gathering form values via .value in a loop",
			},
			{
				pattern: `performance\.getEntries`,
				message: "Consider __devtool.captureNetwork instead of performance.getEntries()",
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
