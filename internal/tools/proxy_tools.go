package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/go-sdk/mcp"
)

// handleExecSearch resolves a proxy `exec` search request, which needs no proxy
// id and is mutually exclusive with code execution. It returns handled=true when
// the input is a search/category request (so the caller should return its
// result), or false to fall through to normal JavaScript execution. Shared by
// the daemon exec handler and the exec-search unit tests.
func handleExecSearch(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, bool) {
	if input.Search == "" && input.Category == "" {
		return nil, ProxyOutput{}, false
	}
	if input.Code != "" {
		return errorResult("cannot combine 'search' with 'code'; run search first, then exec with the resolved function"), ProxyOutput{}, true
	}
	result := SearchAPIFunctions(input.Search, input.Category)
	return nil, ProxyOutput{Success: true, SearchResult: &result}, true
}

// resolveExecTarget maps the high-level Target/frame_id selector onto the wire
// token understood by ProxyServer.ExecuteJavaScript / core.js. "outer" addresses
// the chrome shell ("@chrome"); "inner" (or empty) lets the server pick the
// active content frame; an explicit frame_id is passed through. Target wins over
// frame_id when both are set.
//
// An empty target defers to the frame_id param (the dedicated way to name an
// explicit frame). A non-empty target must be a known selector or a frame-id-
// shaped token; anything else (a typo like "page") is rejected rather than
// silently forwarded as a frame id, which previously produced a misleading exec
// timeout. The token vocabulary is the single source of truth in
// proxy.ClassifyExecTarget, shared with the server-side resolver.
func resolveExecTarget(target, frameID string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return frameID, nil
	}
	switch proxy.ClassifyExecTarget(target) {
	case proxy.ExecTargetChrome:
		return "@chrome", nil
	case proxy.ExecTargetActiveFrame:
		return "", nil
	case proxy.ExecTargetFrameID:
		return target, nil
	default:
		return "", fmt.Errorf("unknown target %q; use 'inner' (active content frame), 'outer' (chrome shell), or pass an explicit frame_id", target)
	}
}

// jsStringLiteral renders s as a safe JavaScript string literal (quotes + escapes
// via JSON, which is a valid JS expression).
func jsStringLiteral(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return "\"\""
	}
	return string(b)
}

// buildNavigateJS produces the JS that drives a page navigation from inside the
// content frame. The actual navigation is deferred to a microtask so the exec
// reply is sent BEFORE the page (and its WebSocket) goes away — otherwise a
// reload/goto would drop the socket before the result returned and the call would
// look like a timeout.
func buildNavigateJS(direction, url string) (string, error) {
	var nav string
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "back":
		nav = "history.back();"
	case "forward":
		nav = "history.forward();"
	case "reload", "refresh":
		nav = "location.reload();"
	case "goto", "":
		if strings.TrimSpace(url) == "" {
			return "", fmt.Errorf("navigate goto requires target_url")
		}
		nav = "location.assign(" + jsStringLiteral(url) + ");"
	default:
		return "", fmt.Errorf("unknown direction %q (use back, forward, reload, goto)", direction)
	}
	return "(function(){var h=location.href;setTimeout(function(){" + nav +
		"},0);return {navigating:true,from:h};})()", nil
}

// buildResizeJS produces the JS (run in the outer shell) that resizes the live
// content frame in place via the shell helper installed by frames.js.
func buildResizeJS(width, height int) string {
	return fmt.Sprintf("(function(){return window.__devtool_resize_content?"+
		"window.__devtool_resize_content(%d,%d):"+
		"{ok:false,error:'resize requires the chrome shell (always-wrap)'};})()", width, height)
}

// ProxyInput defines input for the proxy tool.
type ProxyInput struct {
	Action        string `json:"action" jsonschema:"Action: start, stop, restart, status, list, exec, navigate, resize, toast, chaos"`
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
	Target        string `json:"target,omitempty" jsonschema:"For exec/navigate: which frame in the always-wrap model. 'inner' (default) = the active page content frame; 'outer' = the chrome shell that hosts the page (the proxy UI runtime). Use 'outer' to run a REPL against the shell, 'inner' to script the page."`
	Direction     string `json:"direction,omitempty" jsonschema:"For navigate: 'back', 'forward', 'reload', or 'goto' (with target_url). Drives the page content frame."`
	Width         int    `json:"width,omitempty" jsonschema:"For resize: content-frame viewport width in px (0 resets to full width). Resize is observed from the outer shell; the page reflows in place with no reload, then audits can measure the resized viewport."`
	Height        int    `json:"height,omitempty" jsonschema:"For resize: content-frame viewport height in px (0 = full height)."`
	Global        *bool  `json:"global,omitempty" jsonschema:"For list: override project config; true includes all projects, false forces current project"`
	Help          bool   `json:"help,omitempty" jsonschema:"For exec: show __devtool API overview instead of executing code"`
	Describe      string `json:"describe,omitempty" jsonschema:"For exec: show detailed docs for a specific function (e.g. 'screenshot', 'interactions.getLastClick')"`
	Search        string `json:"search,omitempty" jsonschema:"For exec: case-insensitive substring search across __devtool function names, descriptions, and signatures. Returns up to 10 compact matches. Combine with category to narrow results."`
	Category      string `json:"category,omitempty" jsonschema:"For exec search: optional category filter (e.g. 'accessibility', 'layout', 'inspection'). AND-combined with 'search'."`
	Hints         *bool  `json:"hints,omitempty" jsonschema:"For exec: scan submitted JS for raw DOM patterns and suggest __devtool helpers. Default: true. Set false to opt out."`
	ToastType     string `json:"toast_type,omitempty" jsonschema:"For toast: notification type (success, error, warning, info). Default: info"`
	ToastTitle    string `json:"toast_title,omitempty" jsonschema:"For toast: notification title (optional)"`
	ToastMessage  string `json:"toast_message,omitempty" jsonschema:"For toast: notification message (required for toast)"`
	ToastDuration int    `json:"toast_duration,omitempty" jsonschema:"For toast: duration in milliseconds (0 for default)"`

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
