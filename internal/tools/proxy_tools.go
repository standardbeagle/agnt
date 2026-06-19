package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"

	"github.com/standardbeagle/go-sdk/mcp"
)

// ProxyInput defines input for the proxy tool.
type ProxyInput struct {
	Action        string `json:"action" jsonschema:"Action: start, stop, status, list, exec, toast, chaos"`
	ID            string `json:"id,omitempty" jsonschema:"Proxy ID (required for start/stop/status/exec/toast/chaos)"`
	TargetURL     string `json:"target_url,omitempty" jsonschema:"Target URL to proxy (required for start)"`
	Port          int    `json:"port,omitempty" jsonschema:"Listen port (default: stable hash of target URL). Only specify if you need a specific port."`
	MaxLogSize    int    `json:"max_log_size,omitempty" jsonschema:"Maximum log entries (default: 1000)"`
	BindAddress   string `json:"bind_address,omitempty" jsonschema:"Bind address: '127.0.0.1' (default, localhost only) or '0.0.0.0' (all interfaces for tunnel/mobile testing)"`
	AllowExternal bool   `json:"allow_external,omitempty" jsonschema:"Required to bind to non-localhost addresses (0.0.0.0 or ::). Acknowledges network exposure risk."`
	PublicURL     string `json:"public_url,omitempty" jsonschema:"Public URL for tunnel services (e.g. 'https://abc123.trycloudflare.com'). Used for URL rewriting when behind a tunnel."`
	SkipTLSVerify bool   `json:"skip_tls_verify,omitempty" jsonschema:"Skip TLS certificate verification (default: false, certs are verified). Set to true for self-signed/expired certs in dev environments."`
	Code          string `json:"code,omitempty" jsonschema:"JavaScript code to execute (required for exec)"`
	FrameID       string `json:"frame_id,omitempty" jsonschema:"For exec: target a specific content frame by id (default: the active content frame). Rarely needed — omit to hit the frame the developer is interacting with."`
	Global        bool   `json:"global,omitempty" jsonschema:"For list: include proxies from all directories (default: false)"`
	Help          bool   `json:"help,omitempty" jsonschema:"For exec: show __devtool API overview instead of executing code"`
	Describe      string `json:"describe,omitempty" jsonschema:"For exec: show detailed docs for a specific function (e.g. 'screenshot', 'interactions.getLastClick')"`
	Search        string `json:"search,omitempty" jsonschema:"For exec: case-insensitive substring search across __devtool function names, descriptions, and signatures. Returns up to 10 compact matches. Combine with category to narrow results."`
	Category      string `json:"category,omitempty" jsonschema:"For exec search: optional category filter (e.g. 'accessibility', 'layout', 'inspection'). AND-combined with 'search'."`
	Hints         *bool  `json:"hints,omitempty" jsonschema:"For exec: scan submitted JS for raw DOM patterns and suggest __devtool helpers. Default: true. Set false to opt out."`
	ToastType     string `json:"toast_type,omitempty" jsonschema:"For toast: notification type (success, error, warning, info). Default: info"`
	ToastTitle    string `json:"toast_title,omitempty" jsonschema:"For toast: notification title (optional)"`
	ToastMessage  string `json:"toast_message,omitempty" jsonschema:"For toast: notification message (required for toast)"`
	ToastDuration int    `json:"toast_duration,omitempty" jsonschema:"For toast: duration in milliseconds (0 for default)"`
	// Tunnel configuration (for start action)
	Tunnel        string   `json:"tunnel,omitempty" jsonschema:"Tunnel provider: ngrok, cloudflared, tailscale, or custom. Creates public URL for the proxy."`
	TunnelArgs    []string `json:"tunnel_args,omitempty" jsonschema:"Additional arguments for tunnel command"`
	TunnelToken   string   `json:"tunnel_token,omitempty" jsonschema:"Authentication token for tunnel (e.g., ngrok authtoken)"`
	TunnelRegion  string   `json:"tunnel_region,omitempty" jsonschema:"Tunnel region (optional)"`
	TunnelCommand string   `json:"tunnel_command,omitempty" jsonschema:"Custom tunnel command (when tunnel is 'custom'). Use {{PORT}} as placeholder."`

	// Chaos-related fields
	ChaosOperation string            `json:"chaos_operation,omitempty" jsonschema:"For chaos: enable, disable, status, set, preset, add_rule, remove_rule, list_rules, stats, clear"`
	ChaosPreset    string            `json:"chaos_preset,omitempty" jsonschema:"For chaos preset: mobile-3g, mobile-4g, flaky-api, race-condition, stale-tab, slow-connection, connection-drops, etc."`
	ChaosRules     []ChaosRuleInput  `json:"chaos_rules,omitempty" jsonschema:"For chaos set: array of chaos rules to configure"`
	ChaosRule      *ChaosRuleInput   `json:"chaos_rule,omitempty" jsonschema:"For chaos add_rule: single rule to add"`
	ChaosRuleID    string            `json:"chaos_rule_id,omitempty" jsonschema:"For chaos remove_rule: ID of rule to remove"`
	ChaosConfig    *ChaosConfigInput `json:"chaos_config,omitempty" jsonschema:"For chaos set: full chaos configuration"`
}

