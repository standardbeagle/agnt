package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// GetIncidentsInput is the input schema for the get_incidents tool.
type GetIncidentsInput struct {
	Severity     []string `json:"severity,omitempty"      jsonschema:"Filter by severity: critical/error/warning/info (default: all)"`
	Since        string   `json:"since,omitempty"         jsonschema:"Cursor from prior pull (RFC3339 timestamp) or duration like '5m'"`
	Fingerprints []string `json:"fingerprints,omitempty"  jsonschema:"Retrieve specific incident fingerprints"`
	Sources      []string `json:"sources,omitempty"       jsonschema:"Filter by source: browser_js/http_5xx/http_4xx/transport_err/proxy_diag/process_alert/process_crash/build_fail/port_conflict/shutdown/hook_stop_failure"`
	ProxyID      string   `json:"proxy_id,omitempty"      jsonschema:"Filter to specific proxy"`
	ProcessID    string   `json:"process_id,omitempty"    jsonschema:"Filter to specific process"`
	Detail       string   `json:"detail,omitempty"        jsonschema:"'summary' (default) or 'full' to hydrate full payload from blob store"`
	MarkRead     bool     `json:"mark_read,omitempty"     jsonschema:"Advance cursor and mark returned incidents as read"`
	Limit        int      `json:"limit,omitempty"         jsonschema:"Max incidents to return (default: 20, max: 100)"`
	Raw          bool     `json:"raw,omitempty"           jsonschema:"Return full JSON instead of compact text"`
}

// GetIncidentsOutput is the output for the get_incidents tool.
type GetIncidentsOutput struct {
	Incidents  []incidentView `json:"incidents"`
	InboxAfter inboxStats     `json:"inbox_after"`
	Cursor     string         `json:"replay_cursor,omitempty"`
	NextTools  []toolHint     `json:"next_tools,omitempty"`
	NextSkills []string       `json:"next_skills,omitempty"`
	Truncated  bool           `json:"truncated"`
	// PipelineEnabled reports whether the caller currently has a registered
	// session inbox. False means the session pipeline is unavailable (for
	// example during teardown), never that project config disabled recording.
	PipelineEnabled bool `json:"pipeline_enabled"`
}

type incidentView struct {
	ID          string                       `json:"id"`
	Fingerprint string                       `json:"fingerprint"`
	FirstSeen   time.Time                    `json:"first_seen"`
	LastSeen    time.Time                    `json:"last_seen"`
	Count       int                          `json:"count"`
	Severity    string                       `json:"severity"`
	Source      string                       `json:"source"`
	Category    string                       `json:"category,omitempty"`
	Summary     string                       `json:"summary,omitempty"`
	Payload     *string                      `json:"payload,omitempty"`
	Ctx         protocol.IncidentContext     `json:"context,omitempty"`
	Remediation protocol.IncidentRemediation `json:"remediation,omitempty"`
	Read        bool                         `json:"read"`
}

type inboxStats struct {
	Critical int   `json:"critical"`
	Error    int   `json:"error"`
	Warning  int   `json:"warning"`
	Info     int   `json:"info"`
	Dropped  int64 `json:"dropped"`
	New      int   `json:"new"`
}

type toolHint struct {
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Rationale string         `json:"rationale"`
}

// RegisterGetIncidentsTool registers the get_incidents MCP tool.
func RegisterGetIncidentsTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "get_incidents",
		Description: `[PREFERRED] Pull incidents from the session incident inbox.

Supersedes get_errors. Provides cursor-based resumable pulls, remediation hints,
next-tool suggestions, and skill hints for each incident.

Sources: browser JS errors, HTTP 4xx/5xx, transport errors, proxy diagnostics,
process alerts, process crashes, build failures, port conflicts.

Examples:
  get_incidents {}
  get_incidents {severity: ["critical","error"]}
  get_incidents {since: "5m"}
  get_incidents {mark_read: true}
  get_incidents {detail: "full"}
  get_incidents {raw: true, limit: 50}`,
	}, makeGetIncidentsHandler(dt))
}

