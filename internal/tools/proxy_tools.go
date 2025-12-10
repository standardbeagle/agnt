package tools

import (
	"context"
	"fmt"
	"time"

	"devtool-mcp/internal/proxy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProxyInput defines input for the proxy tool.
type ProxyInput struct {
	Action     string `json:"action" jsonschema:"Action: start, stop, status, list, exec"`
	ID         string `json:"id,omitempty" jsonschema:"Proxy ID (required for start/stop/status/exec)"`
	TargetURL  string `json:"target_url,omitempty" jsonschema:"Target URL to proxy (required for start)"`
	Port       int    `json:"port,omitempty" jsonschema:"Listen port (default: 8080)"`
	MaxLogSize int    `json:"max_log_size,omitempty" jsonschema:"Maximum log entries (default: 1000)"`
	Code       string `json:"code,omitempty" jsonschema:"JavaScript code to execute (required for exec)"`
	Global     bool   `json:"global,omitempty" jsonschema:"For list: include proxies from all directories (default: false)"`
	Help       bool   `json:"help,omitempty" jsonschema:"For exec: show __devtool API overview instead of executing code"`
	Describe   string `json:"describe,omitempty" jsonschema:"For exec: show detailed docs for a specific function (e.g. 'screenshot', 'interactions.getLastClick')"`
}

// CurrentPageInput defines input for the currentpage tool.
type CurrentPageInput struct {
	ProxyID   string `json:"proxy_id" jsonschema:"Proxy ID to query pages from"`
	Action    string `json:"action,omitempty" jsonschema:"Action: list, get, clear (default: list)"`
	SessionID string `json:"session_id,omitempty" jsonschema:"Specific session ID (required for get action)"`
}

// CurrentPageOutput defines output for currentpage tool.
type CurrentPageOutput struct {
	// For list
	Sessions []PageSessionOutput `json:"sessions,omitempty"`
	Count    int                 `json:"count,omitempty"`

	// For get
	Session *PageSessionOutput `json:"session,omitempty"`

	// For clear
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// PageSessionOutput represents a page session in the output.
type PageSessionOutput struct {
	ID             string                   `json:"id"`
	URL            string                   `json:"url"`
	PageTitle      string                   `json:"page_title,omitempty"`
	StartTime      time.Time                `json:"start_time"`
	LastActivity   time.Time                `json:"last_activity"`
	Active         bool                     `json:"active"`
	ResourceCount  int                      `json:"resource_count"`
	ErrorCount     int                      `json:"error_count"`
	HasPerformance bool                     `json:"has_performance"`
	LoadTime       int64                    `json:"load_time_ms,omitempty"`
	Resources      []string                 `json:"resources,omitempty"` // URLs of resources
	Errors         []map[string]interface{} `json:"errors,omitempty"`

	// Interaction tracking
	InteractionCount int                      `json:"interaction_count"`
	Interactions     []map[string]interface{} `json:"interactions,omitempty"` // Detailed view only

	// Mutation tracking
	MutationCount int                      `json:"mutation_count"`
	Mutations     []map[string]interface{} `json:"mutations,omitempty"` // Detailed view only
}

// ProxyOutput defines output for proxy tool.
type ProxyOutput struct {
	// For start
	ID         string `json:"id,omitempty"`
	TargetURL  string `json:"target_url,omitempty"`
	ListenAddr string `json:"listen_addr,omitempty"`

	// For status
	Running       bool            `json:"running,omitempty"`
	Uptime        string          `json:"uptime,omitempty"`
	TotalRequests int64           `json:"total_requests,omitempty"`
	LogStats      *LogStatsOutput `json:"log_stats,omitempty"`

	// For list
	Count     int          `json:"count,omitempty"`
	Proxies   []ProxyEntry `json:"proxies,omitempty"`
	Directory string       `json:"directory,omitempty"`
	Global    bool         `json:"global,omitempty"`

	// For stop/exec
	Success     bool   `json:"success,omitempty"`
	Message     string `json:"message,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"` // For exec action
}

// ProxyEntry represents a proxy in the list.
type ProxyEntry struct {
	ID            string `json:"id"`
	TargetURL     string `json:"target_url"`
	ListenAddr    string `json:"listen_addr"`
	Path          string `json:"path,omitempty"`
	Running       bool   `json:"running"`
	Uptime        string `json:"uptime"`
	TotalRequests int64  `json:"total_requests"`
}

// LogStatsOutput holds logger statistics.
type LogStatsOutput struct {
	TotalEntries     int64 `json:"total_entries"`
	AvailableEntries int64 `json:"available_entries"`
	MaxSize          int64 `json:"max_size"`
	Dropped          int64 `json:"dropped"`
}

// ProxyLogInput defines input for the proxylog tool.
type ProxyLogInput struct {
	ProxyID     string   `json:"proxy_id" jsonschema:"Proxy ID to query logs from"`
	Action      string   `json:"action,omitempty" jsonschema:"Action: query, clear, stats (default: query)"`
	Types       []string `json:"types,omitempty" jsonschema:"Filter by type: http, error, performance"`
	Methods     []string `json:"methods,omitempty" jsonschema:"Filter by HTTP method: GET, POST, etc."`
	URLPattern  string   `json:"url_pattern,omitempty" jsonschema:"URL substring to match"`
	StatusCodes []int    `json:"status_codes,omitempty" jsonschema:"Filter by HTTP status code"`
	Since       string   `json:"since,omitempty" jsonschema:"Start time (RFC3339 or duration like '5m')"`
	Until       string   `json:"until,omitempty" jsonschema:"End time (RFC3339)"`
	Limit       int      `json:"limit,omitempty" jsonschema:"Maximum results (default: 100)"`
}

// ProxyLogOutput defines output for proxylog tool.
type ProxyLogOutput struct {
	// For query
	Entries []LogEntryOutput `json:"entries,omitempty"`
	Count   int              `json:"count,omitempty"`

	// For stats
	Stats *LogStatsOutput `json:"stats,omitempty"`

	// For clear
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// LogEntryOutput represents a log entry in the output.
type LogEntryOutput struct {
	Type      string                 `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// RegisterProxyTools adds proxy-related MCP tools to the server.
func RegisterProxyTools(server *mcp.Server, pm *proxy.ProxyManager) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "currentpage",
		Description: `Get current page sessions with grouped resources and metrics.

Actions:
  list: List all active page sessions (default)
  get: Get detailed information for a specific session
  clear: Clear all page sessions

A page session groups together:
  - The initial HTML document request
  - All associated resource requests (JS, CSS, images, etc.)
  - Frontend JavaScript errors from that page
  - Performance metrics (page load time, paint timing, etc.)

Examples:
  currentpage {proxy_id: "dev"}
  currentpage {proxy_id: "dev", action: "list"}
  currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}
  currentpage {proxy_id: "dev", action: "clear"}

This provides a high-level view of active pages and their resources,
making it easy to understand page load behavior and debug issues.`,
	}, makeCurrentPageHandler(pm))

	mcp.AddTool(server, &mcp.Tool{
		Name: "proxy",
		Description: `Manage reverse proxy servers with traffic logging and frontend instrumentation.

Actions:
  start: Create and start a reverse proxy
  stop: Stop a running proxy
  status: Get proxy status and statistics
  list: List all running proxies
  exec: Execute JavaScript in connected browser clients

Examples:
  proxy {action: "start", id: "dev", target_url: "http://localhost:3000", port: 8080}
  proxy {action: "status", id: "dev"}
  proxy {action: "list"}
  proxy {action: "exec", id: "dev", code: "document.title"}
  proxy {action: "stop", id: "dev"}

The proxy automatically:
  - Logs all HTTP traffic (requests/responses)
  - Injects JavaScript to capture frontend errors
  - Captures performance metrics (page load, resources)
  - Provides WebSocket endpoint for metrics
  - Injects __devtool API with 50+ diagnostic functions

__devtool API (injected into browser):
  proxy {action: "exec", help: true}                    # Full API overview
  proxy {action: "exec", describe: "screenshot"}        # Detailed function docs
  proxy {action: "exec", describe: "interactions.getLastClick"}

Common __devtool examples:
  proxy {action: "exec", id: "dev", code: "__devtool.screenshot('homepage')"}
  proxy {action: "exec", id: "dev", code: "__devtool.log('test', 'info', {data: 1})"}
  proxy {action: "exec", id: "dev", code: "__devtool.interactions.getLastClickContext()"}
  proxy {action: "exec", id: "dev", code: "__devtool.mutations.highlightRecent(5000)"}
  proxy {action: "exec", id: "dev", code: "__devtool.inspect('#submit-btn')"}
  proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility()"}

Each proxy has separate log storage and WebSocket connections.`,
	}, makeProxyHandler(pm))

	mcp.AddTool(server, &mcp.Tool{
		Name: "proxylog",
		Description: `Query and analyze proxy traffic logs.

Actions:
  query: Search logs with filters (default)
  clear: Clear all logs for a proxy
  stats: Get log statistics

Log Types:
  http: HTTP request/response pairs
  error: Frontend JavaScript errors with stack traces
  performance: Page load and resource timing metrics
  custom: Custom log messages from __devtool.log()
  screenshot: Screenshots captured via __devtool.screenshot()
  execution: Results of executed JavaScript code
  response: JavaScript execution responses returned to MCP client

Examples:
  proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"]}
  proxylog {proxy_id: "dev", types: ["error"]}
  proxylog {proxy_id: "dev", types: ["performance"]}
  proxylog {proxy_id: "dev", types: ["custom"], limit: 50}
  proxylog {proxy_id: "dev", types: ["screenshot"]}
  proxylog {proxy_id: "dev", types: ["execution"]}
  proxylog {proxy_id: "dev", types: ["response"]}
  proxylog {proxy_id: "dev", url_pattern: "/api", status_codes: [500, 502]}
  proxylog {proxy_id: "dev", since: "5m", limit: 50}
  proxylog {proxy_id: "dev", action: "stats"}
  proxylog {proxy_id: "dev", action: "clear"}

Each proxy maintains its own separate log storage.`,
	}, makeProxyLogHandler(pm))
}

func makeProxyHandler(pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
		switch input.Action {
		case "start":
			return handleProxyStart(ctx, pm, input)
		case "stop":
			return handleProxyStop(ctx, pm, input)
		case "status":
			return handleProxyStatus(pm, input)
		case "list":
			return handleProxyList(pm)
		case "exec":
			return handleProxyExec(pm, input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: start, stop, status, list, exec", input.Action)), ProxyOutput{}, nil
		}
	}
}

func handleProxyStart(ctx context.Context, pm *proxy.ProxyManager, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for start"), ProxyOutput{}, nil
	}
	if input.TargetURL == "" {
		return errorResult("target_url required for start"), ProxyOutput{}, nil
	}

	if input.Port == 0 {
		input.Port = 8080
	}
	if input.MaxLogSize == 0 {
		input.MaxLogSize = 1000
	}

	config := proxy.ProxyConfig{
		ID:          input.ID,
		TargetURL:   input.TargetURL,
		ListenPort:  input.Port,
		MaxLogSize:  input.MaxLogSize,
		AutoRestart: true, // Enable auto-restart for development tool
	}

	// Use background context - proxy should outlive the MCP tool call
	proxyServer, err := pm.Create(context.Background(), config)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to start proxy: %v", err)), ProxyOutput{}, nil
	}

	return nil, ProxyOutput{
		ID:         proxyServer.ID,
		TargetURL:  proxyServer.TargetURL.String(),
		ListenAddr: proxyServer.ListenAddr,
		Message:    fmt.Sprintf("Proxy started. Access at http://localhost%s", proxyServer.ListenAddr),
	}, nil
}

func handleProxyStop(ctx context.Context, pm *proxy.ProxyManager, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for stop"), ProxyOutput{}, nil
	}

	err := pm.Stop(ctx, input.ID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to stop proxy: %v", err)), ProxyOutput{}, nil
	}

	return nil, ProxyOutput{
		Success: true,
		Message: fmt.Sprintf("Proxy %s stopped", input.ID),
	}, nil
}

func handleProxyStatus(pm *proxy.ProxyManager, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for status"), ProxyOutput{}, nil
	}

	proxyServer, err := pm.Get(input.ID)
	if err != nil {
		return errorResult(fmt.Sprintf("proxy not found: %s", input.ID)), ProxyOutput{}, nil
	}

	stats := proxyServer.Stats()

	return nil, ProxyOutput{
		ID:            stats.ID,
		TargetURL:     stats.TargetURL,
		ListenAddr:    stats.ListenAddr,
		Running:       stats.Running,
		Uptime:        formatDuration(stats.Uptime),
		TotalRequests: stats.TotalRequests,
		LogStats: &LogStatsOutput{
			TotalEntries:     stats.LoggerStats.TotalEntries,
			AvailableEntries: stats.LoggerStats.AvailableEntries,
			MaxSize:          stats.LoggerStats.MaxSize,
			Dropped:          stats.LoggerStats.Dropped,
		},
	}, nil
}

func handleProxyList(pm *proxy.ProxyManager) (*mcp.CallToolResult, ProxyOutput, error) {
	proxies := pm.List()

	entries := make([]ProxyEntry, len(proxies))
	for i, p := range proxies {
		stats := p.Stats()
		entries[i] = ProxyEntry{
			ID:            stats.ID,
			TargetURL:     stats.TargetURL,
			ListenAddr:    stats.ListenAddr,
			Running:       stats.Running,
			Uptime:        formatDuration(stats.Uptime),
			TotalRequests: stats.TotalRequests,
		}
	}

	return nil, ProxyOutput{
		Count:   len(proxies),
		Proxies: entries,
	}, nil
}

func handleProxyExec(pm *proxy.ProxyManager, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	// Handle help request - no proxy ID required
	if input.Help {
		return nil, ProxyOutput{
			Success: true,
			Message: GetAPIOverview(),
		}, nil
	}

	// Handle describe request - no proxy ID required
	if input.Describe != "" {
		doc, found := GetFunctionDescription(input.Describe)
		if !found {
			// List available functions
			names := ListFunctionNames()
			return nil, ProxyOutput{
				Success: false,
				Message: fmt.Sprintf("Function %q not found.\n\nAvailable functions:\n%v", input.Describe, names),
			}, nil
		}
		return nil, ProxyOutput{
			Success: true,
			Message: doc,
		}, nil
	}

	if input.ID == "" {
		return errorResult("id required for exec"), ProxyOutput{}, nil
	}
	if input.Code == "" {
		return errorResult("code required for exec"), ProxyOutput{}, nil
	}

	proxyServer, err := pm.Get(input.ID)
	if err != nil {
		return errorResult(fmt.Sprintf("proxy not found: %s", input.ID)), ProxyOutput{}, nil
	}

	execID, resultChan, err := proxyServer.ExecuteJavaScript(input.Code)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to execute: %v", err)), ProxyOutput{}, nil
	}

	// Wait for result with timeout
	timeout := 30 * time.Second
	select {
	case result := <-resultChan:
		if result == nil {
			return errorResult("execution channel closed without result"), ProxyOutput{}, nil
		}

		// Log the response
		responseLog := proxy.ExecutionResponse{
			ID:        fmt.Sprintf("resp-%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
			ExecID:    execID,
			Success:   result.Error == "",
			Result:    result.Result,
			Error:     result.Error,
			Duration:  result.Duration,
		}
		proxyServer.Logger().LogResponse(responseLog)

		// Return the execution result
		if result.Error != "" {
			return nil, ProxyOutput{
				Success:     false,
				ExecutionID: execID,
				Message:     fmt.Sprintf("JavaScript execution failed: %s", result.Error),
			}, nil
		}

		return nil, ProxyOutput{
			Success:     true,
			ExecutionID: execID,
			Message:     fmt.Sprintf("JavaScript executed successfully.\nResult: %s\nDuration: %v", result.Result, result.Duration),
		}, nil

	case <-time.After(timeout):
		// Log timeout as failed response
		responseLog := proxy.ExecutionResponse{
			ID:        fmt.Sprintf("resp-%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
			ExecID:    execID,
			Success:   false,
			Error:     fmt.Sprintf("execution timed out after %v (no response from browser)", timeout),
			Duration:  timeout,
		}
		proxyServer.Logger().LogResponse(responseLog)

		return errorResult(fmt.Sprintf("execution timed out after %v (no response from browser)", timeout)), ProxyOutput{}, nil
	}
}

func makeProxyLogHandler(pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
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
		case "clear":
			return handleProxyLogClear(proxyServer, input)
		case "stats":
			return handleProxyLogStats(proxyServer, input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: query, clear, stats", action)), ProxyLogOutput{}, nil
		}
	}
}

func handleProxyLogQuery(proxyServer *proxy.ProxyServer, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// Build filter
	filter := proxy.LogFilter{
		Methods:     input.Methods,
		URLPattern:  input.URLPattern,
		StatusCodes: input.StatusCodes,
		Limit:       input.Limit,
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
				Data:      data,
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
				Data:      data,
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
				Data:      data,
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
				Data:      data,
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
			}
			output[i] = LogEntryOutput{
				Type:      string(entry.Type),
				Timestamp: entry.Screenshot.Timestamp,
				Data:      data,
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
				Data:      data,
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
				Data:      data,
			}
		}
	}

	return nil, ProxyLogOutput{
		Entries: output,
		Count:   len(output),
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

// makeCurrentPageHandler creates the handler for the currentpage tool.
func makeCurrentPageHandler(pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
		if input.ProxyID == "" {
			return errorResult("proxy_id required"), CurrentPageOutput{}, nil
		}

		proxyServer, err := pm.Get(input.ProxyID)
		if err != nil {
			return errorResult(fmt.Sprintf("proxy not found: %s", input.ProxyID)), CurrentPageOutput{}, nil
		}

		action := input.Action
		if action == "" {
			action = "list"
		}

		switch action {
		case "list":
			return handleCurrentPageList(proxyServer)
		case "get":
			return handleCurrentPageGet(proxyServer, input)
		case "clear":
			return handleCurrentPageClear(proxyServer, input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: list, get, clear", action)), CurrentPageOutput{}, nil
		}
	}
}

func handleCurrentPageList(proxyServer *proxy.ProxyServer) (*mcp.CallToolResult, CurrentPageOutput, error) {
	sessions := proxyServer.PageTracker().GetActiveSessions()

	output := make([]PageSessionOutput, len(sessions))
	for i, session := range sessions {
		output[i] = convertPageSession(session, false)
	}

	return nil, CurrentPageOutput{
		Sessions: output,
		Count:    len(output),
	}, nil
}

func handleCurrentPageGet(proxyServer *proxy.ProxyServer, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	if input.SessionID == "" {
		return errorResult("session_id required for get action"), CurrentPageOutput{}, nil
	}

	session, ok := proxyServer.PageTracker().GetSession(input.SessionID)
	if !ok {
		return errorResult(fmt.Sprintf("session not found: %s", input.SessionID)), CurrentPageOutput{}, nil
	}

	sessionOutput := convertPageSession(session, true)

	return nil, CurrentPageOutput{
		Session: &sessionOutput,
	}, nil
}

func handleCurrentPageClear(proxyServer *proxy.ProxyServer, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	proxyServer.PageTracker().Clear()

	return nil, CurrentPageOutput{
		Success: true,
		Message: fmt.Sprintf("Page sessions cleared for proxy %s", input.ProxyID),
	}, nil
}

// convertPageSession converts a PageSession to output format.
func convertPageSession(session *proxy.PageSession, includeDetails bool) PageSessionOutput {
	output := PageSessionOutput{
		ID:             session.ID,
		URL:            session.URL,
		PageTitle:      session.PageTitle,
		StartTime:      session.StartTime,
		LastActivity:   session.LastActivity,
		Active:         session.Active,
		ResourceCount:  len(session.Resources),
		ErrorCount:     len(session.Errors),
		HasPerformance: session.Performance != nil,
	}

	if session.Performance != nil {
		output.LoadTime = session.Performance.LoadEventEnd
	}

	// Include detailed information if requested
	if includeDetails {
		// Add resource URLs
		output.Resources = make([]string, len(session.Resources))
		for i, res := range session.Resources {
			output.Resources[i] = res.URL
		}

		// Add error details
		output.Errors = make([]map[string]interface{}, len(session.Errors))
		for i, err := range session.Errors {
			output.Errors[i] = map[string]interface{}{
				"message": err.Message,
				"source":  err.Source,
				"lineno":  err.LineNo,
				"colno":   err.ColNo,
				"stack":   err.Stack,
			}
		}
	}

	return output
}
