package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/standardbeagle/go-sdk/mcp"
)

// ProxyLogInput defines input for the proxylog tool.
type ProxyLogInput struct {
	ProxyID          string   `json:"proxy_id" jsonschema:"Proxy ID to query logs from"`
	Action           string   `json:"action,omitempty" jsonschema:"Action: query (default), summary, clear, stats"`
	Types            []string `json:"types,omitempty" jsonschema:"Log types to filter: http, error, performance, custom, screenshot, execution, response, interaction, mutation, diagnostic"`
	Methods          []string `json:"methods,omitempty" jsonschema:"HTTP methods to filter (e.g., GET, POST)"`
	URLPattern       string   `json:"url_pattern,omitempty" jsonschema:"URL pattern to match (substring)"`
	StatusCodes      []int    `json:"status_codes,omitempty" jsonschema:"HTTP status codes to filter"`
	Limit            int      `json:"limit,omitempty" jsonschema:"Maximum number of entries (default: 100)"`
	Since            string   `json:"since,omitempty" jsonschema:"Start time filter (RFC3339 or duration like '5m')"`
	Until            string   `json:"until,omitempty" jsonschema:"End time filter (RFC3339)"`
	Raw              bool     `json:"raw,omitempty" jsonschema:"Return full JSON dumps instead of compact format"`
	ErrorsOnly       bool     `json:"errors_only,omitempty" jsonschema:"Filter to errors only (HTTP 4xx/5xx, JS errors, diagnostics)"`
	DiagnosticLevels []string `json:"diagnostic_levels,omitempty" jsonschema:"Diagnostic levels to include: error, warning, info"`
	Detail           []string `json:"detail,omitempty" jsonschema:"For summary: sections to include full detail for (errors, http, performance, interactions, mutations, other)"`
}

func (input ProxyLogInput) hasFilters() bool {
	return len(input.Types) > 0 || len(input.Methods) > 0 || input.URLPattern != "" ||
		len(input.StatusCodes) > 0 || input.Since != "" || input.Until != "" ||
		input.ErrorsOnly || len(input.DiagnosticLevels) > 0
}

// ProxyLogOutput defines output for proxylog tool.
type ProxyLogOutput struct {
	// For query
	Entries    []LogEntryOutput `json:"entries,omitempty"`
	Pagination *Pagination      `json:"pagination,omitempty"`

	// For summary
	Summary *ProxyLogSummary `json:"summary,omitempty"`

	// For stats
	Stats *LogStatsOutput `json:"stats,omitempty"`

	// For clear
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// LogEntryOutput represents a log entry in the output.
type LogEntryOutput struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      string    `json:"data"`
}

// ProxyLogSummary provides a compact summary of proxy logs.
type ProxyLogSummary struct {
	TotalEntries  int            `json:"total_entries"`
	EntriesByType map[string]int `json:"entries_by_type"` // e.g., {"error": 150, "http": 300}
	TimeRange     TimeRange      `json:"time_range,omitempty"`

	// Error summary
	ErrorCount   int            `json:"error_count"`
	UniqueErrors []ErrorSummary `json:"unique_errors,omitempty"`  // Top 10 deduplicated errors
	ErrorsByType map[string]int `json:"errors_by_type,omitempty"` // e.g., {"ReferenceError": 3}
	Errors       []CompactError `json:"errors,omitempty"`         // Full list when detail includes "errors"
	RecentErrors []CompactError `json:"recent_errors,omitempty"`  // Last 5 errors (when detail not specified)

	// HTTP summary
	HTTPCount    int                  `json:"http_count"`
	HTTPByStatus map[string]int       `json:"http_by_status,omitempty"` // e.g., {"2xx": 100, "4xx": 5}
	HTTPByMethod map[string]int       `json:"http_by_method,omitempty"` // e.g., {"GET": 80, "POST": 20}
	HTTPRequests []CompactHTTPRequest `json:"http_requests,omitempty"`  // Full list when detail includes "http"
	RecentHTTP   []CompactHTTPRequest `json:"recent_http,omitempty"`    // Last 5 requests (when detail not specified)

	// Performance summary
	PerformanceCount  int                  `json:"performance_count"`
	AvgLoadTime       int64                `json:"avg_load_time_ms,omitempty"`
	Performance       []CompactPerformance `json:"performance,omitempty"`        // Full list when detail includes "performance"
	RecentPerformance []CompactPerformance `json:"recent_performance,omitempty"` // Last 5 (when detail not specified)

	// Interaction summary
	InteractionCount   int                  `json:"interaction_count"`
	InteractionsByType map[string]int       `json:"interactions_by_type,omitempty"` // e.g., {"click": 50, "scroll": 100}
	Interactions       []CompactInteraction `json:"interactions,omitempty"`         // Full list when detail includes "interactions"
	RecentInteractions []CompactInteraction `json:"recent_interactions,omitempty"`  // Last 5 (when detail not specified)

	// Mutation summary
	MutationCount   int               `json:"mutation_count"`
	MutationsByType map[string]int    `json:"mutations_by_type,omitempty"` // e.g., {"added": 10, "modified": 5}
	Mutations       []CompactMutation `json:"mutations,omitempty"`         // Full list when detail includes "mutations"
	RecentMutations []CompactMutation `json:"recent_mutations,omitempty"`  // Last 5 (when detail not specified)

	// Other log types (custom, panel_message, sketch, etc.)
	OtherCount int               `json:"other_count,omitempty"`
	OtherTypes map[string]int    `json:"other_types,omitempty"` // Counts for custom, panel_message, sketch, etc.
	Other      []CompactLogEntry `json:"other,omitempty"`       // Full list when detail includes "other"

	// Detail info
	DetailSections []string `json:"detail_sections,omitempty"` // Which sections have full detail
	DetailLimit    int      `json:"detail_limit,omitempty"`    // Limit applied to detailed sections
}

