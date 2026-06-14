// Package classify is the single source of truth for classifying a line (or
// block) of process output into a severity + category, and — where the format
// is recognised — structured fields (file/line/col/code, or a folded
// multi-line error).
//
// It merges what used to be two independent, drifting banks:
//   - internal/overlay/alerts_defaults.go — broad per-line toast patterns
//   - internal/tools/build_error_parsers.go — precise structured build errors
//
// Both the overlay AlertScanner and the `proc output` extract path now source
// their classification rules from this package, so the rule set has one
// definition instead of two that silently drift.
package classify

// Severity orders output by urgency. The string values match the legacy
// overlay.AlertSeverity / incident.Severity constants so consumers can convert
// with a plain string cast.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)
