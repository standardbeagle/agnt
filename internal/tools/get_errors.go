package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GetErrorsInput is the input for the get_errors tool.
type GetErrorsInput struct {
	ProcessID       string `json:"process_id,omitempty" jsonschema:"Filter to specific process"`
	ProxyID         string `json:"proxy_id,omitempty" jsonschema:"Filter to specific proxy (default: all active proxies)"`
	Since           string `json:"since,omitempty" jsonschema:"Override recency filter (RFC3339 or duration like '5m')"`
	IncludeWarnings *bool  `json:"include_warnings,omitempty" jsonschema:"Include warnings (default: true)"`
	Limit           int    `json:"limit,omitempty" jsonschema:"Max errors to return (default: 25)"`
	Raw             bool   `json:"raw,omitempty" jsonschema:"Return full JSON with all fields"`
}

// GetErrorsOutput is the output for the get_errors tool.
type GetErrorsOutput struct {
	ErrorCount   int    `json:"error_count"`
	WarningCount int    `json:"warning_count"`
	Summary      string `json:"summary,omitempty"`
}

// unifiedError is the internal representation for deduplication and sorting.
type unifiedError struct {
	Source   string    `json:"source"`   // "process:<id>" or "browser:js" or "proxy:http" or "proxy:diagnostic"
	Severity string    `json:"severity"` // "error" or "warning"
	Category string    `json:"category"` // e.g. "TypeError", "COMPILE ERROR", "500 Internal Server Error"
	Message  string    `json:"message"`
	Location string    `json:"location,omitempty"` // file:line:col
	Page     string    `json:"page,omitempty"`     // page URL
	Count    int       `json:"count"`
	LastSeen time.Time `json:"last_seen"`
}

// deduplication key for a unified error.
func (e *unifiedError) dedupKey() string {
	return e.Source + "|" + e.Category + "|" + e.Message + "|" + e.Location
}

// getErrorsBackend holds either a daemon client or a direct proxy manager,
// enabling a single registration path for both daemon and legacy modes.
type getErrorsBackend struct {
	daemon *DaemonTools
	legacy *proxy.ProxyManager
}

// makeGetErrorsHandler creates a handler for the get_errors tool on DaemonTools.
func (dt *DaemonTools) makeGetErrorsHandler() func(context.Context, *mcp.CallToolRequest, GetErrorsInput) (*mcp.CallToolResult, GetErrorsOutput, error) {
	backend := &getErrorsBackend{daemon: dt}
	return backend.makeHandler()
}

// RegisterGetErrorsTool registers the get_errors tool.
// Exactly one of dt or pm must be non-nil.
func RegisterGetErrorsTool(server *mcp.Server, dt *DaemonTools, pm *proxy.ProxyManager) {
	backend := &getErrorsBackend{daemon: dt, legacy: pm}

	desc := `Get all current errors across proxies.

Collects errors from: browser JavaScript errors, HTTP 4xx/5xx responses,
proxy transport errors, and custom error logs.`

	if backend.daemon == nil {
		desc += `

Note: Running in legacy mode - process output alerts are not available.`
	}

	desc += `

Default behavior:
  - Deduplicates identical errors (shows count)
  - Reduces stack traces to first application code frame
  - Filters out noise (static asset 404s, redirects)
  - Sorts by severity (errors first) then recency

Examples:
  get_errors {}
  get_errors {proxy_id: "dev"}
  get_errors {since: "5m"}
  get_errors {include_warnings: false}
  get_errors {raw: true, limit: 50}`

	addLenientTool(server, &mcp.Tool{
		Name:        "get_errors",
		Description: desc,
	}, backend.makeHandler())
}

// makeHandler creates a handler that dispatches to daemon or legacy path.
func (b *getErrorsBackend) makeHandler() func(context.Context, *mcp.CallToolRequest, GetErrorsInput) (*mcp.CallToolResult, GetErrorsOutput, error) {
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

		var allErrors []unifiedError
		var toolErr *mcp.CallToolResult

		if b.daemon != nil {
			if err := b.daemon.ensureConnected(); err != nil {
				return errorResult(err.Error()), GetErrorsOutput{}, nil
			}
			allErrors, toolErr = b.collectDaemonErrors(input)
		} else {
			allErrors, toolErr = b.collectLegacyErrors(input)
		}

		if toolErr != nil {
			return toolErr, GetErrorsOutput{}, nil
		}

		result, output := formatErrorsOutput(allErrors, includeWarnings, limit, input.Raw)
		return result, output, nil
	}
}