// TimeRange represents a time range for logs.
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// CompactHTTPRequest represents a compact HTTP request/response.
type CompactHTTPRequest struct {
	Method     string    `json:"method"`
	URL        string    `json:"url"`
	StatusCode int       `json:"status_code"`
	Duration   int64     `json:"duration_ms"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// CompactPerformance represents compact performance metrics.
type CompactPerformance struct {
	URL              string    `json:"url"`
	LoadTimeMs       int64     `json:"load_time_ms"`
	FirstPaintMs     int64     `json:"first_paint_ms,omitempty"`
	DOMContentLoaded int64     `json:"dom_content_loaded_ms,omitempty"`
	Timestamp        time.Time `json:"timestamp,omitempty"`
}

// CompactInteraction represents a compact user interaction.
type CompactInteraction struct {
	Type      string    `json:"type"`
	Target    string    `json:"target,omitempty"` // CSS selector or element description
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// CompactMutation represents a compact DOM mutation.
type CompactMutation struct {
	Type      string    `json:"type"` // added, removed, modified
	Target    string    `json:"target,omitempty"`
	Count     int       `json:"count,omitempty"` // Number of nodes affected
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// CompactLogEntry represents a compact log entry for other types.
type CompactLogEntry struct {
	Type      string    `json:"type"`
	Message   string    `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// ErrorSummary represents a deduplicated error with occurrence count.
type ErrorSummary struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Count   int    `json:"count"`
}

// CompactError represents a frontend error with truncated verbose fields.
// Used when detail: ["errors"] is specified to avoid token overflow.
type CompactError struct {
	Message      string `json:"message"`
	Type         string `json:"type,omitempty"`
	URL          string `json:"url,omitempty"`
	Location     string `json:"location,omitempty"`      // "file.js:123:45" format
	StackPreview string `json:"stack_preview,omitempty"` // First 3 lines of stack trace
	Timestamp    string `json:"timestamp,omitempty"`
}

func makeProxyLogHandler(pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
		if err := validateProxyLogInput(input); err != nil {
			return errorResult(validationError("proxylog", err)), ProxyLogOutput{}, nil
		}

		if input.ProxyID == "" {
			return errorResult("proxy_id required"), ProxyLogOutput{}, nil
		}

		proxyServer, err := pm.Get(input.ProxyID)
		if err != nil {
			return errorResult(fmt.Sprintf("proxy not found: %s", input.ProxyID)), ProxyLogOutput{}, nil
		}

		action := input.Action
		if action == "" {
			action = "query"
		}

		switch action {
		case "query":
			return handleProxyLogQuery(proxyServer, input)
		case "summary":
			return handleProxyLogSummary(proxyServer, input)
		case "clear":
			return handleProxyLogClear(proxyServer, input)
		case "stats":
			return handleProxyLogStats(proxyServer, input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: query, summary, clear, stats", action)), ProxyLogOutput{}, nil
		}
	}
}

func handleProxyLogQuery(proxyServer *proxy.ProxyServer, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// Build filter
	filter := proxy.LogFilter{
		Methods:          input.Methods,
		URLPattern:       input.URLPattern,
		StatusCodes:      input.StatusCodes,
		Limit:            input.Limit,
		ErrorsOnly:       input.ErrorsOnly,
		DiagnosticLevels: input.DiagnosticLevels,
	}

	// Parse types
	if len(input.Types) > 0 {
		for _, t := range input.Types {
			filter.Types = append(filter.Types, proxy.LogEntryType(t))
		}
	}

	// Parse time range
	if input.Since != "" {
		since, err := parseTimeOrDuration(input.Since)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid since: %v", err)), ProxyLogOutput{}, nil
		}
		filter.Since = &since
	}

	if input.Until != "" {
		until, err := parseTime(input.Until)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid until: %v", err)), ProxyLogOutput{}, nil
		}
		filter.Until = &until
	}

	// Default limit
	if filter.Limit == 0 {
		filter.Limit = 100
	}

	// Query logs
	entries := proxyServer.Logger().Query(filter)

	// Apply limit
	if filter.Limit > 0 && len(entries) > filter.Limit {
		entries = entries[:filter.Limit]
	}

	// Build pagination context
	stats := proxyServer.Logger().Stats()
	pag := NewPagination(len(entries), int(stats.AvailableEntries), filter.Limit, input.hasFilters())

	// Use raw format (JSON dumps) if requested
	if input.Raw {
		return handleProxyLogQueryRaw(entries, &pag)
	}

	// Default: compact format
	return handleProxyLogQueryCompact(entries, &pag)
}

