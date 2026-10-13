package tools

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// Bench-rendering wrappers: the agentbench contract fixtures
// (internal/agentbench/testdata/contract) must carry the SHIPPED compact
// text — including every `next:` line — not hand-typed copies. These
// wrappers expose the existing pure formatters over fixture-supplied
// inputs.
//
// This is a cross-package TEST HARNESS, not production API: it is a plain .go
// file (agentbench's tests import it, so it cannot be a _test.go file) fenced
// by a testing.TB parameter on every entry point, per
// .claude/rules/testing-conventions.md §2. Production code has no testing.TB to
// pass, so it cannot call these.

// BenchIncidentView is the minimal incident shape a contract fixture feeds
// into the shipped get_incidents compact formatter.
type BenchIncidentView struct {
	Severity    string         `json:"severity"`
	Source      string         `json:"source"`
	Category    string         `json:"category,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Fingerprint string         `json:"fingerprint"`
	Count       int            `json:"count,omitempty"`
	URL         string         `json:"url,omitempty"`
	NextTool    string         `json:"next_tool,omitempty"`
	NextArgs    map[string]any `json:"next_args,omitempty"`
}

// RenderIncidentsCompactForBench renders views through
// formatIncidentsCompact, the shipped get_incidents compact renderer.
func RenderIncidentsCompactForBench(t testing.TB, views []BenchIncidentView) string {
	t.Helper()
	out := GetIncidentsOutput{PipelineEnabled: true}
	for _, v := range views {
		iv := incidentView{
			ID:          v.Fingerprint,
			Fingerprint: v.Fingerprint,
			Severity:    v.Severity,
			Source:      v.Source,
			Category:    v.Category,
			Summary:     v.Summary,
			Count:       v.Count,
		}
		if iv.Count == 0 {
			iv.Count = 1
		}
		if v.URL != "" {
			iv.Ctx = protocol.IncidentContext{URL: v.URL}
		}
		if v.NextTool != "" {
			iv.Remediation = protocol.IncidentRemediation{
				PrimaryTool: v.NextTool,
				PrimaryArgs: v.NextArgs,
			}
		}
		out.Incidents = append(out.Incidents, iv)
	}
	return formatIncidentsCompact(out)
}

// RenderBufferAuditCompactForBench renders one API/loading audit payload
// through formatBufferAuditCompact.
func RenderBufferAuditCompactForBench(t testing.TB, headline string, parsed map[string]any, proxyID, profile string) string {
	t.Helper()
	return formatBufferAuditCompact(headline, parsed, proxyID, profile)
}

// RenderResponsiveCompactForBench renders responsive_audit raw JSON through
// renderResponsiveCompact.
func RenderResponsiveCompactForBench(t testing.TB, rawJSON []byte, profile string) (string, error) {
	t.Helper()
	return renderResponsiveCompact(rawJSON, profile)
}

// RenderDiagnoseLayoutCompactForBench renders diagnose kind=layout findings
// through renderDiagnoseLayoutCompact.
func RenderDiagnoseLayoutCompactForBench(t testing.TB, proxyID string, findings []finding.Finding) string {
	t.Helper()
	return renderDiagnoseLayoutCompact(proxyID, findings, nil, "")
}

// RenderDiagnoseClickCompactForBench renders diagnose kind=click findings
// through renderDiagnoseClickCompact.
func RenderDiagnoseClickCompactForBench(t testing.TB, proxyID, verdict string, findings []finding.Finding) string {
	t.Helper()
	return renderDiagnoseClickCompact(proxyID, verdict, findings, nil, "")
}
