package incident

import (
	"strings"

	"github.com/standardbeagle/agnt/internal/alert"
)

// buildCategories maps AlertPattern.Category values that represent build-system
// output to SourceBuildFail. Process-runtime categories map to SourceProcessAlert.
var buildCategories = map[string]bool{
	"webpack": true,
	"vite":    true,
	"nextjs":  true,
	"rebuild": true,
}

// FromAlertMatch converts a pattern-matched line from the AlertScanner into an
// IncidentEvent. Build-failure patterns (webpack, vite, nextjs, rebuild, or any
// pattern whose description contains "build"/"compile") use SourceBuildFail;
// all others use SourceProcessAlert.
func FromAlertMatch(m *alert.AlertMatch, processID string) IncidentEvent {
	src := SourceProcessAlert
	if isBuildPattern(m.Pattern) {
		src = SourceBuildFail
	}

	var sev Severity
	switch m.Pattern.Severity {
	case alert.AlertSeverityError:
		sev = SeverityError
	case alert.AlertSeverityWarning:
		sev = SeverityWarning
	default:
		sev = SeverityInfo
	}

	return NewIncidentEvent(
		src, sev, m.Pattern.Category, m.Line,
		Context{ProcessID: processID},
		nil,
	)
}

func isBuildPattern(p *alert.AlertPattern) bool {
	if buildCategories[p.Category] {
		return true
	}
	lower := strings.ToLower(p.Description)
	return strings.Contains(lower, "build") || strings.Contains(lower, "compile")
}