// handleProxyLogQueryRaw returns full JSON dumps of log entries
func handleProxyLogQueryRaw(entries []proxy.LogEntry, pag *Pagination) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// Helper to marshal data to JSON string
	marshalData := func(data map[string]interface{}) string {
		b, err := json.Marshal(data)
		if err != nil {
			return "{}"
		}
		return string(b)
	}

	// Convert to output format
	output := make([]LogEntryOutput, len(entries))
	for i, entry := range entries {
		data := make(map[string]interface{})

		switch entry.Type {
		case proxy.LogTypeHTTP:
			if entry.HTTP != nil {
				data["id"] = entry.HTTP.ID
				data["method"] = entry.HTTP.Method
				data["url"] = entry.HTTP.URL
				data["status_code"] = entry.HTTP.StatusCode
				data["duration_ms"] = entry.HTTP.Duration.Milliseconds()
				if entry.HTTP.Error != "" {
					data["error"] = entry.HTTP.Error
				}
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.HTTP.Timestamp,
				Data:      marshalData(data),
			}

		case proxy.LogTypeError:
			if entry.Error != nil {
				data["id"] = entry.Error.ID
				data["message"] = entry.Error.Message
				data["source"] = entry.Error.Source
				data["lineno"] = entry.Error.LineNo
				data["colno"] = entry.Error.ColNo
				data["url"] = entry.Error.URL
				if entry.Error.Stack != "" {
					data["stack"] = entry.Error.Stack
				}
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.Error.Timestamp,
				Data:      marshalData(data),
			}

		case proxy.LogTypePerformance:
			if entry.Performance != nil {
				data["id"] = entry.Performance.ID
				data["url"] = entry.Performance.URL
				data["load_time_ms"] = entry.Performance.LoadEventEnd
				data["dom_content_loaded_ms"] = entry.Performance.DOMContentLoaded
				if entry.Performance.FirstPaint > 0 {
					data["first_paint_ms"] = entry.Performance.FirstPaint
				}
				if entry.Performance.FirstContentfulPaint > 0 {
					data["first_contentful_paint_ms"] = entry.Performance.FirstContentfulPaint
				}
				if len(entry.Performance.Resources) > 0 {
					data["resource_count"] = len(entry.Performance.Resources)
				}
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.Performance.Timestamp,
				Data:      marshalData(data),
			}

		case proxy.LogTypeCustom:
			if entry.Custom != nil {
				data["id"] = entry.Custom.ID
				data["level"] = entry.Custom.Level
				data["message"] = entry.Custom.Message
				data["url"] = entry.Custom.URL
				for k, v := range entry.Custom.Data {
					data[k] = v
				}
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.Custom.Timestamp,
				Data:      marshalData(data),
			}

		case proxy.LogTypeScreenshot:
			if entry.Screenshot != nil {
				data["id"] = entry.Screenshot.ID
				data["name"] = entry.Screenshot.Name
				data["file_path"] = entry.Screenshot.FilePath
				data["url"] = entry.Screenshot.URL
				data["width"] = entry.Screenshot.Width
				data["height"] = entry.Screenshot.Height
				data["format"] = entry.Screenshot.Format
				data["selector"] = entry.Screenshot.Selector
				if entry.Screenshot.Error != "" {
					data["error"] = entry.Screenshot.Error
				}
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.Screenshot.Timestamp,
				Data:      marshalData(data),
			}

		case proxy.LogTypeExecution:
			if entry.Execution != nil {
				data["id"] = entry.Execution.ID
				data["code"] = entry.Execution.Code
				data["result"] = entry.Execution.Result
				data["error"] = entry.Execution.Error
				data["duration_ms"] = entry.Execution.Duration.Milliseconds()
				data["url"] = entry.Execution.URL
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.Execution.Timestamp,
				Data:      marshalData(data),
			}

		case proxy.LogTypeResponse:
			if entry.Response != nil {
				data["id"] = entry.Response.ID
				data["exec_id"] = entry.Response.ExecID
				data["success"] = entry.Response.Success
				data["result"] = entry.Response.Result
				data["error"] = entry.Response.Error
				data["duration_ms"] = entry.Response.Duration.Milliseconds()
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.Response.Timestamp,
				Data:      marshalData(data),
			}

		case proxy.LogTypeDiagnostic:
			if entry.Diagnostic != nil {
				data["level"] = string(entry.Diagnostic.Level)
				data["category"] = entry.Diagnostic.Category
				data["event"] = entry.Diagnostic.Event
				data["message"] = entry.Diagnostic.Message
				if entry.Diagnostic.RequestID != "" {
					data["request_id"] = entry.Diagnostic.RequestID
				}
				if entry.Diagnostic.Method != "" {
					data["method"] = entry.Diagnostic.Method
				}
				if entry.Diagnostic.URL != "" {
					data["url"] = entry.Diagnostic.URL
				}
				if entry.Diagnostic.Target != "" {
					data["target"] = entry.Diagnostic.Target
				}
				if len(entry.Diagnostic.Data) > 0 {
					for k, v := range entry.Diagnostic.Data {
						data[k] = v
					}
				}
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.Diagnostic.Timestamp,
					Data:      marshalData(data),
				}
			}

		case proxy.LogTypePanelMessage:
			if entry.PanelMessage != nil {
				data["id"] = entry.PanelMessage.ID
				data["message"] = entry.PanelMessage.Message
				data["url"] = entry.PanelMessage.URL
				data["request_notification"] = entry.PanelMessage.RequestNotification
				if len(entry.PanelMessage.Attachments) > 0 {
					attachments := make([]map[string]interface{}, len(entry.PanelMessage.Attachments))
					for j, att := range entry.PanelMessage.Attachments {
						a := map[string]interface{}{
							"type": att.Type,
						}
						if att.Selector != "" {
							a["selector"] = att.Selector
						}
						if att.Tag != "" {
							a["tag"] = att.Tag
						}
						if att.ID != "" {
							a["id"] = att.ID
						}
						if att.Text != "" {
							a["text"] = att.Text
						}
						if att.Summary != "" {
							a["summary"] = att.Summary
						}
						if att.FilePath != "" {
							a["file_path"] = att.FilePath
						}
						if len(att.Classes) > 0 {
							a["classes"] = att.Classes
						}
						if att.Area != nil {
							a["area"] = map[string]interface{}{
								"x": att.Area.X, "y": att.Area.Y,
								"width": att.Area.Width, "height": att.Area.Height,
							}
						}
						if len(att.Data) > 0 {
							a["data"] = att.Data
						}
						attachments[j] = a
					}
					data["attachments"] = attachments
				}
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.PanelMessage.Timestamp,
					Data:      marshalData(data),
				}
			}
		default:
			// Fallback: serialize entire entry to JSON
			if b, err := json.Marshal(entry); err == nil {
				output[i] = LogEntryOutput{
					Type: string(entry.Type),
					Data: string(b),
				}
				if em := make(map[string]interface{}); json.Unmarshal(b, &em) == nil {
					if ts, ok := em["timestamp"].(string); ok {
						if t, err := time.Parse(time.RFC3339, ts); err == nil {
							output[i].Timestamp = t
						}
					}
					if output[i].Timestamp.IsZero() {
						output[i].Timestamp = time.Now()
					}
				}
			}
		}
	}

	return nil, ProxyLogOutput{
		Entries:    output,
		Pagination: pag,
	}, nil
}

