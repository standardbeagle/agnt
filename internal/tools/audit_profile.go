package tools

import "sort"

// AuditFinding is the profile-projection view over one audit finding.
// Audit tools parse their raw JSON findings into this shape, project with
// ProjectFindingsByProfile, and render. Fields mirror the JS finding shape
// (internal/proxy/scripts): id/type/severity/selector/message/next.
type AuditFinding struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Severity string `json:"severity,omitempty"`
	Selector string `json:"selector,omitempty"`
	Message  string `json:"message,omitempty"`
	Next     string `json:"next,omitempty"`
}

// Audit profile names for ProjectFindingsByProfile.
const (
	// AuditProfileBug renders the top findings by severity (bug-fix triage).
	AuditProfileBug = "bug"
	// AuditProfileRelease renders every finding (release gate).
	AuditProfileRelease = "release"
	// AuditProfileFull renders every finding (default).
	AuditProfileFull = "full"
)

// auditProfileBugLimit caps how many findings the bug profile keeps.
const auditProfileBugLimit = 5

// SeverityRank orders severities for triage: critical < warning < info <
// anything else. Lower ranks are more severe.
func SeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "warning":
		return 1
	case "info":
		return 2
	default:
		return 3
	}
}

// ProjectFindingsByProfile selects which findings a profile renders.
// The projection is a pure function over the raw finding JSON: audit tools
// parse their findings, project once here, and render — the JS producers stay
// unchanged. "bug" keeps the top auditProfileBugLimit findings by severity
// (stable: input order preserved within a severity); "release", "full" and
// the empty default keep everything in input order.
func ProjectFindingsByProfile(findings []AuditFinding, profile string) []AuditFinding {
	if profile != AuditProfileBug {
		out := make([]AuditFinding, len(findings))
		copy(out, findings)
		return out
	}
	ranked := make([]AuditFinding, len(findings))
	copy(ranked, findings)
	sort.SliceStable(ranked, func(i, j int) bool {
		return SeverityRank(ranked[i].Severity) < SeverityRank(ranked[j].Severity)
	})
	if len(ranked) > auditProfileBugLimit {
		ranked = ranked[:auditProfileBugLimit]
	}
	return ranked
}
