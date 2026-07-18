package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/standardbeagle/go-sdk/mcp"
)

// ProxyLogInput defines input for the proxylog tool.
type ProxyLogInput struct {
	ProxyID          string   `json:"proxy_id,omitempty" jsonschema:"Proxy ID to query logs from (preferred)"`
	ID               string   `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Action           string   `json:"action,omitempty" jsonschema:"Action: query (default), summary, clear, stats"`
	Types            []string `json:"types,omitempty" jsonschema:"Log types to filter (unknown types are rejected): http, error, performance, custom, screenshot, execution, response, interaction, mutation, panel_message, sketch, screenshot_capture, element_capture, sketch_capture, design_state, design_request, design_chat, design_edit, walkthrough, responsive_request, responsive_state, diagnostic, process, hook, incident_digest"`
	Methods          []string `json:"methods,omitempty" jsonschema:"HTTP methods to filter (e.g., GET, POST)"`
	URLPattern       string   `json:"url_pattern,omitempty" jsonschema:"URL pattern to match (substring)"`
	StatusCodes      []int    `json:"status_codes,omitempty" jsonschema:"HTTP status codes to filter"`
	Limit            int      `json:"limit,omitempty" jsonschema:"query action: max entries returned. summary action: max items per detailed section. Default: 100"`
	Since            string   `json:"since,omitempty" jsonschema:"Start time filter (RFC3339 or duration like '5m')"`
	Until            string   `json:"until,omitempty" jsonschema:"End time filter (RFC3339)"`
	Raw              bool     `json:"raw,omitempty" jsonschema:"Return full JSON dumps instead of compact format"`
	ErrorsOnly       bool     `json:"errors_only,omitempty" jsonschema:"Filter to error-class entries only: HTTP 4xx/5xx, JS errors, error-level diagnostics, custom error-level logs. Excludes diagnostic warnings and custom warn-level logs (they are not error-class) — use diagnostic_levels or types for those"`
	DiagnosticLevels []string `json:"diagnostic_levels,omitempty" jsonschema:"Diagnostic levels to include: error, warning, info"`
	InteractionTypes []string `json:"interaction_types,omitempty" jsonschema:"Interaction event types to include: click, dblclick, keydown, input, scroll, focus, blur, submit, contextmenu, mousemove"`
	MutationTypes    []string `json:"mutation_types,omitempty" jsonschema:"DOM mutation types to include: added, removed, attributes, characterData"`
	Frames           []string `json:"frames,omitempty" jsonschema:"Restrict to entries emitted by these content-frame ids"`
	MessagePattern   string   `json:"message_pattern,omitempty" jsonschema:"Substring match on the message of error, custom, and diagnostic entries"`
	MinDurationMs    int64    `json:"min_duration_ms,omitempty" jsonschema:"Restrict to HTTP entries whose round-trip is at least this many milliseconds (find slow requests)"`
	Detail           []string `json:"detail,omitempty" jsonschema:"For summary: sections to include full detail for (errors, http, performance, interactions, mutations, other)"`
}

func (input ProxyLogInput) hasFilters() bool {
	return len(input.Types) > 0 || len(input.Methods) > 0 || input.URLPattern != "" ||
		len(input.StatusCodes) > 0 || input.Since != "" || input.Until != "" ||
		input.ErrorsOnly || len(input.DiagnosticLevels) > 0 ||
		len(input.InteractionTypes) > 0 || len(input.MutationTypes) > 0 ||
		len(input.Frames) > 0 || input.MessagePattern != "" || input.MinDurationMs > 0
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
				// Raw mode promises "full JSON dumps": the recorder already
				// captures request/response headers and bodies (10KB cap, with
				// an inline "... [truncated]" marker on the response and a
				// "[request body ...]" marker on the request), so surface them
				// here instead of dropping them silently — otherwise the schema
				// claim is a lie and the agent cannot inspect payloads.
				if len(entry.HTTP.RequestHeaders) > 0 {
					data["request_headers"] = entry.HTTP.RequestHeaders
				}
				if entry.HTTP.RequestBody != "" {
					data["request_body"] = entry.HTTP.RequestBody
				}
				if len(entry.HTTP.ResponseHeaders) > 0 {
					data["response_headers"] = entry.HTTP.ResponseHeaders
				}
				if entry.HTTP.ResponseBody != "" {
					data["response_body"] = entry.HTTP.ResponseBody
					data["response_truncated"] = strings.HasSuffix(entry.HTTP.ResponseBody, "... [truncated]")
				}
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.HTTP.Timestamp,
					Data:      marshalData(data),
				}
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
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.Error.Timestamp,
					Data:      marshalData(data),
				}
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
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.Performance.Timestamp,
					Data:      marshalData(data),
				}
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
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.Custom.Timestamp,
					Data:      marshalData(data),
				}
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
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.Screenshot.Timestamp,
					Data:      marshalData(data),
				}
			}

		case proxy.LogTypeExecution:
			if entry.Execution != nil {
				data["id"] = entry.Execution.ID
				data["code"] = entry.Execution.Code
				data["result"] = entry.Execution.Result
				data["error"] = entry.Execution.Error
				data["duration_ms"] = entry.Execution.Duration.Milliseconds()
				data["url"] = entry.Execution.URL
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.Execution.Timestamp,
					Data:      marshalData(data),
				}
			}

		case proxy.LogTypeResponse:
			if entry.Response != nil {
				data["id"] = entry.Response.ID
				data["exec_id"] = entry.Response.ExecID
				data["success"] = entry.Response.Success
				data["result"] = entry.Response.Result
				data["error"] = entry.Response.Error
				data["duration_ms"] = entry.Response.Duration.Milliseconds()
				output[i] = LogEntryOutput{
					Type:      string(entry.Type),
					Timestamp: entry.Response.Timestamp,
					Data:      marshalData(data),
				}
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
		}

		// Fallback for unrecognized types and for known types whose payload
		// pointer is nil (would otherwise be a zero-value output): serialize
		// the whole entry and recover a timestamp from the payload envelope.
		if output[i].Type == "" {
			output[i] = rawFallbackOutput(entry)
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

		case proxy.LogTypeWalkthrough:
			if entry.Walkthrough != nil {
				timestamp = entry.Walkthrough.Timestamp
				w := entry.Walkthrough
				data = fmt.Sprintf("walkthrough %s: %s", w.Event, w.ScriptID)
				if w.Total > 0 {
					data += fmt.Sprintf(" step %d/%d", w.StepIndex+1, w.Total)
				}
				if w.StepTitle != "" {
					data += fmt.Sprintf(" — %s", w.StepTitle)
				}
				if w.How != "" {
					data += fmt.Sprintf(" (via %s)", w.How)
				}
				if w.Message != "" {
					data += "\n  " + w.Message
				}
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
		case proxy.LogTypeDesignEdit:
			if entry.DesignEdit != nil {
				timestamp = entry.DesignEdit.Timestamp
				data = fmt.Sprintf("Design edit: %s %s", entry.DesignEdit.Selector, formatDeltaPairs(entry.DesignEdit.Deltas))
			}
		default:
			// For other types, serialize the whole entry — but elide heavy
			// base64 image blobs (sketch/capture image_data, captured-area
			// data) so an unhandled type carrying a screenshot cannot dump
			// megabytes of base64 into the agent's context.
			data, timestamp = marshalEntryElided(entry), entryPayloadTimestamp(entry)
		}

		// A known type with a nil payload leaves timestamp/data unset. Leave the
		// timestamp as the zero value (an explicit "unknown time" sentinel) rather
		// than stamping time.Now() — a fabricated now would misdate the entry and
		// silently corrupt any since/until reasoning the agent does downstream.
		if data == "" {
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

// rawFallbackOutput serializes an entry whose typed payload is missing or
// unrecognized, recovering the timestamp from the nested payload JSON when
// present and leaving it zero otherwise. Heavy base64 image blobs are elided
// (see marshalEntryElided) so a captured screenshot/sketch does not flood the
// agent with base64.
func rawFallbackOutput(entry proxy.LogEntry) LogEntryOutput {
	// Leave a missing timestamp as the zero value (explicit "unknown time")
	// rather than stamping time.Now(), which would misdate the entry.
	return LogEntryOutput{
		Type:      string(entry.Type),
		Data:      marshalEntryElided(entry),
		Timestamp: entryPayloadTimestamp(entry),
	}
}

// entryPayloadTimestamp recovers the timestamp from the nested typed payload of
// a LogEntry via its JSON envelope (the payload object is keyed by the entry
// type). Returns the zero time when absent — callers decide the fallback.
func entryPayloadTimestamp(entry proxy.LogEntry) time.Time {
	b, err := json.Marshal(entry)
	if err != nil {
		return time.Time{}
	}
	em := make(map[string]interface{})
	if json.Unmarshal(b, &em) != nil {
		return time.Time{}
	}
	sub, ok := em[string(entry.Type)].(map[string]interface{})
	if !ok {
		return time.Time{}
	}
	ts, ok := sub["timestamp"].(string)
	if !ok {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

// marshalEntryElided serializes a full LogEntry to JSON with heavy base64 image
// blobs replaced by a compact "[elided N bytes]" marker (see elideBase64), so
// the raw and fallback serializers never dump megabytes of screenshot/sketch
// data into the agent's context. Returns "{}" on marshal failure.
func marshalEntryElided(entry proxy.LogEntry) string {
	b, err := json.Marshal(entry)
	if err != nil {
		return "{}"
	}
	var m interface{}
	if json.Unmarshal(b, &m) != nil {
		return string(b)
	}
	out, err := json.Marshal(elideBase64(m))
	if err != nil {
		return string(b)
	}
	return string(out)
}

// elideBase64 walks a decoded JSON value and replaces heavy base64 image blobs
// with a "[elided N bytes]" marker. It elides string values under the key
// "image_data" (always base64 PNG) and under "data" when the value is a long
// string (a captured area's base64 payload); structured "data" maps
// (Custom.Data, Execution.Data) are recursed into, never elided.
func elideBase64(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			if s, ok := child.(string); ok {
				if (k == "image_data" && s != "") || (k == "data" && len(s) > 1024) {
					val[k] = fmt.Sprintf("[elided %d bytes]", len(s))
					continue
				}
			}
			val[k] = elideBase64(child)
		}
		return val
	case []interface{}:
		for i, child := range val {
			val[i] = elideBase64(child)
		}
		return val
	default:
		return v
	}
}

// formatDeltaPairs renders a property→value delta map as a compact, stably
// ordered "prop=value" list for log summaries.
func formatDeltaPairs(deltas map[string]string) string {
	if len(deltas) == 0 {
		return "(no delta)"
	}
	keys := make([]string, 0, len(deltas))
	for k := range deltas {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + deltas[k]
	}
	return strings.Join(pairs, " ")
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