// ChaosRuleInput defines input for a single chaos rule.
type ChaosRuleInput struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Type        string   `json:"type"` // latency, out_of_order, slow_drip, disconnect, http_error, truncate, etc.
	Enabled     bool     `json:"enabled"`
	URLPattern  string   `json:"url_pattern,omitempty"`
	Methods     []string `json:"methods,omitempty"`
	Probability float64  `json:"probability,omitempty"` // 0.0-1.0, default 1.0

	// Latency config
	MinLatencyMs int `json:"min_latency_ms,omitempty"`
	MaxLatencyMs int `json:"max_latency_ms,omitempty"`
	JitterMs     int `json:"jitter_ms,omitempty"`

	// Slow-drip config
	BytesPerMs int `json:"bytes_per_ms,omitempty"`
	ChunkSize  int `json:"chunk_size,omitempty"`

	// Connection drop config
	DropAfterPercent float64 `json:"drop_after_percent,omitempty"`
	DropAfterBytes   int64   `json:"drop_after_bytes,omitempty"`

	// Error injection config
	ErrorCodes   []int  `json:"error_codes,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	// Truncation config
	TruncatePercent float64 `json:"truncate_percent,omitempty"`

	// Out-of-order config
	ReorderMinRequests int `json:"reorder_min_requests,omitempty"`
	ReorderMaxWaitMs   int `json:"reorder_max_wait_ms,omitempty"`

	// Stale config
	StaleDelayMs int64 `json:"stale_delay_ms,omitempty"`
}

// ChaosConfigInput defines input for full chaos configuration.
type ChaosConfigInput struct {
	Enabled     bool             `json:"enabled"`
	Rules       []ChaosRuleInput `json:"rules,omitempty"`
	GlobalOdds  float64          `json:"global_odds,omitempty"`  // 0.0-1.0
	Seed        int64            `json:"seed,omitempty"`         // For reproducible chaos
	LoggingMode int              `json:"logging_mode,omitempty"` // 0=silent, 1=testing, 2=coordinated
}

// ProxyOutput defines output for proxy tool.
type ProxyOutput struct {
	// For start
	ID          string `json:"id,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
	ListenAddr  string `json:"listen_addr,omitempty"`
	BindAddress string `json:"bind_address,omitempty"`
	PublicURL   string `json:"public_url,omitempty"`
	TunnelURL   string `json:"tunnel_url,omitempty"` // Public tunnel URL if tunnel is configured

	// For status
	Running       bool            `json:"running,omitempty"`
	Uptime        string          `json:"uptime,omitempty"`
	TotalRequests int64           `json:"total_requests,omitempty"`
	LogStats      *LogStatsOutput `json:"log_stats,omitempty"`
	Tunnel        *TunnelStatus   `json:"tunnel,omitempty"` // Tunnel status if configured

	// Readiness-gate state: when a proxy declares `wait-for`, it
	// binds immediately but does not forward until every listed
	// script signals ready. State is "waiting_for_dependencies"
	// while gating, "running" once the gate opens.
	State     string   `json:"state,omitempty"`
	WaitingOn []string `json:"waiting_on,omitempty"`

	// For list
	Count       int          `json:"count"`
	Proxies     []ProxyEntry `json:"proxies,omitempty"`
	ProjectPath string       `json:"project_path,omitempty"`
	SessionCode string       `json:"session_code,omitempty"`
	Global      bool         `json:"global,omitempty"`

	// For stop/exec
	Success     bool   `json:"success,omitempty"`
	Message     string `json:"message,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"` // For exec action

	// For exec search
	SearchResult *APISearchResult `json:"search_result,omitempty"`

	// For exec: advisory hints when raw DOM patterns duplicate __devtool helpers
	ExecHints []string `json:"hints,omitempty"`

	// For chaos
	ChaosEnabled bool              `json:"chaos_enabled,omitempty"`
	ChaosStats   *ChaosStatsOutput `json:"chaos_stats,omitempty"`
	ChaosRules   []ChaosRuleOutput `json:"chaos_rules,omitempty"`
	ChaosPresets []string          `json:"chaos_presets,omitempty"`
}

