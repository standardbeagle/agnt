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
	// Action selects the retention verb. Empty/"query" is the default read.
	// error_id/tag mirror get_errors' spelling so a caller migrating off it does
	// not have to rename anything; for incidents the id is the fingerprint from
	// a prior result.
	Action       string   `json:"action,omitempty"        jsonschema:"'query' (default) | 'pin' | 'unpin' | 'clear'. pin/unpin keep an incident alive past eviction and every retention clear; clear retires the session's unpinned incidents"`
	ErrorID      string   `json:"error_id,omitempty"      jsonschema:"Pin/unpin target: the incident fingerprint (or id) from a prior get_incidents result"`
	Tag          string   `json:"tag,omitempty"           jsonschema:"Note stored with a pin, returned on the pinned item"`
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
	// CollectionWarnings names every way this view is known to be PARTIAL —
	// events the bus dropped before they reached any inbox, and payloads a
	// detail:"full" pull could not hydrate. A non-empty list means "some
	// incidents are missing from this answer", which must never be silently
	// rendered as a clean result.
	CollectionWarnings []string `json:"collection_warnings,omitempty" jsonschema:"Ways this view is partial; non-empty means some incidents are missing from the answer"`
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
	// Pinned reports that this incident is exempt from eviction and from every
	// retention clear; Tag is the note stored at pin time. Both are required for
	// pinning to be observable at all — without them the agent cannot tell which
	// incidents it saved.
	Pinned bool   `json:"pinned,omitempty"`
	Tag    string `json:"tag,omitempty"`
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
		filter := buildGetIncidentsFilter(input)

		if dt == nil {
			return fail[GetIncidentsOutput]("incident query unavailable: no daemon client")
		}
		// A failed connect or query must be surfaced, not swallowed: rendering an
		// empty inbox for a down daemon reports a false-healthy state and the
		// agent stops investigating. Reserve the empty-result path for a genuinely
		// empty inbox (PipelineEnabled distinguishes an unavailable session inbox
		// from a registered inbox with no data).
		if err := dt.ensureConnected(); err != nil {
			return fail[GetIncidentsOutput]("incident query failed: cannot reach daemon: " + err.Error())
		}
		if isIncidentRetentionAction(input.Action) {
			return dt.handleIncidentRetentionAction(input)
		}
		result, err := dt.client.IncidentQuery(filter)
		if err != nil {
			return fail[GetIncidentsOutput]("incident query failed: " + err.Error())
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
			Cursor:             result.Cursor,
			NextTools:          nextTools,
			NextSkills:         nextSkills,
			Truncated:          truncated,
			PipelineEnabled:    result.PipelineEnabled,
			CollectionWarnings: result.CollectionWarnings,
		}

		if input.Raw {
			b, _ := json.Marshal(output)
			return mcpText(string(b)), output, nil
		}

		return mcpText(formatIncidentsCompact(output)), output, nil
	}
}

// isIncidentRetentionAction reports whether action asks for a retention verb
// rather than the default query. An unknown action is NOT swallowed as a query:
// it routes here and is rejected, so a typo cannot silently return a read.
func isIncidentRetentionAction(action string) bool {
	switch action {
	case "", "query":
		return false
	default:
		return true
	}
}

