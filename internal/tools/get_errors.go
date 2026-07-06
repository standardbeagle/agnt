package tools

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// GetErrorsInput is the input for the get_errors tool.
type GetErrorsInput struct {
	ProcessID       string `json:"process_id,omitempty" jsonschema:"Filter to specific process"`
	ProxyID         string `json:"proxy_id,omitempty" jsonschema:"Filter to specific proxy (default: all active proxies)"`
	Since           string `json:"since,omitempty" jsonschema:"Override recency filter (RFC3339 or duration like '5m')"`
	IncludeWarnings *bool  `json:"include_warnings,omitempty" jsonschema:"Include warnings (default: true)"`
	Limit           int    `json:"limit,omitempty" jsonschema:"Max errors to return (default: 25)"`
	Raw             bool   `json:"raw,omitempty" jsonschema:"Return full JSON with all fields"`
	Global          bool   `json:"global,omitempty" jsonschema:"Bypass session scoping: include errors from all projects instead of only the current one (default: false)"`
}

// GetErrorsOutput is the output for the get_errors tool.
type GetErrorsOutput struct {
	ErrorCount   int    `json:"error_count"`
	WarningCount int    `json:"warning_count"`
	Summary      string `json:"summary,omitempty"`
}

// unifiedError is the internal representation for deduplication and sorting.
type unifiedError struct {
	ID       string    `json:"id"`       // stable 8-char hex: sha256(source+category+message+location)[:4 bytes]
	Source   string    `json:"source"`   // "process:<id>" or "browser:js" or "proxy:http" or "proxy:diagnostic"
	Severity string    `json:"severity"` // "error" or "warning"
	Category string    `json:"category"` // e.g. "TypeError", "COMPILE ERROR", "500 Internal Server Error"
	Message  string    `json:"message"`
	Location string    `json:"location,omitempty"` // file:line:col
	Page     string    `json:"page,omitempty"`     // page URL
	FrameID  string    `json:"frame_id,omitempty"` // emitting content frame (always-wrap model)
	Count    int       `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}

// deduplication key for a unified error. FrameID is included so the same error
// raised in two distinct content frames is not collapsed into one (each frame
// is a distinct context — docs/responsive-canonical-target.md §5.2/§6.2).
func (e *unifiedError) dedupKey() string {
	return e.Source + "|" + e.Category + "|" + e.Message + "|" + e.Location + "|" + e.FrameID
}

// makeGetErrorsHandler creates the get_errors handler, which collects errors
// via the daemon IPC path.
func (dt *DaemonTools) makeGetErrorsHandler() func(context.Context, *mcp.CallToolRequest, GetErrorsInput) (*mcp.CallToolResult, GetErrorsOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input GetErrorsInput) (*mcp.CallToolResult, GetErrorsOutput, error) {
		if err := validateGetErrorsInput(input); err != nil {
			return errorResult(validationError("get_errors", err)), GetErrorsOutput{}, nil
		}

		includeWarnings := true
		if input.IncludeWarnings != nil {
			includeWarnings = *input.IncludeWarnings
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 25
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), GetErrorsOutput{}, nil
		}

		// Shim over the incident pipeline when it is enabled for this session:
		// project the same inbox get_incidents reads (fingerprint IDs) instead
		// of the legacy alert/proxy stores, so the two tools present one
		// coherent, non-duplicated view. Cross-project (global) requests and
		// pipeline-off sessions fall through to the legacy collectors, which is
		// the only path that can serve them.
		if !input.Global {
			if incidentErrors, ok := dt.collectIncidentErrors(input, includeWarnings); ok {
				result, output := formatErrorsOutput(incidentErrors, includeWarnings, limit, input.Raw)
				return result, output, nil
			}
		}

		allErrors, toolErr := dt.collectDaemonErrors(input)
		if toolErr != nil {
			return toolErr, GetErrorsOutput{}, nil
		}

		result, output := formatErrorsOutput(allErrors, includeWarnings, limit, input.Raw)
		return result, output, nil
	}
}

// collectIncidentErrors projects the session incident inbox into unified errors
// when the incident pipeline is active for the caller's session. The second
// return is false when the pipeline is off (or the session is unattached), in
// which case the caller must use the legacy collectors. Incident fingerprints
// become the unified-error ID so get_errors and get_incidents share IDs.
func (dt *DaemonTools) collectIncidentErrors(input GetErrorsInput, includeWarnings bool) ([]unifiedError, bool) {
	filter := protocol.IncidentQueryFilter{
		ProcessID: input.ProcessID,
		ProxyID:   input.ProxyID,
		Since:     input.Since,
		// Pull a wide page; formatErrorsOutput applies the display limit after
		// dedup/sort so the count reflects the whole inbox, not a truncated page.
		Limit: 100,
	}
	if !includeWarnings {
		filter.Severities = []string{"critical", "error"}
	}

	res, err := dt.client.IncidentQuery(filter)
	if err != nil || res == nil || !res.PipelineEnabled {
		return nil, false
	}

	out := make([]unifiedError, 0, len(res.Incidents))
	for _, rec := range res.Incidents {
		out = append(out, incidentRecordToUnifiedError(rec))
	}
	return out, true
}

// incidentRecordToUnifiedError maps an incident inbox record onto the unified
// error shape. Severity is folded to the two-level error/warning scale
// get_errors sorts and counts on (critical→error, info→warning).
func incidentRecordToUnifiedError(rec protocol.IncidentRecord) unifiedError {
	severity := "error"
	switch rec.Severity {
	case "warning", "info":
		severity = "warning"
	}
	count := rec.Count
	if count <= 0 {
		count = 1
	}
	lastSeen, _ := time.Parse(time.RFC3339, rec.LastSeen)
	return unifiedError{
		ID:       rec.Fingerprint, // correlatable with get_incidents
		Source:   rec.Source,
		Severity: severity,
		Category: rec.Category,
		Message:  rec.Summary,
		Page:     rec.Context.URL,
		Count:    count,
		LastSeen: lastSeen,
	}
}

// collectDaemonErrors collects errors via the daemon IPC path.
func (dt *DaemonTools) collectDaemonErrors(input GetErrorsInput) ([]unifiedError, *mcp.CallToolResult) {
	allErrors := make([]unifiedError, 0)

	// 1. Collect process alerts
	processErrors, procErr := dt.collectProcessAlerts(input.ProcessID, input.Since, input.Global)
	if procErr != nil {
		return nil, procErr
	}
	allErrors = append(allErrors, processErrors...)

	// 2. Collect startup errors
	startupErrors, startupErr := dt.collectStartupErrors(input.ProcessID, input.Since, input.Global)
	if startupErr != nil {
		return nil, startupErr
	}
	allErrors = append(allErrors, startupErrors...)

	// 3. Collect proxy errors
	proxyErrors, proxyErr := dt.collectProxyErrors(input.ProxyID, input.Since, input.Global)
	if proxyErr != nil {
		return nil, proxyErr
	}
	allErrors = append(allErrors, proxyErrors...)

	return allErrors, nil
}

// formatErrorsOutput applies deduplication, filtering, sorting, limiting, and formatting
// to a collected set of unified errors.
func formatErrorsOutput(allErrors []unifiedError, includeWarnings bool, limit int, raw bool) (*mcp.CallToolResult, GetErrorsOutput) {
	// Deduplicate
	allErrors = deduplicateErrors(allErrors)

	// Filter warnings if not wanted
	if !includeWarnings {
		filtered := allErrors[:0]
		for _, e := range allErrors {
			if e.Severity == "error" {
				filtered = append(filtered, e)
			}
		}
		allErrors = filtered
	}

	// Sort: errors first, then warnings; within each, most recent first
	sort.Slice(allErrors, func(i, j int) bool {
		li, lj := allErrors[i].Severity, allErrors[j].Severity
		if li != lj {
			if li == "error" {
				return true
			}
			if lj == "error" {
				return false
			}
		}
		return allErrors[i].LastSeen.After(allErrors[j].LastSeen)
	})

	// Count before limiting
	errorCount, warningCount := 0, 0
	for _, e := range allErrors {
		if e.Severity == "error" {
			errorCount++
		} else {
			warningCount++
		}
	}

	// Apply limit
	if len(allErrors) > limit {
		allErrors = allErrors[:limit]
	}

	// Format output
	output := GetErrorsOutput{
		ErrorCount:   errorCount,
		WarningCount: warningCount,
	}

	if raw {
		b, _ := json.Marshal(allErrors)
		output.Summary = string(b)
	} else {
		output.Summary = formatCompactErrors(allErrors, errorCount, warningCount)
	}

	return nil, output
}

// collectProcessAlerts queries the daemon alert store and converts to unified errors.
// Scope: global bypasses the session-scope chokepoint (cross-project); otherwise
// the query is scoped to the caller's session/project so other projects' alerts
// don't leak in. The MCP daemon connection is not session-bound, so the project
// is named explicitly via SessionCode/Directory (mirrors collectProxyErrors).
func (dt *DaemonTools) collectProcessAlerts(processID, since string, global bool) ([]unifiedError, *mcp.CallToolResult) {
	filter := protocol.AlertQueryFilter{
		ProcessID: processID,
		Since:     since,
		Global:    global,
	}
	if !global {
		if sessionCode := dt.SessionCode(); sessionCode != "" {
			filter.SessionCode = sessionCode
		} else if p := getProjectPath(); p != "" {
			filter.Directory = p
		}
	}

	result, err := dt.client.AlertQuery(filter)
	if err != nil {
		// Non-fatal: daemon might not have alert store yet
		return nil, nil
	}

	alerts, ok := result["alerts"].([]interface{})
	if !ok {
		return nil, nil
	}

	var errors []unifiedError
	for _, a := range alerts {
		am, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		if ue := alertMapToUnifiedError(am); ue != nil {
			errors = append(errors, *ue)
		}
	}

	return errors, nil
}

// collectStartupErrors queries the daemon startup log for error-level entries.
// Scope mirrors collectProcessAlerts: global bypasses the session-scope
// chokepoint (cross-project); otherwise the query is scoped to the caller's
// session/project so other projects' startup events don't leak in.
func (dt *DaemonTools) collectStartupErrors(processID, since string, global bool) ([]unifiedError, *mcp.CallToolResult) {
	dirFilter := protocol.DirectoryFilter{Global: global}
	if !global {
		if sessionCode := dt.SessionCode(); sessionCode != "" {
			dirFilter.SessionCode = sessionCode
		} else if p := getProjectPath(); p != "" {
			dirFilter.Directory = p
		}
	}
	result, err := dt.client.StartupLog(50, dirFilter)
	if err != nil {
		// Non-fatal: daemon might not support startup log
		return nil, nil
	}

	entries, ok := result["entries"].([]interface{})
	if !ok {
		return nil, nil
	}

	sinceTime := parseSince(since)

	var errors []unifiedError
	for _, e := range entries {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}

		// Filter by process_id if specified
		if processID != "" {
			if getString(em, "process_id") != processID {
				continue
			}
		}

		// Filter by since if specified
		ts := getTime(em, "timestamp")
		if sinceTime != nil && !sinceTime.IsZero() && ts.Before(*sinceTime) {
			continue
		}

		converted := convertStartupLogEntry(em)
		if converted != nil {
			errors = append(errors, *converted)
		}
	}

	return errors, nil
}

// collectProxyErrors lists proxies and queries their logs for errors.
// global bypasses the session-scope chokepoint (cross-project); otherwise the
// proxy list is scoped to the caller's session/project so other projects'
// proxy errors don't leak in — consistent with collectProcessAlerts /
// collectStartupErrors so a single get_errors {global:true} is uniform.
func (dt *DaemonTools) collectProxyErrors(proxyID, since string, global bool) ([]unifiedError, *mcp.CallToolResult) {
	// Build directory filter for proxy list
	dirFilter := protocol.DirectoryFilter{Global: global}
	if !global {
		if sessionCode := dt.SessionCode(); sessionCode != "" {
			dirFilter.SessionCode = sessionCode
		} else if p := getProjectPath(); p != "" {
			dirFilter.Directory = p
		}
	}

	var proxyIDs []string

	if proxyID != "" {
		proxyIDs = []string{proxyID}
	} else {
		// List all active proxies
		result, err := dt.client.ProxyList(dirFilter)
		if err != nil {
			return nil, nil // Non-fatal
		}

		if proxies, ok := result["proxies"].([]interface{}); ok {
			for _, p := range proxies {
				if pm, ok := p.(map[string]interface{}); ok {
					if id := getString(pm, "id"); id != "" {
						proxyIDs = append(proxyIDs, id)
					}
				}
			}
		}
	}

	if len(proxyIDs) == 0 {
		return nil, nil
	}

	var allErrors []unifiedError

	for _, pid := range proxyIDs {
		filter := protocol.LogQueryFilter{
			Types: []string{"error", "http", "diagnostic", "custom"},
			Since: since,
		}

		result, err := dt.client.ProxyLogQuery(pid, filter)
		if err != nil {
			continue // Skip this proxy on error
		}

		logs, ok := result["entries"].([]interface{})
		if !ok {
			continue
		}

		for _, entry := range logs {
			em, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}

			entryType := getString(em, "type")
			errors := convertProxyEntry(pid, entryType, em)
			allErrors = append(allErrors, errors...)
		}
	}

	return allErrors, nil
}