// ChaosStatsOutput holds chaos engine statistics.
type ChaosStatsOutput struct {
	TotalRequests   int64            `json:"total_requests"`
	AffectedCount   int64            `json:"affected_count"`
	LatencyInjected int64            `json:"latency_injected_ms"`
	ErrorsInjected  int64            `json:"errors_injected"`
	DropsInjected   int64            `json:"drops_injected"`
	TruncatedCount  int64            `json:"truncated_count"`
	ReorderedCount  int64            `json:"reordered_count"`
	RuleStats       map[string]int64 `json:"rule_stats,omitempty"`
}

// ChaosRuleOutput represents a chaos rule in the output.
type ChaosRuleOutput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Type         string   `json:"type"`
	Enabled      bool     `json:"enabled"`
	URLPattern   string   `json:"url_pattern,omitempty"`
	Methods      []string `json:"methods,omitempty"`
	Probability  float64  `json:"probability"`
	TimesApplied int64    `json:"times_applied"`
}

// TunnelStatus represents tunnel status information.
type TunnelStatus struct {
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
}

// ProxyEntry represents a proxy in the list.
type ProxyEntry struct {
	ID            string `json:"id"`
	TargetURL     string `json:"target_url"`
	ListenAddr    string `json:"listen_addr"`
	BindAddress   string `json:"bind_address,omitempty"`
	PublicURL     string `json:"public_url,omitempty"`
	Path          string `json:"path,omitempty"`
	Running       bool   `json:"running"`
	Uptime        string `json:"uptime"`
	TotalRequests int64  `json:"total_requests"`
	TunnelURL     string `json:"tunnel_url,omitempty"`
	TunnelRunning bool   `json:"tunnel_running,omitempty"`

	// Readiness-gate fields: populated when the proxy is waiting on
	// declared `wait-for` dependencies. State is
	// "waiting_for_dependencies" while gating, "running" otherwise.
	State     string   `json:"state,omitempty"`
	WaitingOn []string `json:"waiting_on,omitempty"`
}

// LogStatsOutput holds logger statistics.
type LogStatsOutput struct {
	TotalEntries     int64 `json:"total_entries"`
	AvailableEntries int64 `json:"available_entries"`
	MaxSize          int64 `json:"max_size"`
	Dropped          int64 `json:"dropped"`
}