// collectDaemonErrors collects errors via the daemon IPC path.
func (b *getErrorsBackend) collectDaemonErrors(input GetErrorsInput) ([]unifiedError, *mcp.CallToolResult) {
	allErrors := make([]unifiedError, 0)

	// 1. Collect process alerts
	processErrors, procErr := b.daemon.collectProcessAlerts(input.ProcessID, input.Since)
	if procErr != nil {
		return nil, procErr
	}
	allErrors = append(allErrors, processErrors...)

	// 2. Collect startup errors
	startupErrors, startupErr := b.daemon.collectStartupErrors(input.ProcessID, input.Since)
	if startupErr != nil {
		return nil, startupErr
	}
	allErrors = append(allErrors, startupErrors...)

	// 3. Collect proxy errors
	proxyErrors, proxyErr := b.daemon.collectProxyErrors(input.ProxyID, input.Since)
	if proxyErr != nil {
		return nil, proxyErr
	}
	allErrors = append(allErrors, proxyErrors...)

	return allErrors, nil
}

// collectLegacyErrors collects errors via direct proxy access (no daemon).
func (b *getErrorsBackend) collectLegacyErrors(input GetErrorsInput) ([]unifiedError, *mcp.CallToolResult) {
	// Build time filter
	var sinceTime *time.Time
	if input.Since != "" {
		sinceTime = parseSince(input.Since)
	}

	// Collect proxies to query
	var proxies []*proxy.ProxyServer
	if input.ProxyID != "" {
		ps, err := b.legacy.Get(input.ProxyID)
		if err != nil {
			return nil, errorResult(fmt.Sprintf("proxy %q not found: %v", input.ProxyID, err))
		}
		proxies = []*proxy.ProxyServer{ps}
	} else {
		proxies = b.legacy.List()
	}

	// Collect errors from all proxies
	var allErrors []unifiedError
	for _, ps := range proxies {
		filter := proxy.LogFilter{
			Types: []proxy.LogEntryType{
				proxy.LogTypeError,
				proxy.LogTypeHTTP,
				proxy.LogTypeDiagnostic,
				proxy.LogTypeCustom,
			},
			Since: sinceTime,
		}
		entries := ps.Logger().Query(filter)
		for _, entry := range entries {
			errs := convertProxyEntryDirect(ps.ID, entry)
			allErrors = append(allErrors, errs...)
		}
	}

	return allErrors, nil
}

