package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	ID       string    `json:"id"`       // stable 8-char hex: sha256(source+category+message+location)[:4 bytes]
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
	DualBackend[DaemonTools, proxy.ProxyManager]
}

// makeGetErrorsHandler creates a handler for the get_errors tool on DaemonTools.
func (dt *DaemonTools) makeGetErrorsHandler() func(context.Context, *mcp.CallToolRequest, GetErrorsInput) (*mcp.CallToolResult, GetErrorsOutput, error) {
	backend := &getErrorsBackend{DualBackend[DaemonTools, proxy.ProxyManager]{Daemon: dt}}
	return backend.makeHandler()
}

// RegisterGetErrorsTool registers the get_errors tool.
// Exactly one of dt or pm must be non-nil.
func RegisterGetErrorsTool(server *mcp.Server, dt *DaemonTools, pm *proxy.ProxyManager) {
	backend := &getErrorsBackend{DualBackend[DaemonTools, proxy.ProxyManager]{Daemon: dt, Legacy: pm}}

	desc := `Get all current errors across proxies.

Collects errors from: browser JavaScript errors, HTTP 4xx/5xx responses,
proxy transport errors, and custom error logs.`

	if backend.Daemon == nil {
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

		type collectResult struct {
			errors  []unifiedError
			toolErr *mcp.CallToolResult
		}
		var collected collectResult
		_ = b.Dispatch(
			func(dt *DaemonTools) error {
				if err := dt.ensureConnected(); err != nil {
					collected.toolErr = errorResult(err.Error())
					return nil
				}
				collected.errors, collected.toolErr = b.collectDaemonErrors(input)
				return nil
			},
			func(_ *proxy.ProxyManager) error {
				collected.errors, collected.toolErr = b.collectLegacyErrors(input)
				return nil
			},
		)
		allErrors, toolErr := collected.errors, collected.toolErr

		if toolErr != nil {
			return toolErr, GetErrorsOutput{}, nil
		}

		result, output := formatErrorsOutput(allErrors, includeWarnings, limit, input.Raw)
		return result, output, nil
	}
}

// collectDaemonErrors collects errors via the daemon IPC path.
func (b *getErrorsBackend) collectDaemonErrors(input GetErrorsInput) ([]unifiedError, *mcp.CallToolResult) {
	dt := b.Daemon
	allErrors := make([]unifiedError, 0)

	// 1. Collect process alerts
	processErrors, procErr := dt.collectProcessAlerts(input.ProcessID, input.Since)
	if procErr != nil {
		return nil, procErr
	}
	allErrors = append(allErrors, processErrors...)

	// 2. Collect startup errors
	startupErrors, startupErr := dt.collectStartupErrors(input.ProcessID, input.Since)
	if startupErr != nil {
		return nil, startupErr
	}
	allErrors = append(allErrors, startupErrors...)

	// 3. Collect proxy errors
	proxyErrors, proxyErr := dt.collectProxyErrors(input.ProxyID, input.Since)
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
	pm := b.Legacy
	var proxies []*proxy.ProxyServer
	if input.ProxyID != "" {
		ps, err := pm.Get(input.ProxyID)
		if err != nil {
			return nil, errorResult(fmt.Sprintf("proxy %q not found: %v", input.ProxyID, err))
		}
		proxies = []*proxy.ProxyServer{ps}
	} else {
		proxies = pm.List()
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
