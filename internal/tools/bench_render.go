package tools

import (
	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// Bench-rendering wrappers: the agentbench contract fixtures
// (internal/agentbench/testdata/contract) must carry the SHIPPED compact
// text — including every `next:` line — not hand-typed copies. These
// wrappers expose the existing pure formatters over fixture-supplied
// inputs. Additive export for the test harness; no behaviour change.

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
func RenderIncidentsCompactForBench(views []BenchIncidentView) string {
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
func RenderBufferAuditCompactForBench(headline string, parsed map[string]any, proxyID, profile string) string {
	return formatBufferAuditCompact(headline, parsed, proxyID, profile)
}

// RenderResponsiveCompactForBench renders responsive_audit raw JSON through
// renderResponsiveCompact.
func RenderResponsiveCompactForBench(rawJSON []byte, profile string) (string, error) {
	return renderResponsiveCompact(rawJSON, profile)
}

// RenderDiagnoseLayoutCompactForBench renders diagnose kind=layout findings
// through renderDiagnoseLayoutCompact.
func RenderDiagnoseLayoutCompactForBench(proxyID string, findings []finding.Finding) string {
	return renderDiagnoseLayoutCompact(proxyID, findings, nil, "")
}

// RenderDiagnoseClickCompactForBench renders diagnose kind=click findings
// through renderDiagnoseClickCompact.
func RenderDiagnoseClickCompactForBench(proxyID, verdict string, findings []finding.Finding) string {
	return renderDiagnoseClickCompact(proxyID, verdict, findings, nil, "")
}