// formatErrorsOutput applies deduplication, filtering, sorting, limiting, and formatting
// to a collected set of unified errors. Shared between daemon and legacy paths.
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
func (dt *DaemonTools) collectProcessAlerts(processID, since string) ([]unifiedError, *mcp.CallToolResult) {
	filter := protocol.AlertQueryFilter{
		ProcessID: processID,
		Since:     since,
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

// alertMapToUnifiedError converts a raw daemon AlertEntry JSON map into a
// unifiedError. Kept separate from collectProcessAlerts so it can be
// tested without a live daemon client.
//
// Process-lifecycle entries (category "process_lifecycle") get special
// handling: the category is rendered as "PROCESS LIFECYCLE" for the
// compact output, and the description is prepended to the message so
// agents see the exit-code summary plus the stderr tail.
func alertMapToUnifiedError(am map[string]interface{}) *unifiedError {
	severity := getString(am, "severity")
	level := "error"
	if severity == "warning" || severity == "info" {
		level = "warning"
	}

	scriptID := getString(am, "script_id")
	source := "process:" + scriptID

	rawCategory := getString(am, "category")
	category := strings.ToUpper(rawCategory)
	if category == "" {
		category = "PROCESS ERROR"
	}
	// Humanise snake_case categories. The daemon uses "process_lifecycle"
	// for exit events; compact output reads cleaner as "PROCESS LIFECYCLE".
	if strings.Contains(category, "_") {
		category = strings.ReplaceAll(category, "_", " ")
	}

	description := getString(am, "description")
	line := getString(am, "line")
	ts := getTime(am, "timestamp")

	// For lifecycle entries the description carries the exit summary
	// ("process X exited (code 1, reason crash, uptime 12m34s)") and the
	// line carries the stderr tail that explains WHY. Combine both so
	// the agent gets the full picture in one entry.
	message := line
	if rawCategory == "process_lifecycle" && description != "" {
		if message == "" || message == description {
			message = description
		} else {
			message = description + " — " + message
		}
	} else if message == "" {
		message = description
	}

	return &unifiedError{
		Source:   source,
		Severity: level,
		Category: category,
		Message:  message,
		LastSeen: ts,
		Count:    1,
	}
}

// collectStartupErrors queries the daemon startup log for error-level entries.
func (dt *DaemonTools) collectStartupErrors(processID, since string) ([]unifiedError, *mcp.CallToolResult) {
	result, err := dt.client.StartupLog(50)
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

// convertStartupLogEntry converts a startup log entry map to a unified error.
// Returns nil for non-error/non-warning entries or entries with empty messages.
func convertStartupLogEntry(em map[string]interface{}) *unifiedError {
	level := getString(em, "level")
	if level != "error" && level != "warning" {
		return nil
	}

	message := getString(em, "message")
	if message == "" {
		return nil
	}

	severity := "error"
	if level == "warning" {
		severity = "warning"
	}

	scriptName := getString(em, "script_name")
	source := "startup:" + scriptName
	if scriptName == "" {
		source = "startup"
	}

	eventType := getString(em, "event_type")
	category := "STARTUP ERROR"
	if eventType != "" {
		category = strings.ToUpper(strings.ReplaceAll(eventType, "_", " "))
	}

	ts := getTime(em, "timestamp")

	return &unifiedError{
		Source:   source,
		Severity: severity,
		Category: category,
		Message:  message,
		LastSeen: ts,
		Count:    1,
	}
}

// collectProxyErrors lists proxies and queries their logs for errors.
func (dt *DaemonTools) collectProxyErrors(proxyID, since string) ([]unifiedError, *mcp.CallToolResult) {
	// Build directory filter for proxy list
	dirFilter := protocol.DirectoryFilter{}
	if sessionCode := dt.SessionCode(); sessionCode != "" {
		dirFilter.SessionCode = sessionCode
	} else if p := getProjectPath(); p != "" {
		dirFilter.Directory = p
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

// convertProxyEntry converts a proxy log entry to zero or more unified errors.
func convertProxyEntry(proxyID, entryType string, em map[string]interface{}) []unifiedError {
	switch entryType {
	case "error":
		return convertJSError(proxyID, em)
	case "http":
		return convertHTTPError(proxyID, em)
	case "diagnostic":
		return convertDiagnosticError(proxyID, em)
	case "custom":
		return convertCustomError(proxyID, em)
	}
	return nil
}

// convertJSError converts a browser JS error log entry.
func convertJSError(proxyID string, em map[string]interface{}) []unifiedError {
	errData, ok := em["error"].(map[string]interface{})
	if !ok {
		return nil
	}

	message := getString(errData, "message")
	if message == "" {
		return nil
	}

	// Extract error type from message (e.g. "TypeError: ..." -> "TypeError")
	category := "JS Error"
	if idx := strings.Index(message, ":"); idx > 0 && idx < 30 {
		category = message[:idx]
		message = strings.TrimSpace(message[idx+1:])
	}

	location := ""
	stack := getString(errData, "stack")
	if stack != "" {
		location = extractFirstAppFrame(stack)
	}
	if location == "" {
		source := getString(errData, "source")
		lineNo := getInt(errData, "lineno")
		colNo := getInt(errData, "colno")
		if source != "" && lineNo > 0 {
			location = fmt.Sprintf("%s:%d:%d", source, lineNo, colNo)
		}
	}

	pageURL := getString(errData, "url")
	ts := getTime(errData, "timestamp")

	return []unifiedError{{
		Source:   "browser:js",
		Severity: "error",
		Category: category,
		Message:  message,
		Location: location,
		Page:     pageURL,
		LastSeen: ts,
		Count:    1,
	}}
}

// convertHTTPError converts an HTTP log entry to an error if status >= 400.
func convertHTTPError(proxyID string, em map[string]interface{}) []unifiedError {
	httpData, ok := em["http"].(map[string]interface{})
	if !ok {
		return nil
	}

	statusCode := getInt(httpData, "status_code")
	url := getString(httpData, "url")
	errField := getString(httpData, "error")

	// Skip non-error statuses (unless there's an error field)
	if statusCode < 400 && errField == "" {
		return nil
	}

	// Noise filtering
	if isNoiseError(url, statusCode) {
		return nil
	}

	method := getString(httpData, "method")
	category := categorizeHTTPError(statusCode)

	message := fmt.Sprintf("%s %s", method, url)
	if errField != "" {
		message += fmt.Sprintf(" → %q", truncate(errField, 200))
	} else {
		body := getString(httpData, "response_body")
		if body != "" {
			extracted := extractErrorMessage(body, 200)
			if extracted != "" {
				message += fmt.Sprintf(" → %q", extracted)
			}
		}
	}

	level := "error"
	if statusCode >= 400 && statusCode < 500 {
		level = "warning"
	}

	ts := getTime(httpData, "timestamp")

	return []unifiedError{{
		Source:   "proxy:http",
		Severity: level,
		Category: category,
		Message:  message,
		LastSeen: ts,
		Count:    1,
	}}
}

// convertDiagnosticError converts a proxy diagnostic entry.
func convertDiagnosticError(proxyID string, em map[string]interface{}) []unifiedError {
	diagData, ok := em["diagnostic"].(map[string]interface{})
	if !ok {
		return nil
	}

	level := getString(diagData, "level")
	if level != "error" && level != "warning" {
		return nil
	}

	message := getString(diagData, "message")
	event := getString(diagData, "event")
	ts := getTime(diagData, "timestamp")

	category := "PROXY DIAGNOSTIC"
	if event != "" {
		category = strings.ToUpper(strings.ReplaceAll(event, "_", " "))
	}

	return []unifiedError{{
		Source:   "proxy:diagnostic",
		Severity: level,
		Category: category,
		Message:  message,
		LastSeen: ts,
		Count:    1,
	}}
}

// convertCustomError converts a custom log entry with error level.
func convertCustomError(proxyID string, em map[string]interface{}) []unifiedError {
	customData, ok := em["custom"].(map[string]interface{})
	if !ok {
		return nil
	}

	level := getString(customData, "level")
	if level != "error" && level != "warn" {
		return nil
	}

	unifiedLevel := "error"
	if level == "warn" {
		unifiedLevel = "warning"
	}

	message := getString(customData, "message")
	pageURL := getString(customData, "url")
	ts := getTime(customData, "timestamp")

	return []unifiedError{{
		Source:   "browser:custom",
		Severity: unifiedLevel,
		Category: "CUSTOM ERROR",
		Message:  message,
		Page:     pageURL,
		LastSeen: ts,
		Count:    1,
	}}
}

// parseSince parses a "since" parameter as either RFC3339 or a Go duration string.
// Returns nil if the string is empty or unparseable.
func parseSince(since string) *time.Time {
	if since == "" {
		return nil
	}

	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, since); err == nil {
		return &t
	}

	// Try Go duration (e.g. "5m", "1h", "30s")
	if d, err := time.ParseDuration(since); err == nil {
		t := time.Now().Add(-d)
		return &t
	}

	return nil
}

// convertProxyEntryDirect converts a proxy.LogEntry directly to unified errors.
func convertProxyEntryDirect(proxyID string, entry proxy.LogEntry) []unifiedError {
	switch entry.Type {
	case proxy.LogTypeError:
		return convertJSErrorDirect(proxyID, entry.Error)
	case proxy.LogTypeHTTP:
		return convertHTTPErrorDirect(proxyID, entry.HTTP)
	case proxy.LogTypeDiagnostic:
		return convertDiagnosticErrorDirect(proxyID, entry.Diagnostic)
	case proxy.LogTypeCustom:
		return convertCustomErrorDirect(proxyID, entry.Custom)
	}
	return nil
}

// convertJSErrorDirect converts a FrontendError struct to a unified error.
func convertJSErrorDirect(proxyID string, fe *proxy.FrontendError) []unifiedError {
	if fe == nil || fe.Message == "" {
		return nil
	}

	message := fe.Message
	category := "JS Error"
	if idx := strings.Index(message, ":"); idx > 0 && idx < 30 {
		category = message[:idx]
		message = strings.TrimSpace(message[idx+1:])
	}

	location := ""
	if fe.Stack != "" {
		location = extractFirstAppFrame(fe.Stack)
	}
	if location == "" && fe.Source != "" && fe.LineNo > 0 {
		location = fmt.Sprintf("%s:%d:%d", fe.Source, fe.LineNo, fe.ColNo)
	}

	return []unifiedError{{
		Source:   "browser:js",
		Severity: "error",
		Category: category,
		Message:  message,
		Location: location,
		Page:     fe.URL,
		LastSeen: fe.Timestamp,
		Count:    1,
	}}
}

// convertHTTPErrorDirect converts an HTTPLogEntry struct to a unified error.
func convertHTTPErrorDirect(proxyID string, h *proxy.HTTPLogEntry) []unifiedError {
	if h == nil {
		return nil
	}

	if h.StatusCode < 400 && h.Error == "" {
		return nil
	}

	if isNoiseError(h.URL, h.StatusCode) {
		return nil
	}

	category := categorizeHTTPError(h.StatusCode)
	message := fmt.Sprintf("%s %s", h.Method, h.URL)
	if h.Error != "" {
		message += fmt.Sprintf(" → %q", truncate(h.Error, 200))
	} else if h.ResponseBody != "" {
		extracted := extractErrorMessage(h.ResponseBody, 200)
		if extracted != "" {
			message += fmt.Sprintf(" → %q", extracted)
		}
	}

	level := "error"
	if h.StatusCode >= 400 && h.StatusCode < 500 {
		level = "warning"
	}

	return []unifiedError{{
		Source:   "proxy:http",
		Severity: level,
		Category: category,
		Message:  message,
		LastSeen: h.Timestamp,
		Count:    1,
	}}
}

// convertDiagnosticErrorDirect converts a ProxyDiagnostic struct to a unified error.
func convertDiagnosticErrorDirect(proxyID string, d *proxy.ProxyDiagnostic) []unifiedError {
	if d == nil {
		return nil
	}

	level := string(d.Level)
	if level != "error" && level != "warning" {
		return nil
	}

	category := "PROXY DIAGNOSTIC"
	if d.Event != "" {
		category = strings.ToUpper(strings.ReplaceAll(d.Event, "_", " "))
	}

	return []unifiedError{{
		Source:   "proxy:diagnostic",
		Severity: level,
		Category: category,
		Message:  d.Message,
		LastSeen: d.Timestamp,
		Count:    1,
	}}
}

// convertCustomErrorDirect converts a CustomLog struct to a unified error.
func convertCustomErrorDirect(proxyID string, c *proxy.CustomLog) []unifiedError {
	if c == nil {
		return nil
	}

	if c.Level != "error" && c.Level != "warn" {
		return nil
	}

	unifiedLevel := "error"
	if c.Level == "warn" {
		unifiedLevel = "warning"
	}

	return []unifiedError{{
		Source:   "browser:custom",
		Severity: unifiedLevel,
		Category: "CUSTOM ERROR",
		Message:  c.Message,
		Page:     c.URL,
		LastSeen: c.Timestamp,
		Count:    1,
	}}
}

// deduplicateErrors merges errors with the same key, incrementing counts and keeping latest timestamp.
func deduplicateErrors(errors []unifiedError) []unifiedError {
	seen := make(map[string]int) // dedupKey -> index in result
	var result []unifiedError

	for _, e := range errors {
		key := e.dedupKey()
		if idx, ok := seen[key]; ok {
			result[idx].Count++
			if e.LastSeen.After(result[idx].LastSeen) {
				result[idx].LastSeen = e.LastSeen
			}
		} else {
			seen[key] = len(result)
			result = append(result, e)
		}
	}

	return result
}

// formatCompactErrors renders the compact text output.
func formatCompactErrors(errors []unifiedError, totalErrors, totalWarnings int) string {
	if len(errors) == 0 {
		return "No errors found."
	}

	var b strings.Builder

	// Split into errors and warnings
	var errs, warns []unifiedError
	for _, e := range errors {
		if e.Severity == "error" {
			errs = append(errs, e)
		} else {
			warns = append(warns, e)
		}
	}

	if len(errs) > 0 {
		fmt.Fprintf(&b, "=== Errors (%d) ===\n", totalErrors)
		for _, e := range errs {
			b.WriteString("\n")
			formatSingleError(&b, e)
		}
	}

	if len(warns) > 0 {
		if len(errs) > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "=== Warnings (%d) ===\n", totalWarnings)
		for _, e := range warns {
			b.WriteString("\n")
			formatSingleError(&b, e)
		}
	}

	return b.String()
}

// formatSingleError writes one error entry in compact format.
func formatSingleError(b *strings.Builder, e unifiedError) {
	ago := formatTimeAgo(e.LastSeen)
	countStr := ""
	if e.Count > 1 {
		countStr = fmt.Sprintf("%dx, latest ", e.Count)
	} else {
		countStr = "1x, "
	}

	fmt.Fprintf(b, "[%s] %s (%s%s)\n", e.Source, e.Category, countStr, ago)
	fmt.Fprintf(b, "  %s\n", truncate(e.Message, 200))

	if e.Location != "" {
		fmt.Fprintf(b, "  → %s\n", e.Location)
	}
	if e.Page != "" {
		fmt.Fprintf(b, "  page: %s\n", truncate(e.Page, 120))
	}
}

// extractFirstAppFrame parses a JS/Go/Python stack trace and returns the first app-code frame.
// Skips node_modules, internal frames, and Go runtime frames.
func extractFirstAppFrame(stack string) string {
	lines := strings.Split(stack, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip common framework/internal frames
		if strings.Contains(line, "node_modules/") ||
			strings.Contains(line, "node:internal/") ||
			strings.Contains(line, "<anonymous>") ||
			strings.Contains(line, "webpack/") ||
			strings.Contains(line, "webpack-internal") {
			continue
		}

		// JS stack trace: "at functionName (file:line:col)" or "at file:line:col"
		if strings.HasPrefix(line, "at ") {
			// Extract file:line:col from parentheses
			if lparen := strings.LastIndex(line, "("); lparen != -1 {
				rparen := strings.LastIndex(line, ")")
				if rparen > lparen {
					return line[lparen+1 : rparen]
				}
			}
			// No parentheses: "at file:line:col"
			return strings.TrimPrefix(line, "at ")
		}

		// Go stack trace: "    /path/to/file.go:line +0xNN"
		if strings.HasSuffix(line, ".go") || strings.Contains(line, ".go:") {
			// Skip Go runtime frames
			if strings.Contains(line, "runtime/") || strings.Contains(line, "runtime.") {
				continue
			}
			// Strip offset
			if plusIdx := strings.LastIndex(line, " +0x"); plusIdx > 0 {
				return line[:plusIdx]
			}
			return line
		}

		// Python: "  File \"path\", line N, in func"
		if strings.HasPrefix(line, "File ") || strings.Contains(line, "File \"") {
			// Extract path and line number
			line = strings.ReplaceAll(line, "\"", "")
			line = strings.TrimPrefix(line, "File ")
			parts := strings.Split(line, ", ")
			if len(parts) >= 2 {
				path := strings.TrimSpace(parts[0])
				lineNum := strings.TrimPrefix(strings.TrimSpace(parts[1]), "line ")
				return path + ":" + lineNum
			}
		}
	}

	return ""
}

// extractErrorMessage extracts the error message from a response body.
// Tries JSON fields (message, error, detail), strips HTML tags, or returns plain text.
func extractErrorMessage(body string, maxLen int) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}

	// Try JSON
	if body[0] == '{' {
		var obj map[string]interface{}
		if json.Unmarshal([]byte(body), &obj) == nil {
			for _, key := range []string{"message", "error", "detail", "error_description"} {
				if v, ok := obj[key]; ok {
					switch val := v.(type) {
					case string:
						return truncate(val, maxLen)
					case map[string]interface{}:
						if msg, ok := val["message"].(string); ok {
							return truncate(msg, maxLen)
						}
					}
				}
			}
		}
	}

	// Strip HTML tags if it looks like HTML
	if strings.Contains(body, "<") && strings.Contains(body, ">") {
		stripped := stripHTMLTags(body)
		stripped = strings.Join(strings.Fields(stripped), " ")
		return truncate(stripped, maxLen)
	}

	return truncate(body, maxLen)
}

// stripHTMLTags removes HTML tags from a string.
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			b.WriteByte(' ')
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isNoiseError returns true for HTTP errors that should be filtered out.
func isNoiseError(url string, statusCode int) bool {
	// Skip redirects and not-modified
	if statusCode == 301 || statusCode == 302 || statusCode == 304 {
		return true
	}

	// Skip 404s for common noise files
	if statusCode == 404 {
		lower := strings.ToLower(url)
		noisePatterns := []string{
			".map",
			"favicon",
			".hot-update.",
			"__webpack_hmr",
			"sockjs-node",
			"ws://",
			"wss://",
		}
		for _, p := range noisePatterns {
			if strings.Contains(lower, p) {
				return true
			}
		}
	}

	return false
}

// formatTimeAgo formats a duration since a timestamp as a human-readable string.
func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

// categorizeHTTPError returns a human-readable category for an HTTP status code.
func categorizeHTTPError(statusCode int) string {
	text := http.StatusText(statusCode)
	if text == "" {
		return fmt.Sprintf("%d Error", statusCode)
	}
	return fmt.Sprintf("%d %s", statusCode, text)
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