// RegisterProxyTools adds proxy-related MCP tools to the server.
func RegisterProxyTools(server *mcp.Server, pm *proxy.ProxyManager) {
	addLenientTool(server, &mcp.Tool{
		Name: "currentpage",
		Description: `Get current page sessions with grouped resources and metrics. Uses compact format by default.

Actions:
  list: List all active page sessions with summary counts (default)
  get: Get information for a specific session (compact by default)
  clear: Clear all page sessions

A page session groups together:
  - The initial HTML document request
  - All associated resource requests (JS, CSS, images, etc.)
  - Frontend JavaScript errors from that page
  - Performance metrics (page load time, paint timing, etc.)
  - User interactions (clicks, scrolls, form inputs)
  - DOM mutations (elements added/removed)

Output Format:
  - DEFAULT (compact): Counts and metadata only (e.g., resource_count: 15, error_count: 2)
  - With raw: true: Full arrays with all resources, errors, interactions, mutations

Examples (compact format):
  currentpage {proxy_id: "dev"}
  currentpage {proxy_id: "dev", action: "list"}
  currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}

Full Details (raw format):
  currentpage {proxy_id: "dev", action: "get", session_id: "page-1", raw: true}

Clear Sessions:
  currentpage {proxy_id: "dev", action: "clear"}

Tip: For detailed summaries with recent errors/interactions, use proxylog summary instead.

This provides a high-level view of active pages and their resources,
making it easy to understand page load behavior and debug issues.`,
	}, makeCurrentPageHandler(pm))

	addLenientTool(server, &mcp.Tool{
		Name:        "proxy",
		Description: ProxyToolDescription,
	}, makeProxyHandler(pm))

	addLenientTool(server, &mcp.Tool{
		Name: "proxylog",
		Description: `Query and analyze proxy traffic logs with compact, human-readable output by default.

Actions:
  query: Search logs with filters (default) - returns compact semi-structured format
  summary: Get overview with counts + top errors + recent items (RECOMMENDED for initial analysis)
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
  interaction: User interactions (clicks, keyboard, scroll)
  mutation: DOM mutations (elements added/removed/modified)
  diagnostic: Server-side proxy diagnostics (connection errors, timeouts, etc.)

Output Format:
  - DEFAULT: Compact semi-structured text (easy to read, token-efficient)
    Example: "GET /api/users → 200 (45ms)"
  - With raw: true: Full JSON dumps (for programmatic processing)

Consolidated Error Stream:
  Use errors_only: true to get all errors from all sources in one query:
  - HTTP errors (4xx, 5xx status codes)
  - Frontend JavaScript errors
  - Proxy diagnostics (connection refused, timeouts, etc.)
  - Custom error logs

Query Examples (compact format):
  proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"]}
  proxylog {proxy_id: "dev", types: ["error"]}
  proxylog {proxy_id: "dev", types: ["performance"]}
  proxylog {proxy_id: "dev", types: ["http"], since: "5m", limit: 50}

Consolidated Error Examples:
  proxylog {proxy_id: "dev", errors_only: true}
  proxylog {proxy_id: "dev", errors_only: true, since: "10m"}
  proxylog {proxy_id: "dev", types: ["diagnostic"], diagnostic_levels: ["error", "warning"]}

Query with Full Data (raw format):
  proxylog {proxy_id: "dev", types: ["error"], raw: true}
  proxylog {proxy_id: "dev", types: ["http"], raw: true, limit: 20}

Summary Examples (RECOMMENDED for first look):
  proxylog {proxy_id: "dev", action: "summary"}
  proxylog {proxy_id: "dev", action: "summary", detail: ["errors"], limit: 10}
  proxylog {proxy_id: "dev", action: "summary", detail: ["http", "errors", "diagnostics"]}

Stats & Clear:
  proxylog {proxy_id: "dev", action: "stats"}
  proxylog {proxy_id: "dev", action: "clear"}

Each proxy maintains its own separate log storage.`,
	}, makeProxyLogHandler(pm))
}

