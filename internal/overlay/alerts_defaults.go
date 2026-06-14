package overlay

import "github.com/standardbeagle/agnt/internal/classify"

// DefaultAlertPatterns returns the built-in set of alert patterns for common
// dev server frameworks and languages.
//
// The rule data is owned by internal/classify (the single source of truth
// shared with the `proc output` extract path); this function adapts
// classify.LineRule into the overlay AlertPattern shape the AlertScanner
// consumes. Add or edit rules in internal/classify/linerules.go.
func DefaultAlertPatterns() []*AlertPattern {
	rules := classify.DefaultLineRules()
	out := make([]*AlertPattern, len(rules))
	for i, r := range rules {
		out[i] = &AlertPattern{
			ID:          r.ID,
			Pattern:     r.Pattern,
			Severity:    AlertSeverity(r.Severity),
			Category:    r.Category,
			Description: r.Description,
		}
	}
	return out
}