func makeGetIncidentsHandler(dt *DaemonTools) func(context.Context, *mcp.CallToolRequest, GetIncidentsInput) (*mcp.CallToolResult, GetIncidentsOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input GetIncidentsInput) (*mcp.CallToolResult, GetIncidentsOutput, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}

		// The hub parses Since strictly as RFC3339, so a duration form like "5m"
		// (which the schema advertises) would be silently ignored there. Resolve
		// it to an absolute RFC3339 timestamp here where duration parsing lives.
		since := input.Since
		if t := parseSince(input.Since); t != nil && !t.IsZero() {
			since = t.Format(time.RFC3339)
		}

		filter := protocol.IncidentQueryFilter{
			Severities:   input.Severity,
			Since:        since,
			Fingerprints: input.Fingerprints,
			Sources:      input.Sources,
			ProxyID:      input.ProxyID,
			ProcessID:    input.ProcessID,
			Detail:       input.Detail,
			MarkRead:     input.MarkRead,
			Limit:        limit,
		}

		if dt == nil {
			return errorResult("incident query unavailable: no daemon client"), GetIncidentsOutput{}, nil
		}
		// A failed connect or query must be surfaced, not swallowed: rendering an
		// empty inbox for a down daemon reports a false-healthy state and the
		// agent stops investigating. Reserve the empty-result path for a genuinely
		// empty inbox (PipelineEnabled distinguishes "pipeline off" from "no data").
		if err := dt.ensureConnected(); err != nil {
			return errorResult("incident query failed: cannot reach daemon: " + err.Error()), GetIncidentsOutput{}, nil
		}
		result, err := dt.client.IncidentQuery(filter)
		if err != nil {
			return errorResult("incident query failed: " + err.Error()), GetIncidentsOutput{}, nil
		}
		if result == nil {
			result = &protocol.IncidentQueryResult{}
		}

		// The hub over-fetches and truncates server-side so the cursor and the
		// mark-read set cover exactly this page; trust its truncation signal
		// rather than re-truncating (which would drop an already-marked record).
		truncated := result.Truncated

		views := make([]incidentView, 0, len(result.Incidents))
		for _, rec := range result.Incidents {
			views = append(views, recordToView(rec))
		}

		// Build IncidentViews for aggregation
		incViews := make([]incident.IncidentView, 0, len(views))
		for _, v := range views {
			incViews = append(incViews, incident.IncidentView{
				Count:  v.Count,
				Source: incident.Source(v.Source),
				Remediation: incident.Remediation{
					PrimaryTool: v.Remediation.PrimaryTool,
					PrimaryArgs: v.Remediation.PrimaryArgs,
					SkillHint:   v.Remediation.SkillHint,
				},
			})
		}
		skill, tools := incident.AggregateRemediation(incViews)

		var nextTools []toolHint
		for _, t := range tools {
			nextTools = append(nextTools, toolHint{
				Tool:      t.Tool,
				Args:      t.Args,
				Rationale: t.Rationale,
			})
		}
		var nextSkills []string
		if skill != "" {
			nextSkills = []string{skill}
		}

		stats := result.InboxStats
		output := GetIncidentsOutput{
			Incidents: views,
			InboxAfter: inboxStats{
				Critical: stats.Critical,
				Error:    stats.Error,
				Warning:  stats.Warning,
				Info:     stats.Info,
				Dropped:  stats.Dropped,
				New:      stats.New,
			},
			Cursor:          result.Cursor,
			NextTools:       nextTools,
			NextSkills:      nextSkills,
			Truncated:       truncated,
			PipelineEnabled: result.PipelineEnabled,
		}

		if input.Raw {
			b, _ := json.Marshal(output)
			return mcpText(string(b)), output, nil
		}

		return mcpText(formatIncidentsCompact(output)), output, nil
	}
}