func makeProxyHandler(pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
		if err := validateProxyInput(input); err != nil {
			return errorResult(validationError("proxy", err)), ProxyOutput{}, nil
		}

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

	// Use -1 to signal "use default" (hash-based port), 0 means auto-assign
	listenPort := input.Port
	if listenPort == 0 {
		listenPort = -1 // Trigger hash-based default in NewProxyServer
	}
	if input.MaxLogSize == 0 {
		input.MaxLogSize = 1000
	}

	config := proxy.ProxyConfig{
		ID:            input.ID,
		TargetURL:     input.TargetURL,
		ListenPort:    listenPort,
		MaxLogSize:    input.MaxLogSize,
		AutoRestart:   true, // Enable auto-restart for development tool
		AllowExternal: input.AllowExternal,
		SkipTLSVerify: input.SkipTLSVerify,
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
		State:         runtimeStateFromStats(stats),
		WaitingOn:     stats.WaitingFor,
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

// runtimeStateFromStats returns the `state` string emitted in
// proxy status / proxy list output. Mirrors the daemon-side
// proxyRuntimeStatus helper so legacy mode (no daemon) surfaces the
// same vocabulary the AI agent already understands.
func runtimeStateFromStats(stats proxy.ProxyStats) string {
	if !stats.Running {
		return "stopped"
	}
	if !stats.ReadyForForwarding {
		return "waiting_for_dependencies"
	}
	return "running"
}

func handleProxyList(pm *proxy.ProxyManager) (*mcp.CallToolResult, ProxyOutput, error) {
	// Legacy non-daemon mode has no session registry — a single in-process
	// manager serves the one caller, so an audited unscoped list is correct.
	proxies := pm.ListScoped(scope.Unscoped("legacy non-daemon proxy list"))

	entries := make([]ProxyEntry, len(proxies))
	for i, p := range proxies {
		stats := p.Stats()
		entries[i] = ProxyEntry{
			ID:            stats.ID,
			TargetURL:     stats.TargetURL,
			ListenAddr:    stats.ListenAddr,
			Running:       stats.Running,
			State:         runtimeStateFromStats(stats),
			WaitingOn:     stats.WaitingFor,
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

	// Handle search request - no proxy ID required. Mutually exclusive
	// with code execution: if both are present, favor the discovery path
	// but surface the conflict so callers don't silently get the wrong
	// behavior.
	if input.Search != "" || input.Category != "" {
		if input.Code != "" {
			return errorResult("cannot combine 'search' with 'code'; run search first, then exec with the resolved function"), ProxyOutput{}, nil
		}
		result := SearchAPIFunctions(input.Search, input.Category)
		return nil, ProxyOutput{
			Success:      true,
			SearchResult: &result,
		}, nil
	}

	if input.ID == "" {
		return errorResult("id required for exec"), ProxyOutput{}, nil
	}
	if input.Code == "" {
		return errorResult("code required for exec"), ProxyOutput{}, nil
	}

	// Scan for anti-pattern hints before execution (advisory only, never blocks).
	// Default enabled; set hints: false to opt out.
	var execHints []string
	if input.Hints == nil || *input.Hints {
		execHints = ScanForHints(input.Code)
	}

	proxyServer, err := pm.Get(input.ID)
	if err != nil {
		return errorResult(fmt.Sprintf("proxy not found: %s", input.ID)), ProxyOutput{}, nil
	}

	execID, resultChan, err := proxyServer.ExecuteJavaScript(input.Code, input.FrameID)
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

		// Handle large results saved to file
		if result.FilePath != "" {
			return nil, ProxyOutput{
				Success:     true,
				ExecutionID: execID,
				ExecHints:   execHints,
				Message: fmt.Sprintf(`JavaScript executed successfully.
Result: Large response saved to file
File: %s
Duration: %v

Use the Read tool to view the full result.`, result.FilePath, result.Duration),
			}, nil
		}

		// Check if result is a screenshot and include file path if available
		message := fmt.Sprintf("JavaScript executed successfully.\nResult: %s\nDuration: %v", result.Result, result.Duration)
		if screenshotPath := detectAndLookupScreenshot(proxyServer, result.Result); screenshotPath != "" {
			message = fmt.Sprintf("JavaScript executed successfully.\nResult: %s\nScreenshot saved: %s\nDuration: %v", result.Result, screenshotPath, result.Duration)
		}

		return nil, ProxyOutput{
			Success:     true,
			ExecutionID: execID,
			ExecHints:   execHints,
			Message:     message,
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

// detectAndLookupScreenshot checks if the result looks like a screenshot result
// and if so, looks up the file path from the proxy's capture registry.
func detectAndLookupScreenshot(proxyServer *proxy.ProxyServer, resultJSON string) string {
	var result struct {
		Name   string `json:"name"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return ""
	}
	// Check if this looks like a screenshot result (has name, width, height)
	if result.Name != "" && result.Width > 0 && result.Height > 0 {
		return proxyServer.LookupCapture(result.Name)
	}
	return ""
}