// handleProxyLogQueryCompact returns compact semi-structured format (default)
func handleProxyLogQueryCompact(entries []proxy.LogEntry, pag *Pagination) (*mcp.CallToolResult, ProxyLogOutput, error) {
	output := make([]LogEntryOutput, len(entries))

	for i, entry := range entries {
		var timestamp time.Time
		var data string

		switch entry.Type {
		case proxy.LogTypeHTTP:
			if entry.HTTP != nil {
				timestamp = entry.HTTP.Timestamp
				errorSuffix := ""
				if entry.HTTP.Error != "" {
					errorSuffix = fmt.Sprintf(" ERROR: %s", entry.HTTP.Error)
				}
				data = fmt.Sprintf("%s %s → %d (%dms)%s",
					entry.HTTP.Method,
					entry.HTTP.URL,
					entry.HTTP.StatusCode,
					entry.HTTP.Duration.Milliseconds(),
					errorSuffix)
			}

		case proxy.LogTypeError:
			if entry.Error != nil {
				timestamp = entry.Error.Timestamp
				location := formatLocation(entry.Error.Source, entry.Error.LineNo, entry.Error.ColNo)
				stackPreview := ""
				if entry.Error.Stack != "" {
					stackPreview = "\n  " + truncateStack(entry.Error.Stack, 2)
				}
				data = fmt.Sprintf("%s\n  at %s%s",
					entry.Error.Message,
					location,
					stackPreview)
			}

		case proxy.LogTypePerformance:
			if entry.Performance != nil {
				timestamp = entry.Performance.Timestamp
				data = fmt.Sprintf("%s - Load: %dms, DOMContentLoaded: %dms, FP: %dms",
					entry.Performance.URL,
					entry.Performance.LoadEventEnd,
					entry.Performance.DOMContentLoaded,
					entry.Performance.FirstPaint)
			}

		case proxy.LogTypeCustom:
			if entry.Custom != nil {
				timestamp = entry.Custom.Timestamp
				dataStr := ""
				if len(entry.Custom.Data) > 0 {
					dataBytes, _ := json.Marshal(entry.Custom.Data)
					dataStr = fmt.Sprintf(" %s", string(dataBytes))
				}
				data = fmt.Sprintf("[%s] %s%s", entry.Custom.Level, entry.Custom.Message, dataStr)
			}

		case proxy.LogTypeInteraction:
			if entry.Interaction != nil {
				timestamp = entry.Interaction.Timestamp
				target := entry.Interaction.Target.Selector
				if target == "" {
					target = entry.Interaction.Target.Tag
				}
				data = fmt.Sprintf("%s on %s", entry.Interaction.EventType, target)
			}

		case proxy.LogTypeMutation:
			if entry.Mutation != nil {
				timestamp = entry.Mutation.Timestamp
				nodeCount := len(entry.Mutation.Added) + len(entry.Mutation.Removed)
				data = fmt.Sprintf("%s (%d nodes) at %s",
					entry.Mutation.MutationType,
					nodeCount,
					entry.Mutation.Target.Selector)
			}

		case proxy.LogTypeScreenshot:
			if entry.Screenshot != nil {
				timestamp = entry.Screenshot.Timestamp
				data = fmt.Sprintf("%s (%dx%d) → %s",
					entry.Screenshot.Name,
					entry.Screenshot.Width,
					entry.Screenshot.Height,
					entry.Screenshot.FilePath)
			}

		case proxy.LogTypeExecution:
			if entry.Execution != nil {
				timestamp = entry.Execution.Timestamp
				result := entry.Execution.Result
				if len(result) > 100 {
					result = result[:97] + "..."
				}
				errorSuffix := ""
				if entry.Execution.Error != "" {
					errorSuffix = fmt.Sprintf(" ERROR: %s", entry.Execution.Error)
				}
				data = fmt.Sprintf("Executed in %dms%s\n  Result: %s",
					entry.Execution.Duration.Milliseconds(),
					errorSuffix,
					result)
			}

		case proxy.LogTypeResponse:
			if entry.Response != nil {
				timestamp = entry.Response.Timestamp
				status := "success"
				if !entry.Response.Success {
					status = "failed"
				}
				data = fmt.Sprintf("Response [%s] (%dms) exec_id=%s",
					status,
					entry.Response.Duration.Milliseconds(),
					entry.Response.ExecID)
			}

		case proxy.LogTypePanelMessage:
			if entry.PanelMessage != nil {
				timestamp = entry.PanelMessage.Timestamp
				parts := []string{entry.PanelMessage.Message}
				if len(entry.PanelMessage.Attachments) > 0 {
					attParts := make([]string, len(entry.PanelMessage.Attachments))
					for j, att := range entry.PanelMessage.Attachments {
						desc := att.Type
						if att.Selector != "" {
							desc += ":" + att.Selector
						}
						if att.Summary != "" {
							desc += " (" + att.Summary + ")"
						}
						if att.FilePath != "" {
							desc += " → " + att.FilePath
						}
						attParts[j] = desc
					}
					parts = append(parts, fmt.Sprintf("[%d attachments: %s]",
						len(entry.PanelMessage.Attachments), strings.Join(attParts, ", ")))
				}
				if entry.PanelMessage.URL != "" {
					parts = append(parts, "page: "+entry.PanelMessage.URL)
				}
				data = strings.Join(parts, "\n  ")
			}

		case proxy.LogTypeSketch:
			if entry.Sketch != nil {
				timestamp = entry.Sketch.Timestamp
				data = fmt.Sprintf("%s (%d elements) → %s",
					entry.Sketch.Description,
					entry.Sketch.ElementCount,
					entry.Sketch.FilePath)
			}

		case proxy.LogTypeDiagnostic:
			if entry.Diagnostic != nil {
				timestamp = entry.Diagnostic.Timestamp
				levelIcon := ""
				switch entry.Diagnostic.Level {
				case proxy.DiagnosticError:
					levelIcon = "[ERROR]"
				case proxy.DiagnosticWarning:
					levelIcon = "[WARN]"
				case proxy.DiagnosticInfo:
					levelIcon = "[INFO]"
				}
				data = fmt.Sprintf("%s %s:%s - %s",
					levelIcon,
					entry.Diagnostic.Category,
					entry.Diagnostic.Event,
					entry.Diagnostic.Message)
				if entry.Diagnostic.URL != "" {
					data += fmt.Sprintf("\n  URL: %s", entry.Diagnostic.URL)
				}
				if entry.Diagnostic.Target != "" {
					data += fmt.Sprintf("\n  Target: %s", entry.Diagnostic.Target)
				}
			}

		case proxy.LogTypeScreenshotCapture:
			if entry.ScreenshotCapture != nil {
				timestamp = entry.ScreenshotCapture.Timestamp
				data = fmt.Sprintf("Screenshot: %s", entry.ScreenshotCapture.Summary)
			}
		case proxy.LogTypeElementCapture:
			if entry.ElementCapture != nil {
				timestamp = entry.ElementCapture.Timestamp
				data = fmt.Sprintf("Element: %s %s", entry.ElementCapture.Tag, entry.ElementCapture.Selector)
			}
		case proxy.LogTypeSketchCapture:
			if entry.SketchCapture != nil {
				timestamp = entry.SketchCapture.Timestamp
				data = fmt.Sprintf("Sketch: %s", entry.SketchCapture.Summary)
			}
		case proxy.LogTypeDesignState:
			if entry.DesignState != nil {
				timestamp = entry.DesignState.Timestamp
				data = fmt.Sprintf("Design selected: %s", entry.DesignState.Selector)
				if entry.DesignState.Metadata.Tag != "" {
					data += fmt.Sprintf(" (%s)", entry.DesignState.Metadata.Tag)
				}
			}
		case proxy.LogTypeDesignRequest:
			if entry.DesignRequest != nil {
				timestamp = entry.DesignRequest.Timestamp
				data = fmt.Sprintf("Design request: %s with %d existing alternatives", entry.DesignRequest.Selector, entry.DesignRequest.AlternativesCount)
			}
		case proxy.LogTypeDesignChat:
			if entry.DesignChat != nil {
				timestamp = entry.DesignChat.Timestamp
				data = fmt.Sprintf("Design chat: %s", entry.DesignChat.Message)
			}
		default:
			// For other types, use JSON serialization
			if b, err := json.Marshal(entry); err == nil {
				data = string(b)
				if em := make(map[string]interface{}); json.Unmarshal(b, &em) == nil {
					if sub, ok := em[string(entry.Type)].(map[string]interface{}); ok {
						if ts, ok := sub["timestamp"].(string); ok {
							if t, err := time.Parse(time.RFC3339, ts); err == nil {
								timestamp = t
							}
						}
					}
				}
			}
			if timestamp.IsZero() {
				timestamp = time.Now()
			}
			if data == "" {
				data = fmt.Sprintf("%s event", entry.Type)
			}
			data = fmt.Sprintf("%s event", entry.Type)
		}

		output[i] = LogEntryOutput{
			Type:      string(entry.Type),
			Timestamp: timestamp,
			Data:      data,
		}
	}

	return nil, ProxyLogOutput{
		Entries:    output,
		Pagination: pag,
	}, nil
}

