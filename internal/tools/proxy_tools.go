package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProxyInput defines input for the proxy tool.
type ProxyInput struct {
	Action        string `json:"action" jsonschema:"Action: start, stop, status, list, exec, toast, chaos"`
	ID            string `json:"id,omitempty" jsonschema:"Proxy ID (required for start/stop/status/exec/toast/chaos)"`
	TargetURL     string `json:"target_url,omitempty" jsonschema:"Target URL to proxy (required for start)"`
	Port          int    `json:"port,omitempty" jsonschema:"Listen port (default: stable hash of target URL). Only specify if you need a specific port."`
	MaxLogSize    int    `json:"max_log_size,omitempty" jsonschema:"Maximum log entries (default: 1000)"`
	BindAddress   string `json:"bind_address,omitempty" jsonschema:"Bind address: '127.0.0.1' (default, localhost only) or '0.0.0.0' (all interfaces for tunnel/mobile testing)"`
	PublicURL     string `json:"public_url,omitempty" jsonschema:"Public URL for tunnel services (e.g. 'https://abc123.trycloudflare.com'). Used for URL rewriting when behind a tunnel."`
	VerifyTLS     bool   `json:"verify_tls,omitempty" jsonschema:"Verify TLS certificates (default: false, accepts self-signed/expired certs for dev). Set to true for strict validation."`
	Code          string `json:"code,omitempty" jsonschema:"JavaScript code to execute (required for exec)"`
	Global        bool   `json:"global,omitempty" jsonschema:"For list: include proxies from all directories (default: false)"`
	Help          bool   `json:"help,omitempty" jsonschema:"For exec: show __devtool API overview instead of executing code"`
	Describe      string `json:"describe,omitempty" jsonschema:"For exec: show detailed docs for a specific function (e.g. 'screenshot', 'interactions.getLastClick')"`
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

// CurrentPageInput defines input for the currentpage tool.
type CurrentPageInput struct {
	ProxyID   string   `json:"proxy_id" jsonschema:"Proxy ID to query pages from"`
	Action    string   `json:"action,omitempty" jsonschema:"Action: list, get, summary, clear (default: list)"`
	SessionID string   `json:"session_id,omitempty" jsonschema:"Specific session ID (required for get/summary action)"`
	Detail    []string `json:"detail,omitempty" jsonschema:"For summary: sections to include full detail for (interactions, mutations, errors, resources)"`
	Limit     int      `json:"limit,omitempty" jsonschema:"For summary: max items per detailed section (default: 5, max: 100)"`
	Raw       bool     `json:"raw,omitempty" jsonschema:"For get: return full arrays with all details instead of compact format (default: false)"`
}

// CurrentPageOutput defines output for currentpage tool.
type CurrentPageOutput struct {
	// For list
	Sessions []PageSessionOutput `json:"sessions,omitempty"`
	Count    int                 `json:"count,omitempty"`

	// For get
	Session *PageSessionOutput `json:"session,omitempty"`

	// For summary
	Summary *PageSummaryOutput `json:"summary,omitempty"`

	// For clear
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// PageSummaryOutput provides a compact summary of a large page without blowing context.
type PageSummaryOutput struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	PageTitle    string    `json:"page_title,omitempty"`
	StartTime    time.Time `json:"start_time"`
	LastActivity time.Time `json:"last_activity"`
	Active       bool      `json:"active"`

	// Resource summary
	ResourceCount    int            `json:"resource_count"`
	ResourcesByType  map[string]int `json:"resources_by_type,omitempty"` // e.g., {"js": 5, "css": 3, "img": 10}
	TotalPayloadSize int64          `json:"total_payload_size,omitempty"`
	Resources        []string       `json:"resources,omitempty"` // Full list when detail=["resources"]

	// Error summary
	ErrorCount   int            `json:"error_count"`
	UniqueErrors []ErrorSummary `json:"unique_errors,omitempty"`  // Deduplicated errors with counts
	ErrorsByType map[string]int `json:"errors_by_type,omitempty"` // e.g., {"ReferenceError": 3}
	Errors       []CompactError `json:"errors,omitempty"`         // Compact error list when detail=["errors"]

	// Performance
	LoadTimeMs       int64 `json:"load_time_ms,omitempty"`
	FirstPaintMs     int64 `json:"first_paint_ms,omitempty"`
	DOMContentLoaded int64 `json:"dom_content_loaded_ms,omitempty"`

	// Interaction summary
	InteractionCount   int                      `json:"interaction_count"`
	InteractionsByType map[string]int           `json:"interactions_by_type,omitempty"` // e.g., {"click": 5, "scroll": 10}
	RecentInteractions []map[string]interface{} `json:"recent_interactions,omitempty"`  // Last N (default 5)
	Interactions       []map[string]interface{} `json:"interactions,omitempty"`         // Full list when detail=["interactions"]

	// Mutation summary
	MutationCount   int                      `json:"mutation_count"`
	MutationsByType map[string]int           `json:"mutations_by_type,omitempty"` // e.g., {"added": 10, "modified": 5}
	RecentMutations []map[string]interface{} `json:"recent_mutations,omitempty"`  // Last N (default 5)
	Mutations       []map[string]interface{} `json:"mutations,omitempty"`         // Full list when detail=["mutations"]

	// Page dimensions (if available from client)
	PageHeight     int `json:"page_height,omitempty"`
	PageWidth      int `json:"page_width,omitempty"`
	ViewportHeight int `json:"viewport_height,omitempty"`
	ViewportWidth  int `json:"viewport_width,omitempty"`

	// Detail info
	DetailSections []string `json:"detail_sections,omitempty"` // Which sections have full detail
	DetailLimit    int      `json:"detail_limit,omitempty"`    // Limit applied to detailed sections
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

	// For list
	Count       int          `json:"count,omitempty"`
	Proxies     []ProxyEntry `json:"proxies,omitempty"`
	ProjectPath string       `json:"project_path,omitempty"`
	SessionCode string       `json:"session_code,omitempty"`
	Global      bool         `json:"global,omitempty"`

	// For stop/exec
	Success     bool   `json:"success,omitempty"`
	Message     string `json:"message,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"` // For exec action

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
	Action      string   `json:"action,omitempty" jsonschema:"Action: query, summary, clear, stats (default: query)"`
	Types       []string `json:"types,omitempty" jsonschema:"Filter by type: http, error, performance"`
	Methods     []string `json:"methods,omitempty" jsonschema:"Filter by HTTP method: GET, POST, etc."`
	URLPattern  string   `json:"url_pattern,omitempty" jsonschema:"URL substring to match"`
	StatusCodes []int    `json:"status_codes,omitempty" jsonschema:"Filter by HTTP status code"`
	Since       string   `json:"since,omitempty" jsonschema:"Start time (RFC3339 or duration like '5m')"`
	Until       string   `json:"until,omitempty" jsonschema:"End time (RFC3339)"`
	Limit       int      `json:"limit,omitempty" jsonschema:"Maximum results (default: 100)"`
	Detail      []string `json:"detail,omitempty" jsonschema:"For summary: sections to include full detail for (errors, http, performance, interactions, mutations)"`
	Raw         bool     `json:"raw,omitempty" jsonschema:"For query: return full raw data dumps instead of compact format (default: false)"`
}

// ProxyLogOutput defines output for proxylog tool.
type ProxyLogOutput struct {
	// For query
	Entries []LogEntryOutput `json:"entries,omitempty"`
	Count   int              `json:"count,omitempty"`

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

// RegisterProxyTools adds proxy-related MCP tools to the server.
func RegisterProxyTools(server *mcp.Server, pm *proxy.ProxyManager) {
	mcp.AddTool(server, &mcp.Tool{
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
  proxy {action: "start", id: "dev", target_url: "http://localhost:3000"}
  proxy {action: "status", id: "dev"}
  proxy {action: "list"}
  proxy {action: "exec", id: "dev", code: "document.title"}
  proxy {action: "stop", id: "dev"}

The proxy automatically:
  - Assigns a stable port based on the target URL (same URL always gets same port)
  - Logs all HTTP traffic (requests/responses)
  - Injects JavaScript to capture frontend errors
  - Captures performance metrics (page load, resources)
  - Provides WebSocket endpoint for metrics
  - Injects __devtool API with 50+ diagnostic functions

Port selection:
  - Default: A stable port derived from target URL hash (range 10000-60000)
  - Only specify 'port' if you need a specific port number
  - The assigned port is returned in the response's 'listen_addr' field

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

Output Format:
  - DEFAULT: Compact semi-structured text (easy to read, token-efficient)
    Example: "GET /api/users → 200 (45ms)"
  - With raw: true: Full JSON dumps (for programmatic processing)

Query Examples (compact format):
  proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"]}
  proxylog {proxy_id: "dev", types: ["error"]}
  proxylog {proxy_id: "dev", types: ["performance"]}
  proxylog {proxy_id: "dev", types: ["http"], since: "5m", limit: 50}

Query with Full Data (raw format):
  proxylog {proxy_id: "dev", types: ["error"], raw: true}
  proxylog {proxy_id: "dev", types: ["http"], raw: true, limit: 20}

Summary Examples (RECOMMENDED for first look):
  proxylog {proxy_id: "dev", action: "summary"}
  proxylog {proxy_id: "dev", action: "summary", detail: ["errors"], limit: 10}
  proxylog {proxy_id: "dev", action: "summary", detail: ["http", "errors"]}

Stats & Clear:
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

	// Use -1 to signal "use default" (hash-based port), 0 means auto-assign
	listenPort := input.Port
	if listenPort == 0 {
		listenPort = -1 // Trigger hash-based default in NewProxyServer
	}
	if input.MaxLogSize == 0 {
		input.MaxLogSize = 1000
	}

	config := proxy.ProxyConfig{
		ID:          input.ID,
		TargetURL:   input.TargetURL,
		ListenPort:  listenPort,
		MaxLogSize:  input.MaxLogSize,
		AutoRestart: true, // Enable auto-restart for development tool
		VerifyTLS:   input.VerifyTLS,
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

		// Handle large results saved to file
		if result.FilePath != "" {
			return nil, ProxyOutput{
				Success:     true,
				ExecutionID: execID,
				Message: fmt.Sprintf(`JavaScript executed successfully.
Result: Large response saved to file
File: %s
Duration: %v

Use the Read tool to view the full result.`, result.FilePath, result.Duration),
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

	// Use raw format (JSON dumps) if requested
	if input.Raw {
		return handleProxyLogQueryRaw(entries)
	}

	// Default: compact format
	return handleProxyLogQueryCompact(entries)
}

// handleProxyLogQueryRaw returns full JSON dumps of log entries
func handleProxyLogQueryRaw(entries []proxy.LogEntry) (*mcp.CallToolResult, ProxyLogOutput, error) {
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
		}
	}

	return nil, ProxyLogOutput{
		Entries: output,
		Count:   len(output),
	}, nil
}

// handleProxyLogQueryCompact returns compact semi-structured format (default)
func handleProxyLogQueryCompact(entries []proxy.LogEntry) (*mcp.CallToolResult, ProxyLogOutput, error) {
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
				attachmentInfo := ""
				if len(entry.PanelMessage.Attachments) > 0 {
					attachmentInfo = fmt.Sprintf(" [%d attachments]", len(entry.PanelMessage.Attachments))
				}
				data = fmt.Sprintf("%s%s", entry.PanelMessage.Message, attachmentInfo)
			}

		case proxy.LogTypeSketch:
			if entry.Sketch != nil {
				timestamp = entry.Sketch.Timestamp
				data = fmt.Sprintf("%s (%d elements) → %s",
					entry.Sketch.Description,
					entry.Sketch.ElementCount,
					entry.Sketch.FilePath)
			}

		default:
			// For other types, use basic string representation
			data = fmt.Sprintf("%s event", entry.Type)
		}

		output[i] = LogEntryOutput{
			Type:      string(entry.Type),
			Timestamp: timestamp,
			Data:      data,
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
	// Use lightweight summaries to avoid massive token usage
	summaries := proxyServer.PageTracker().GetActiveSessionSummaries()

	output := make([]PageSessionOutput, len(summaries))
	for i, summary := range summaries {
		output[i] = PageSessionOutput{
			ID:               summary.ID,
			URL:              summary.URL,
			PageTitle:        summary.PageTitle,
			StartTime:        summary.StartTime,
			LastActivity:     summary.LastActivity,
			Active:           summary.Active,
			ResourceCount:    summary.ResourceCount,
			ErrorCount:       summary.ErrorCount,
			HasPerformance:   summary.HasPerformance,
			LoadTime:         summary.LoadTimeMs,
			InteractionCount: summary.InteractionCount,
			MutationCount:    summary.MutationCount,
			// Note: No Resources, Errors, Interactions, or Mutations arrays
			// Use action="get" with specific session_id for full details
		}
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

	// Use raw format (full arrays) if requested, otherwise compact
	sessionOutput := convertPageSession(session, input.Raw)

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
		ID:               session.ID,
		URL:              session.URL,
		PageTitle:        session.PageTitle,
		StartTime:        session.StartTime,
		LastActivity:     session.LastActivity,
		Active:           session.Active,
		ResourceCount:    len(session.Resources),
		ErrorCount:       len(session.Errors),
		HasPerformance:   session.Performance != nil,
		InteractionCount: session.InteractionCount,
		MutationCount:    session.MutationCount,
	}

	if session.Performance != nil {
		output.LoadTime = session.Performance.LoadEventEnd
	}

	// Include detailed arrays only if requested (to avoid token bloat)
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

		// Add interaction details
		output.Interactions = make([]map[string]interface{}, len(session.Interactions))
		for i, interaction := range session.Interactions {
			intMap := map[string]interface{}{
				"id":         interaction.ID,
				"event_type": interaction.EventType,
				"timestamp":  interaction.Timestamp,
				"url":        interaction.URL,
			}
			if interaction.Target.Selector != "" {
				intMap["target"] = map[string]interface{}{
					"selector": interaction.Target.Selector,
					"tag":      interaction.Target.Tag,
					"id":       interaction.Target.ID,
					"text":     interaction.Target.Text,
				}
			}
			if interaction.Position != nil {
				intMap["position"] = map[string]interface{}{
					"client_x": interaction.Position.ClientX,
					"client_y": interaction.Position.ClientY,
				}
			}
			output.Interactions[i] = intMap
		}

		// Add mutation details
		output.Mutations = make([]map[string]interface{}, len(session.Mutations))
		for i, mutation := range session.Mutations {
			mutMap := map[string]interface{}{
				"id":            mutation.ID,
				"mutation_type": mutation.MutationType,
				"timestamp":     mutation.Timestamp,
				"url":           mutation.URL,
			}
			if mutation.Target.Selector != "" {
				mutMap["target"] = map[string]interface{}{
					"selector": mutation.Target.Selector,
					"tag":      mutation.Target.Tag,
					"id":       mutation.Target.ID,
				}
			}
			output.Mutations[i] = mutMap
		}
	}

	return output
}