// handleIncidentRetentionAction executes pin/unpin/clear against the caller's
// session inbox. Mirrors handleErrorRetentionAction (get_errors) so the two
// tools behave the same way while both exist.
//
// The rendered message is returned as tool content only; GetIncidentsOutput
// deliberately gains no summary field, because that would change a separate,
// unrelated migration divergence.
func (dt *DaemonTools) handleIncidentRetentionAction(input GetIncidentsInput) (*mcp.CallToolResult, GetIncidentsOutput, error) {
	switch input.Action {
	case "pin", "unpin":
		if input.ErrorID == "" {
			return fail[GetIncidentsOutput](validationError("get_incidents", fmt.Errorf(
				"action %q requires error_id (the incident fingerprint from a prior get_incidents result)", input.Action)))
		}
		payload := protocol.IncidentPinPayload{Fingerprint: input.ErrorID, Tag: input.Tag}

		if input.Action == "unpin" {
			res, err := dt.client.IncidentUnpin(payload)
			if err != nil {
				return fail[GetIncidentsOutput]("unpin failed: " + err.Error())
			}
			return mcpText(incidentRetentionMessage(res.Message,
				fmt.Sprintf("Incident %s unpinned — normal retention applies again.", input.ErrorID))), GetIncidentsOutput{}, nil
		}

		res, err := dt.client.IncidentPin(payload)
		if err != nil {
			return fail[GetIncidentsOutput]("pin failed: " + err.Error())
		}
		msg := incidentRetentionMessage(res.Message, fmt.Sprintf("Incident %s pinned.", input.ErrorID))
		if res.PinLimit > 0 {
			msg += fmt.Sprintf(" (%d/%d pins used)", res.PinnedCount, res.PinLimit)
		}
		return mcpText(msg), GetIncidentsOutput{}, nil

	case "clear":
		res, err := dt.client.IncidentClear()
		if err != nil {
			return fail[GetIncidentsOutput]("clear failed: " + err.Error())
		}
		return mcpText(incidentRetentionMessage(res.Message, "Incidents cleared (pinned entries kept).")), GetIncidentsOutput{}, nil
	}
	return fail[GetIncidentsOutput](validationError("get_incidents", fmt.Errorf(
		"unknown action %q — want query, pin, unpin, or clear", input.Action)))
}

func incidentRetentionMessage(fromDaemon, fallback string) string {
	if fromDaemon != "" {
		return fromDaemon
	}
	return fallback
}

// buildGetIncidentsFilter translates a get_incidents query into an inbox
// filter. Extracted from the handler so the get_errors/get_incidents oracle can
// compare both tools' filter construction against the real code rather than a
// re-derived copy of it — see get_errors_oracle_test.go.
func buildGetIncidentsFilter(input GetIncidentsInput) protocol.IncidentQueryFilter {
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

	return protocol.IncidentQueryFilter{
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
		Pinned:      rec.Pinned,
		Tag:         rec.Tag,
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

	// Partial-collection warnings render immediately under the header, BEFORE
	// the incident list and before the "(no incidents)" line. A degraded view
	// that reads as a clean all-clear is the failure mode this exists to
	// prevent, so it must not be findable only at the bottom of a long page.
	if len(out.CollectionWarnings) > 0 {
		sb.WriteString("\n!! PARTIAL VIEW — incidents are missing from this answer:\n")
		for _, w := range out.CollectionWarnings {
			sb.WriteString("  - " + w + "\n")
		}
	}

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
			pin := ""
			if iv.Pinned {
				pin = " [pinned"
				if iv.Tag != "" {
					pin += ": " + iv.Tag
				}
				pin += "]"
			}
			sb.WriteString(fmt.Sprintf("[%s:%s] %s (%dx%s)%s\n",
				iv.Severity, iv.Source, iv.Category, iv.Count, age, pin))
			// The fingerprint is the pin/unpin target, so compact mode has to
			// render it: without an id here the retention verbs are unreachable
			// from the default view.
			sb.WriteString("  id: " + iv.Fingerprint + "\n")
			if iv.Summary != "" {
				sb.WriteString("  " + iv.Summary + "\n")
			}
			// Hydrated payload (detail:"full") renders in compact mode as well as
			// raw JSON. Truncate it to keep the compact output compact.
			if iv.Payload != nil && *iv.Payload != "" {
				oneLine := strings.ReplaceAll(strings.TrimSpace(*iv.Payload), "\n", " ")
				sb.WriteString("  payload: " + truncate(oneLine, 500) + "\n")
			}
			if iv.Ctx.Location != "" {
				sb.WriteString("  at: " + iv.Ctx.Location + "\n")
			}
			if iv.Ctx.URL != "" {
				page := iv.Ctx.URL
				// The frame is what makes two same-message incidents distinct
				// entries, so it renders next to the page it qualifies.
				if iv.Ctx.FrameID != "" {
					page += " [frame " + iv.Ctx.FrameID + "]"
				}
				sb.WriteString("  → " + page + "\n")
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