func handleProxyLogClear(proxyServer *proxy.ProxyServer, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	proxyServer.Logger().Clear()

	return nil, ProxyLogOutput{
		Success: true,
		Message: fmt.Sprintf("Logs cleared for proxy %s", input.ProxyID),
	}, nil
}

func handleProxyLogStats(proxyServer *proxy.ProxyServer, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	stats := proxyServer.Logger().Stats()

	return nil, ProxyLogOutput{
		Stats: &LogStatsOutput{
			TotalEntries:     stats.TotalEntries,
			AvailableEntries: stats.AvailableEntries,
			MaxSize:          stats.MaxSize,
			Dropped:          stats.Dropped,
		},
	}, nil
}

func handleProxyLogSummary(proxyServer *proxy.ProxyServer, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// Query all logs
	allEntries := proxyServer.Logger().Query(proxy.LogFilter{})

	// Set default limit
	limit := input.Limit
	if limit == 0 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}

	// Build detail sections map
	detailSections := make(map[string]bool)
	for _, section := range input.Detail {
		detailSections[section] = true
	}

	summary := &ProxyLogSummary{
		TotalEntries:       len(allEntries),
		EntriesByType:      make(map[string]int),
		HTTPByStatus:       make(map[string]int),
		HTTPByMethod:       make(map[string]int),
		InteractionsByType: make(map[string]int),
		MutationsByType:    make(map[string]int),
		ErrorsByType:       make(map[string]int),
		OtherTypes:         make(map[string]int),
		DetailSections:     input.Detail,
		DetailLimit:        limit,
	}

	// Track time range
	var minTime, maxTime time.Time

	// Temporary slices for collecting entries
	var httpEntries []proxy.HTTPLogEntry
	var errorEntries []proxy.FrontendError
	var perfEntries []proxy.PerformanceMetric
	var interactionEntries []proxy.InteractionEvent
	var mutationEntries []proxy.MutationEvent
	var otherEntries []proxy.LogEntry

	// First pass: count and collect
	for _, entry := range allEntries {
		summary.EntriesByType[string(entry.Type)]++

		// Track time range
		var timestamp time.Time
		switch entry.Type {
		case proxy.LogTypeHTTP:
			if entry.HTTP != nil {
				timestamp = entry.HTTP.Timestamp
				httpEntries = append(httpEntries, *entry.HTTP)
				summary.HTTPCount++

				// Count by method
				summary.HTTPByMethod[entry.HTTP.Method]++

				// Count by status range
				statusCode := entry.HTTP.StatusCode
				if statusCode >= 200 && statusCode < 300 {
					summary.HTTPByStatus["2xx"]++
				} else if statusCode >= 300 && statusCode < 400 {
					summary.HTTPByStatus["3xx"]++
				} else if statusCode >= 400 && statusCode < 500 {
					summary.HTTPByStatus["4xx"]++
				} else if statusCode >= 500 {
					summary.HTTPByStatus["5xx"]++
				}
			}

		case proxy.LogTypeError:
			if entry.Error != nil {
				timestamp = entry.Error.Timestamp
				errorEntries = append(errorEntries, *entry.Error)
				summary.ErrorCount++

				// Extract error type from message (e.g., "ReferenceError: ...")
				errorType := "Error"
				if len(entry.Error.Message) > 0 {
					if idx := len(entry.Error.Message); idx > 0 {
						// Try to extract error type from message prefix
						parts := splitFirst(entry.Error.Message, ":")
						if len(parts) > 1 {
							errorType = parts[0]
						}
					}
				}
				summary.ErrorsByType[errorType]++
			}

		case proxy.LogTypePerformance:
			if entry.Performance != nil {
				timestamp = entry.Performance.Timestamp
				perfEntries = append(perfEntries, *entry.Performance)
				summary.PerformanceCount++
			}

		case proxy.LogTypeInteraction:
			if entry.Interaction != nil {
				timestamp = entry.Interaction.Timestamp
				interactionEntries = append(interactionEntries, *entry.Interaction)
				summary.InteractionCount++
				summary.InteractionsByType[entry.Interaction.EventType]++
			}

		case proxy.LogTypeMutation:
			if entry.Mutation != nil {
				timestamp = entry.Mutation.Timestamp
				mutationEntries = append(mutationEntries, *entry.Mutation)
				summary.MutationCount++
				summary.MutationsByType[entry.Mutation.MutationType]++
			}

		default:
			otherEntries = append(otherEntries, entry)
			summary.OtherCount++
			summary.OtherTypes[string(entry.Type)]++

			// Get timestamp for "other" types
			switch entry.Type {
			case proxy.LogTypeCustom:
				if entry.Custom != nil {
					timestamp = entry.Custom.Timestamp
				}
			case proxy.LogTypeScreenshot:
				if entry.Screenshot != nil {
					timestamp = entry.Screenshot.Timestamp
				}
			case proxy.LogTypeExecution:
				if entry.Execution != nil {
					timestamp = entry.Execution.Timestamp
				}
			case proxy.LogTypeResponse:
				if entry.Response != nil {
					timestamp = entry.Response.Timestamp
				}
			case proxy.LogTypePanelMessage:
				if entry.PanelMessage != nil {
					timestamp = entry.PanelMessage.Timestamp
				}
			case proxy.LogTypeSketch:
				if entry.Sketch != nil {
					timestamp = entry.Sketch.Timestamp
				}
			}
		}

		// Update time range
		if !timestamp.IsZero() {
			if minTime.IsZero() || timestamp.Before(minTime) {
				minTime = timestamp
			}
			if maxTime.IsZero() || timestamp.After(maxTime) {
				maxTime = timestamp
			}
		}
	}

	// Set time range
	if !minTime.IsZero() {
		summary.TimeRange = TimeRange{Start: minTime, End: maxTime}
	}

	// Process errors - deduplicate and get top 5
	errorCounts := make(map[string]*ErrorSummary)
	for _, err := range errorEntries {
		key := err.Message
		if es, exists := errorCounts[key]; exists {
			es.Count++
		} else {
			// Extract error type
			errorType := "Error"
			parts := splitFirst(err.Message, ":")
			if len(parts) > 1 {
				errorType = parts[0]
			}
			errorCounts[key] = &ErrorSummary{
				Message: err.Message,
				Type:    errorType,
				Count:   1,
			}
		}
	}

	// Get top errors by count (max 10)
	var uniqueErrors []ErrorSummary
	for _, es := range errorCounts {
		uniqueErrors = append(uniqueErrors, *es)
	}
	// Sort by count descending
	sortErrorsByCount(uniqueErrors)
	if len(uniqueErrors) > 10 {
		uniqueErrors = uniqueErrors[:10]
	}
	summary.UniqueErrors = uniqueErrors

	// Recent errors (last 5) or full list if detail includes "errors"
	if detailSections["errors"] {
		summary.Errors = make([]CompactError, min(len(errorEntries), limit))
		startIdx := maxInt(0, len(errorEntries)-limit)
		for i := startIdx; i < len(errorEntries); i++ {
			err := errorEntries[i]
			summary.Errors[i-startIdx] = CompactError{
				Message:      err.Message,
				Type:         extractErrorType(err.Message),
				URL:          err.URL,
				Location:     formatLocation(err.Source, err.LineNo, err.ColNo),
				StackPreview: truncateStack(err.Stack, 3),
				Timestamp:    err.Timestamp.Format(time.RFC3339),
			}
		}
	} else if summary.ErrorCount > 0 {
		// Show recent 5 errors when detail not specified
		recentCount := min(5, len(errorEntries))
		summary.RecentErrors = make([]CompactError, recentCount)
		startIdx := maxInt(0, len(errorEntries)-5)
		for i := 0; i < recentCount; i++ {
			err := errorEntries[startIdx+i]
			summary.RecentErrors[i] = CompactError{
				Message:      err.Message,
				Type:         extractErrorType(err.Message),
				URL:          err.URL,
				Location:     formatLocation(err.Source, err.LineNo, err.ColNo),
				StackPreview: truncateStack(err.Stack, 3),
				Timestamp:    err.Timestamp.Format(time.RFC3339),
			}
		}
	}

	// HTTP requests - recent or full list
	if detailSections["http"] {
		summary.HTTPRequests = make([]CompactHTTPRequest, min(len(httpEntries), limit))
		startIdx := maxInt(0, len(httpEntries)-limit)
		for i := startIdx; i < len(httpEntries); i++ {
			http := httpEntries[i]
			summary.HTTPRequests[i-startIdx] = CompactHTTPRequest{
				Method:     http.Method,
				URL:        http.URL,
				StatusCode: http.StatusCode,
				Duration:   http.Duration.Milliseconds(),
				Timestamp:  http.Timestamp,
				Error:      http.Error,
			}
		}
	} else if summary.HTTPCount > 0 {
		// Show recent 5 requests
		recentCount := min(5, len(httpEntries))
		summary.RecentHTTP = make([]CompactHTTPRequest, recentCount)
		startIdx := maxInt(0, len(httpEntries)-5)
		for i := 0; i < recentCount; i++ {
			http := httpEntries[startIdx+i]
			summary.RecentHTTP[i] = CompactHTTPRequest{
				Method:     http.Method,
				URL:        http.URL,
				StatusCode: http.StatusCode,
				Duration:   http.Duration.Milliseconds(),
				Timestamp:  http.Timestamp,
				Error:      http.Error,
			}
		}
	}

	// Performance metrics - average and recent
	if summary.PerformanceCount > 0 {
		var totalLoadTime int64
		for _, perf := range perfEntries {
			totalLoadTime += perf.LoadEventEnd
		}
		summary.AvgLoadTime = totalLoadTime / int64(len(perfEntries))

		if detailSections["performance"] {
			summary.Performance = make([]CompactPerformance, min(len(perfEntries), limit))
			startIdx := maxInt(0, len(perfEntries)-limit)
			for i := startIdx; i < len(perfEntries); i++ {
				perf := perfEntries[i]
				summary.Performance[i-startIdx] = CompactPerformance{
					URL:              perf.URL,
					LoadTimeMs:       perf.LoadEventEnd,
					FirstPaintMs:     perf.FirstPaint,
					DOMContentLoaded: perf.DOMContentLoaded,
					Timestamp:        perf.Timestamp,
				}
			}
		} else {
			// Show recent 5
			recentCount := min(5, len(perfEntries))
			summary.RecentPerformance = make([]CompactPerformance, recentCount)
			startIdx := maxInt(0, len(perfEntries)-5)
			for i := 0; i < recentCount; i++ {
				perf := perfEntries[startIdx+i]
				summary.RecentPerformance[i] = CompactPerformance{
					URL:              perf.URL,
					LoadTimeMs:       perf.LoadEventEnd,
					FirstPaintMs:     perf.FirstPaint,
					DOMContentLoaded: perf.DOMContentLoaded,
					Timestamp:        perf.Timestamp,
				}
			}
		}
	}

	// Interactions - recent or full list
	if detailSections["interactions"] {
		summary.Interactions = make([]CompactInteraction, min(len(interactionEntries), limit))
		startIdx := maxInt(0, len(interactionEntries)-limit)
		for i := startIdx; i < len(interactionEntries); i++ {
			interaction := interactionEntries[i]
			summary.Interactions[i-startIdx] = CompactInteraction{
				Type:      interaction.EventType,
				Target:    interaction.Target.Selector,
				Timestamp: interaction.Timestamp,
			}
		}
	} else if summary.InteractionCount > 0 {
		// Show recent 5
		recentCount := min(5, len(interactionEntries))
		summary.RecentInteractions = make([]CompactInteraction, recentCount)
		startIdx := maxInt(0, len(interactionEntries)-5)
		for i := 0; i < recentCount; i++ {
			interaction := interactionEntries[startIdx+i]
			summary.RecentInteractions[i] = CompactInteraction{
				Type:      interaction.EventType,
				Target:    interaction.Target.Selector,
				Timestamp: interaction.Timestamp,
			}
		}
	}

	// Mutations - recent or full list
	if detailSections["mutations"] {
		summary.Mutations = make([]CompactMutation, min(len(mutationEntries), limit))
		startIdx := maxInt(0, len(mutationEntries)-limit)
		for i := startIdx; i < len(mutationEntries); i++ {
			mutation := mutationEntries[i]
			nodeCount := len(mutation.Added) + len(mutation.Removed)
			summary.Mutations[i-startIdx] = CompactMutation{
				Type:      mutation.MutationType,
				Target:    mutation.Target.Selector,
				Count:     nodeCount,
				Timestamp: mutation.Timestamp,
			}
		}
	} else if summary.MutationCount > 0 {
		// Show recent 5
		recentCount := min(5, len(mutationEntries))
		summary.RecentMutations = make([]CompactMutation, recentCount)
		startIdx := maxInt(0, len(mutationEntries)-5)
		for i := 0; i < recentCount; i++ {
			mutation := mutationEntries[startIdx+i]
			nodeCount := len(mutation.Added) + len(mutation.Removed)
			summary.RecentMutations[i] = CompactMutation{
				Type:      mutation.MutationType,
				Target:    mutation.Target.Selector,
				Count:     nodeCount,
				Timestamp: mutation.Timestamp,
			}
		}
	}

	_ = otherEntries // collected but not currently surfaced in summary output

	return nil, ProxyLogOutput{
		Summary: summary,
	}, nil
}

