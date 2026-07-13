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
	Global          *bool  `json:"global,omitempty" jsonschema:"Override project config: true includes all projects, false forces current project"`
}

// GetErrorsOutput is the output for the get_errors tool.
type GetErrorsOutput struct {
	ErrorCount   int    `json:"error_count"`
	WarningCount int    `json:"warning_count"`
	Summary      string `json:"summary,omitempty"`
	// CollectionWarnings surfaces per-source query failures so a partial
	// collection is not silently reported as a clean "0 errors". Each entry
	// names the source that failed (alert store, startup log, proxy list, or a
	// specific proxy's log). Empty when every source answered.
	CollectionWarnings []string `json:"collection_warnings,omitempty" jsonschema:"Per-source query failures encountered while collecting; a non-empty list means the error view is partial"`
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
		effectiveGlobal, err := resolveEffectiveGlobal(input.Global, func() (bool, error) {
			return dt.client.ResolveQueryScope(sessionScopeFilter(dt, nil))
		})
		if err != nil {
			return errorResult("failed to resolve query scope: " + err.Error()), GetErrorsOutput{}, nil
		}
		if !effectiveGlobal {
			if incidentErrors, ok := dt.collectIncidentErrors(input, includeWarnings); ok {
				result, output := formatErrorsOutput(incidentErrors, includeWarnings, limit, input.Raw)
				return result, output, nil
			}
		}

		allErrors, collectionWarnings := dt.collectDaemonErrors(input)

		result, output := formatErrorsOutput(allErrors, includeWarnings, limit, input.Raw)
		output.CollectionWarnings = collectionWarnings
		return result, output, nil
	}
}

func resolveEffectiveGlobal(explicit *bool, resolveDefault func() (bool, error)) (bool, error) {
	if explicit != nil {
		return *explicit, nil
	}
	return resolveDefault()
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

// collectDaemonErrors collects errors via the daemon IPC path. Per-source query
// failures are non-fatal but returned as collection warnings so a partial
// collection is never silently presented as a clean "0 errors" view.
func (dt *DaemonTools) collectDaemonErrors(input GetErrorsInput) ([]unifiedError, []string) {
	allErrors := make([]unifiedError, 0)
	var warnings []string

	// 1. Collect process alerts
	processErrors, procWarn := dt.collectProcessAlerts(input.ProcessID, input.Since, input.Global)
	if procWarn != "" {
		warnings = append(warnings, procWarn)
	}
	allErrors = append(allErrors, processErrors...)

	// 2. Collect startup errors
	startupErrors, startupWarn := dt.collectStartupErrors(input.ProcessID, input.Since, input.Global)
	if startupWarn != "" {
		warnings = append(warnings, startupWarn)
	}
	allErrors = append(allErrors, startupErrors...)

	// 3. Collect proxy errors
	proxyErrors, proxyWarns := dt.collectProxyErrors(input.ProxyID, input.Since, input.Global)
	warnings = append(warnings, proxyWarns...)
	allErrors = append(allErrors, proxyErrors...)

	return allErrors, warnings
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
func (dt *DaemonTools) collectProcessAlerts(processID, since string, global *bool) ([]unifiedError, string) {
	filter := protocol.AlertQueryFilter{
		ProcessID: processID,
		Since:     since,
		Global:    global,
	}
	if !globalEnabled(global) {
		if sessionCode := dt.SessionCode(); sessionCode != "" {
			filter.SessionCode = sessionCode
		} else if p := getProjectPath(); p != "" {
			filter.Directory = p
		}
	}

	result, err := dt.client.AlertQuery(filter)
	if err != nil {
		// Non-fatal, but must be visible: a failed alert-store query would
		// otherwise be indistinguishable from "no process errors".
		return nil, "alert store query failed: " + err.Error()
	}

	alerts, ok := result["alerts"].([]interface{})
	if !ok {
		return nil, ""
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

	return errors, ""
}

// collectStartupErrors queries the daemon startup log for error-level entries.
// Scope mirrors collectProcessAlerts: global bypasses the session-scope
// chokepoint (cross-project); otherwise the query is scoped to the caller's
// session/project so other projects' startup events don't leak in.
func (dt *DaemonTools) collectStartupErrors(processID, since string, global *bool) ([]unifiedError, string) {
	dirFilter := protocol.DirectoryFilter{GlobalOverride: global}
	if !globalEnabled(global) {
		if sessionCode := dt.SessionCode(); sessionCode != "" {
			dirFilter.SessionCode = sessionCode
		} else if p := getProjectPath(); p != "" {
			dirFilter.Directory = p
		}
	}
	result, err := dt.client.StartupLog(50, dirFilter)
	if err != nil {
		// Non-fatal, but must be visible rather than reported as no errors.
		return nil, "startup log query failed: " + err.Error()
	}

	entries, ok := result["entries"].([]interface{})
	if !ok {
		return nil, ""
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

	return errors, ""
}

// collectProxyErrors lists proxies and queries their logs for errors.
// global bypasses the session-scope chokepoint (cross-project); otherwise the
// proxy list is scoped to the caller's session/project so other projects'
// proxy errors don't leak in — consistent with collectProcessAlerts /
// collectStartupErrors so a single get_errors {global:true} is uniform.
func (dt *DaemonTools) collectProxyErrors(proxyID, since string, global *bool) ([]unifiedError, []string) {
	// Build directory filter for proxy list
	dirFilter := protocol.DirectoryFilter{GlobalOverride: global}
	if !globalEnabled(global) {
		if sessionCode := dt.SessionCode(); sessionCode != "" {
			dirFilter.SessionCode = sessionCode
		} else if p := getProjectPath(); p != "" {
			dirFilter.Directory = p
		}
	}

	var warnings []string
	var proxyIDs []string

	if proxyID != "" {
		proxyIDs = []string{proxyID}
	} else {
		// List all active proxies
		result, err := dt.client.ProxyList(dirFilter)
		if err != nil {
			// Non-fatal, but visible: without the proxy list we cannot tell
			// "no proxy errors" from "could not enumerate proxies".
			return nil, []string{"proxy list query failed: " + err.Error()}
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
		return nil, warnings
	}

	var allErrors []unifiedError

	for _, pid := range proxyIDs {
		filter := protocol.LogQueryFilter{
			Types: []string{"error", "http", "diagnostic", "custom"},
			Since: since,
		}

		result, err := dt.client.ProxyLogQuery(pid, filter)
		if err != nil {
			// Tolerate a single bad proxy, but count it so the caller knows
			// this proxy's errors are missing from the view.
			warnings = append(warnings, "proxy log query failed for "+pid+": "+err.Error())
			continue
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

	return allErrors, warnings
}