func recordToView(rec protocol.IncidentRecord) incidentView {
	v := incidentView{
		ID:          rec.ID,
		Fingerprint: rec.Fingerprint,
		Count:       rec.Count,
		Severity:    rec.Severity,
		Source:      rec.Source,
		Category:    rec.Category,
		Summary:     rec.Summary,
		Payload:     rec.Payload,
		Ctx:         rec.Context,
		Remediation: rec.Remediation,
		Read:        rec.Read,
	}
	if t, err := time.Parse(time.RFC3339, rec.FirstSeen); err == nil {
		v.FirstSeen = t
	}
	if t, err := time.Parse(time.RFC3339, rec.LastSeen); err == nil {
		v.LastSeen = t
	}
	return v
}

func formatIncidentsCompact(out GetIncidentsOutput) string {
	var sb strings.Builder

	s := out.InboxAfter
	sb.WriteString(fmt.Sprintf("=== Incidents (%d) === [inbox: crit=%d err=%d warn=%d info=%d new=%d]\n",
		len(out.Incidents), s.Critical, s.Error, s.Warning, s.Info, s.New))

	if len(out.Incidents) == 0 {
		if !out.PipelineEnabled {
			sb.WriteString("\n(incident session inbox unavailable — no inbox to report)\n")
		} else {
			sb.WriteString("\n(no incidents)\n")
		}
	} else {
		sb.WriteString("\n")
		for _, iv := range out.Incidents {
			age := ""
			if !iv.LastSeen.IsZero() {
				age = fmt.Sprintf(", %s ago", formatAge(time.Since(iv.LastSeen)))
			}
			sb.WriteString(fmt.Sprintf("[%s:%s] %s (%dx%s)\n",
				iv.Severity, iv.Source, iv.Category, iv.Count, age))
			if iv.Summary != "" {
				sb.WriteString("  " + iv.Summary + "\n")
			}
			// Hydrated payload (detail:"full"). Render in compact mode too — it was
			// previously visible only under raw:true, making detail:"full" a no-op
			// for the default view. Truncated to keep the compact output compact.
			if iv.Payload != nil && *iv.Payload != "" {
				oneLine := strings.ReplaceAll(strings.TrimSpace(*iv.Payload), "\n", " ")
				sb.WriteString("  payload: " + truncate(oneLine, 500) + "\n")
			}
			if iv.Ctx.URL != "" {
				sb.WriteString("  → " + iv.Ctx.URL + "\n")
			}
			if iv.Remediation.PrimaryTool != "" {
				args := formatArgs(iv.Remediation.PrimaryArgs)
				sb.WriteString(fmt.Sprintf("  next: %s%s\n", iv.Remediation.PrimaryTool, args))
			}
			if iv.Remediation.SkillHint != "" {
				sb.WriteString("  skill: " + iv.Remediation.SkillHint + "\n")
			}
			sb.WriteString("\n")
		}
	}

	if out.Truncated {
		sb.WriteString("(results truncated)\n")
	}

	// Aggregate remediation across the returned incidents — the dominant skill
	// and the deduped set of primary tools, weighted by occurrence. Rendered
	// here so the compact (default) mode surfaces it; previously it was only
	// visible under raw:true.
	if len(out.NextTools) > 0 || len(out.NextSkills) > 0 || out.Cursor != "" {
		sb.WriteString("=== Next ===\n")
		for _, t := range out.NextTools {
			sb.WriteString(fmt.Sprintf("tool: %s%s\n", t.Tool, formatArgs(t.Args)))
		}
		for _, s := range out.NextSkills {
			sb.WriteString("skill: " + s + "\n")
		}
		if out.Cursor != "" {
			sb.WriteString("replay_cursor: " + out.Cursor + "\n")
		}
	}

	return sb.String()
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return " " + strings.Join(parts, " ")
}

func mcpText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}
