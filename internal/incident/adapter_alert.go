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
}

// rebuildCategory is the AlertPattern.Category of build-lifecycle progress
// lines ("rebuilding", "build succeeded", "page reload", "file changed:").
// They are signals, not failures: the daemon feeds them to the health tracker
// and to build-success retention, and they must never become incidents.
const rebuildCategory = "rebuild"

// AlertMatchIsIncident reports whether a scanner match belongs in the incident
// inbox. Rebuild-lifecycle matches do not: recording them turned every
// successful rebuild into an `info:build_fail` entry (with build-failure
// remediation hints) and a ping, and re-inserted the very build-success line
// that had just retired the stale errors.
func AlertMatchIsIncident(m *alert.AlertMatch) bool {
	return m != nil && m.Pattern != nil && m.Pattern.Category != rebuildCategory
}

// FromAlertMatch converts a pattern-matched line from the AlertScanner into an
// IncidentEvent. Build-failure patterns (webpack, vite, nextjs, or any
// pattern whose description contains "build"/"compile") use SourceBuildFail;
// all others use SourceProcessAlert. Callers gate on AlertMatchIsIncident
// first; a rebuild-lifecycle match that reaches here anyway is never labelled
// a build failure.
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
	if p.Category == rebuildCategory {
		return false
	}
	if buildCategories[p.Category] {
		return true
	}
	lower := strings.ToLower(p.Description)
	return strings.Contains(lower, "build") || strings.Contains(lower, "compile")
}