// Helper functions for summary

func splitFirst(s, sep string) []string {
	idx := 0
	for i := 0; i < len(s); i++ {
		if s[i:i+len(sep)] == sep {
			idx = i
			break
		}
	}
	if idx == 0 {
		return []string{s}
	}
	return []string{s[:idx], s[idx+len(sep):]}
}

func extractErrorType(message string) string {
	parts := splitFirst(message, ":")
	if len(parts) > 1 {
		return parts[0]
	}
	return "Error"
}

func formatLocation(source string, line, col int) string {
	if source == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d", source, line, col)
}

func truncateStack(stack string, maxLines int) string {
	if stack == "" {
		return ""
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(stack) && len(lines) < maxLines; i++ {
		if stack[i] == '\n' {
			lines = append(lines, stack[start:i])
			start = i + 1
		}
	}
	if start < len(stack) && len(lines) < maxLines {
		lines = append(lines, stack[start:])
	}
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}

func sortErrorsByCount(errors []ErrorSummary) {
	// Simple bubble sort by count (descending)
	for i := 0; i < len(errors); i++ {
		for j := i + 1; j < len(errors); j++ {
			if errors[j].Count > errors[i].Count {
				errors[i], errors[j] = errors[j], errors[i]
			}
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Helper functions

func parseTimeOrDuration(s string) (time.Time, error) {
	// Try parsing as duration first (e.g., "5m", "1h")
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}

	// Try parsing as RFC3339 timestamp
	return time.Parse(time.RFC3339, s)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}
