package protocol

import "encoding/json"

// Agnt-specific command verbs (beyond those in go-cli-server).
const (
	VerbProxy        = "PROXY"
	VerbProxyLog     = "PROXYLOG"
	VerbCurrentPage  = "CURRENTPAGE"
	VerbTunnel       = "TUNNEL"
	VerbBrowser      = "BROWSER"
	VerbAutomation   = "AUTOMATION" // chromedp-based browser automation sessions
	VerbChaos        = "CHAOS"
	VerbDetect       = "DETECT"
	VerbOverlay      = "OVERLAY"
	VerbStatus       = "STATUS" // Full daemon status (Hub's INFO is minimal)
	VerbStore        = "STORE"
	VerbAutomate     = "AUTOMATE"      // Agent-based automation processing
	VerbAlerts       = "ALERTS"        // Process output alert queries
	VerbScript       = "SCRIPT"        // Script registry queries and control
	VerbDoctor       = "DOCTOR"        // Health check / diagnostic report
	VerbAutostart    = "AUTOSTART"     // Resolve port conflicts and resume autostart
	VerbStreamEvents = "STREAM-EVENTS" // Long-lived event stream
	VerbHook         = "HOOK"          // Claude Code hook dispatcher enqueue
	VerbIncidents    = "INCIDENTS"     // Incident inbox query + mark-read
	VerbPorts        = "PORTS"         // Listening-port inventory + orphan pgid management
	VerbSessionHost  = "SESSION-HOST"  // Daemon-owned detachable PTY sessions (see docs/superpowers/specs/2026-07-03-remote-ssh-design.md §1)
	VerbScope        = "SCOPE"         // Resolve effective query scope
	VerbPublish      = "PUBLISH"       // Public walkthrough share-token lifecycle (control plane; see docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md)
	VerbShim         = "SHIM"          // Shell shim routing (EXEC) + install bookkeeping (REGISTER)
)

// DirectoryFilter preserves JSON presence for global while retaining the
// legacy Global bool for source-compatible `Global: true` callers.
type DirectoryFilter struct {
	SessionCode    string `json:"session_code,omitempty"`
	Directory      string `json:"directory,omitempty"`
	Global         bool   `json:"global,omitempty"`
	GlobalOverride *bool  `json:"-"`
}

// Bool returns a pointer suitable for optional boolean protocol fields.
func Bool(value bool) *bool { return &value }

func (f DirectoryFilter) GlobalSetting() (bool, bool) {
	if f.GlobalOverride != nil {
		return *f.GlobalOverride, true
	}
	if f.Global {
		return true, true
	}
	return false, false
}

func (f DirectoryFilter) MarshalJSON() ([]byte, error) {
	type wire struct {
		SessionCode string `json:"session_code,omitempty"`
		Directory   string `json:"directory,omitempty"`
		Global      *bool  `json:"global,omitempty"`
	}
	var global *bool
	if f.GlobalOverride != nil {
		global = f.GlobalOverride
	} else if f.Global {
		v := true
		global = &v
	}
	return json.Marshal(wire{f.SessionCode, f.Directory, global})
}

func (f *DirectoryFilter) UnmarshalJSON(data []byte) error {
	type wire struct {
		SessionCode string `json:"session_code,omitempty"`
		Directory   string `json:"directory,omitempty"`
		Global      *bool  `json:"global,omitempty"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	f.SessionCode, f.Directory, f.GlobalOverride = w.SessionCode, w.Directory, w.Global
	f.Global = w.Global != nil && *w.Global
	return nil
}

// Agnt-specific sub-verbs (beyond those in go-cli-server).
const (
	SubVerbExec           = "EXEC"
	SubVerbToast          = "TOAST"
	SubVerbQuery          = "QUERY"
	SubVerbSetForwards    = "SET-FORWARDS"
	SubVerbDeveloperEvent = "DEVELOPER-EVENT"
	SubVerbStats          = "STATS"
	SubVerbActivity       = "ACTIVITY"
	SubVerbOutputPreview  = "OUTPUT-PREVIEW"
	SubVerbForwarding     = "FORWARDING" // Pause/resume agent-inbound push for a session
	SubVerbEnable         = "ENABLE"
	SubVerbDisable        = "DISABLE"
	SubVerbAddRule        = "ADD-RULE"
	SubVerbRemoveRule     = "REMOVE-RULE"
	SubVerbListRules      = "LIST-RULES"
	SubVerbPreset         = "PRESET"
	SubVerbReset          = "RESET"
	SubVerbSend           = "SEND"
	SubVerbSchedule       = "SCHEDULE"
	SubVerbCancel         = "CANCEL"
	SubVerbTasks          = "TASKS"
	SubVerbFind           = "FIND"
	SubVerbAttach         = "ATTACH"
	SubVerbURL            = "URL"           // Report detected URL from agnt run session
	SubVerbGetAll         = "GET-ALL"       // Get all entries in a scope
	SubVerbDelete         = "DELETE"        // Delete an entry from a scope
	SubVerbRunGroup       = "RUN-GROUP"     // Launch a multi-process startup group with depends_on ordering
	SubVerbProcess        = "PROCESS"       // Process a single automation task
	SubVerbBatch          = "BATCH"         // Process multiple automation tasks
	SubVerbRestart        = "RESTART"       // Restart a process or proxy
	SubVerbScreenshot     = "SCREENSHOT"    // Take screenshot in automation session
	SubVerbNavigate       = "NAVIGATE"      // Navigate to URL in automation session
	SubVerbEvaluate       = "EVALUATE"      // Evaluate JavaScript in automation session
	SubVerbReport         = "REPORT"        // Report alert matches from agnt run
	SubVerbStartupLog     = "STARTUP-LOG"   // Query startup log (successes and failures)
	SubVerbClearPorts     = "CLEAR-PORTS"   // Kill port blockers and resume autostart
	SubVerbContinue       = "CONTINUE"      // Resume autostart without killing blockers
	SubVerbAutostartRun   = "RUN"           // Run autostart from MCP InitializedHandler (non-interactive)
	SubVerbReconcile      = "RECONCILE"     // Live-apply .agnt.kdl changes to running scripts/proxies
	SubVerbCleanOrphans   = "CLEAN-ORPHANS" // Reap orphaned process groups (PORTS verb)
	SubVerbCreate         = "CREATE"        // Create a session-host session (SESSION-HOST verb)
	SubVerbKill           = "KILL"          // Explicit termination of a session-host session
	SubVerbDetach         = "DETACH"        // Client-initiated clean detach (SESSION-HOST ATTACH stream)
	SubVerbResize         = "RESIZE"        // Out-of-band window-size renegotiation (SESSION-HOST)
	SubVerbResolve        = "RESOLVE"       // Resolve configured query scope (SCOPE verb)
	SubVerbRevoke         = "REVOKE"        // Tombstone a share (PUBLISH)
	SubVerbRotate         = "ROTATE"        // Mint a fresh token, kill the old (PUBLISH)
	SubVerbFeedback       = "FEEDBACK"      // Read owner-scoped feedback rows for a share (PUBLISH)
)

type ForwardMapping struct {
	ProxyID    string `json:"proxy_id"`
	RemotePort int    `json:"remote_port"`
	LocalPort  int    `json:"local_port"`
}

type ForwardSet struct {
	Source   string           `json:"source"`
	Mappings []ForwardMapping `json:"mappings"`
}

type DeveloperEvent struct {
	Kind        string `json:"kind"`
	ProxyID     string `json:"proxy_id,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	Severity    string `json:"severity"`
}

// ProxyStartConfig represents configuration for a PROXY START command.
type ProxyStartConfig struct {
	ID          string        `json:"id"`
	TargetURL   string        `json:"target_url"`
	Port        int           `json:"port"`
	MaxLogSize  int           `json:"max_log_size,omitempty"`
	BindAddress string        `json:"bind_address,omitempty"` // "127.0.0.1" (default) or "0.0.0.0" (all interfaces)
	PublicURL   string        `json:"public_url,omitempty"`   // Public URL for tunnels (e.g., "https://abc.trycloudflare.com")
	Tunnel      *TunnelConfig `json:"tunnel,omitempty"`
}

// TunnelConfig represents configuration for starting a tunnel alongside a proxy.
type TunnelConfig struct {
	// Provider is the tunnel provider: "ngrok", "cloudflared", "tailscale", or "custom"
	Provider string `json:"provider"`
	// Command is used when Provider is "custom" - the full command to run
	Command string `json:"command,omitempty"`
	// Args are additional arguments for the tunnel command
	Args []string `json:"args,omitempty"`
	// AuthToken is the authentication token (for ngrok)
	AuthToken string `json:"auth_token,omitempty"`
	// Region is the tunnel region (optional)
	Region string `json:"region,omitempty"`
}

// LogQueryFilter represents filters for PROXYLOG QUERY command.
type LogQueryFilter struct {
	Types       []string `json:"types,omitempty"`
	Methods     []string `json:"methods,omitempty"`
	URLPattern  string   `json:"url_pattern,omitempty"`
	StatusCodes []int    `json:"status_codes,omitempty"`
	Since       string   `json:"since,omitempty"`
	Until       string   `json:"until,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	// ErrorsOnly filters to error-class entries (HTTP 4xx/5xx, JS errors,
	// error-level diagnostics) across all sources.
	ErrorsOnly bool `json:"errors_only,omitempty"`
	// DiagnosticLevels filters diagnostic entries by level (info, warning, error).
	DiagnosticLevels []string `json:"diagnostic_levels,omitempty"`
	// InteractionTypes filters interaction entries by event type (click, keydown, scroll, ...).
	InteractionTypes []string `json:"interaction_types,omitempty"`
	// MutationTypes filters mutation entries by mutation type (added, removed, attributes, ...).
	MutationTypes []string `json:"mutation_types,omitempty"`
	// Frames restricts to entries emitted by these content-frame ids.
	Frames []string `json:"frames,omitempty"`
	// MessagePattern restricts to message-bearing entries (error/custom/diagnostic)
	// whose message contains this substring.
	MessagePattern string `json:"message_pattern,omitempty"`
	// MinDurationMs restricts to HTTP entries at or above this round-trip duration (ms).
	MinDurationMs int64 `json:"min_duration_ms,omitempty"`
	// OmitBodies asks the daemon to strip HTTP bodies/headers from the result
	// before sending it (used by the summary path, which never reads them).
	OmitBodies bool `json:"omit_bodies,omitempty"`
}

// ToastConfig represents configuration for a PROXY TOAST command.
type ToastConfig struct {
	Type     string `json:"type"`               // success, error, warning, info
	Title    string `json:"title,omitempty"`    // Toast title (optional)
	Message  string `json:"message"`            // Toast message
	Duration int    `json:"duration,omitempty"` // Duration in ms (0 for default)
	// Input requests an interactive field on the toast. The only supported
	// mode is "secret": the browser renders a masked password field whose
	// submitted value is routed straight to the daemon store — it never
	// enters the channel/panel event stream.
	Input string `json:"input,omitempty"`
	// Name is the env-var-style secret name (e.g. FIGMA_KEY). Required when
	// Input is "secret".
	Name string `json:"name,omitempty"`
}

// TunnelStartConfig represents configuration for a TUNNEL START command.
type TunnelStartConfig struct {
	ID         string `json:"id"`                    // Tunnel ID (usually same as proxy ID)
	Provider   string `json:"provider"`              // "cloudflare" or "ngrok"
	LocalPort  int    `json:"local_port"`            // Local port to tunnel
	LocalHost  string `json:"local_host,omitempty"`  // Local host (default: localhost)
	BinaryPath string `json:"binary_path,omitempty"` // Optional path to tunnel binary
	ProxyID    string `json:"proxy_id,omitempty"`    // Optional proxy ID to auto-configure public_url
}

// ChaosRuleConfig represents configuration for a CHAOS ADD-RULE command.
type ChaosRuleConfig struct {
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

// ChaosConfigPayload represents the full chaos configuration for SET command.
type ChaosConfigPayload struct {
	Enabled     bool               `json:"enabled"`
	Rules       []*ChaosRuleConfig `json:"rules,omitempty"`
	GlobalOdds  float64            `json:"global_odds,omitempty"`  // 0.0-1.0
	Seed        int64              `json:"seed,omitempty"`         // For reproducible chaos
	LoggingMode int                `json:"logging_mode,omitempty"` // 0=silent, 1=testing, 2=coordinated
}

// SessionRegisterConfig represents configuration for a SESSION REGISTER command.
// This extends the base hub SessionRegisterConfig with agnt-specific fields.
type SessionRegisterConfig struct {
	OverlayPath      string   `json:"overlay_path"`                 // Unix socket path for overlay
	ProjectPath      string   `json:"project_path"`                 // Directory where session was started
	Command          string   `json:"command"`                      // Command being run (e.g., "claude")
	Args             []string `json:"args,omitempty"`               // Command arguments
	SessionPGID      int      `json:"session_pgid,omitempty"`       // POSIX process group ID of the PTY child (session leader); 0 on Windows or when unavailable
	SessionJobHandle uint64   `json:"session_job_handle,omitempty"` // Windows Job Object handle for the PTY child subtree; 0 on Unix or when unavailable. Handles are per-process on Windows — this is a best-effort hint for daemon cleanup, not a stable cross-process identifier.
	OwnerPID         int      `json:"owner_pid,omitempty"`          // PID of the agnt wrapper that owns the classic session. Disconnect cleanup may reap the child tree only after this process is confirmed gone.
}

// SessionScheduleConfig represents configuration for a SESSION SCHEDULE command.
type SessionScheduleConfig struct {
	SessionCode string `json:"session_code"` // Target session
	Duration    string `json:"duration"`     // Go duration string (e.g., "5m", "1h30m")
	Message     string `json:"message"`      // Message to deliver
	ProjectPath string `json:"project_path"` // For project-scoped storage
}

// StoreGetRequest represents a STORE GET command.
type StoreGetRequest struct {
	Scope    string `json:"scope"`
	ScopeKey string `json:"scope_key"`
	Key      string `json:"key"`
}

// StoreSetRequest represents a STORE SET command.
type StoreSetRequest struct {
	Scope    string         `json:"scope"`
	ScopeKey string         `json:"scope_key"`
	Key      string         `json:"key"`
	Value    interface{}    `json:"value"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// StoreDeleteRequest represents a STORE DELETE command.
type StoreDeleteRequest struct {
	Scope    string `json:"scope"`
	ScopeKey string `json:"scope_key"`
	Key      string `json:"key"`
}

// StoreListRequest represents a STORE LIST command.
type StoreListRequest struct {
	Scope    string `json:"scope"`
	ScopeKey string `json:"scope_key"`
}

// StoreClearRequest represents a STORE CLEAR command.
type StoreClearRequest struct {
	Scope    string `json:"scope"`
	ScopeKey string `json:"scope_key"`
}

// StoreGetAllRequest represents a STORE GET_ALL command.
type StoreGetAllRequest struct {
	Scope    string `json:"scope"`
	ScopeKey string `json:"scope_key"`
}

// AutomateProcessRequest represents an AUTOMATE PROCESS command.
type AutomateProcessRequest struct {
	Type    string                 `json:"type"`    // Task type (audit_process, summarize, etc.)
	Data    map[string]interface{} `json:"data"`    // Task-specific input data
	Context map[string]interface{} `json:"context"` // Additional context
	Options AutomateOptions        `json:"options,omitempty"`
}

// AutomateOptions configures automation task processing.
type AutomateOptions struct {
	Model       string  `json:"model,omitempty"`       // Model to use (haiku, sonnet)
	MaxTokens   int     `json:"max_tokens,omitempty"`  // Max response tokens
	Temperature float64 `json:"temperature,omitempty"` // 0.0-1.0
}

// AutomateBatchRequest represents an AUTOMATE BATCH command.
type AutomateBatchRequest struct {
	Tasks []AutomateProcessRequest `json:"tasks"`
}

// AutomateProcessResponse represents the response from AUTOMATE PROCESS.
type AutomateProcessResponse struct {
	Success  bool        `json:"success"`
	Result   interface{} `json:"result,omitempty"`
	Error    string      `json:"error,omitempty"`
	Tokens   int         `json:"tokens_used"`
	CostUSD  float64     `json:"cost_usd"`
	Duration string      `json:"duration"`
}

// BrowserStartConfig represents configuration for a BROWSER START command.
type BrowserStartConfig struct {
	ID         string `json:"id,omitempty"`          // Browser ID (auto-generated if empty)
	URL        string `json:"url,omitempty"`         // URL to open
	ProxyID    string `json:"proxy_id,omitempty"`    // Proxy to use (auto-starts if needed)
	Headless   *bool  `json:"headless,omitempty"`    // Default: true
	BinaryPath string `json:"binary_path,omitempty"` // Optional Chrome path
}

// AutomationStartConfig represents configuration for an AUTOMATION START command.
type AutomationStartConfig struct {
	ID       string `json:"id,omitempty"`       // Session ID (auto-generated if empty)
	URL      string `json:"url,omitempty"`      // URL to open
	ProxyID  string `json:"proxy_id,omitempty"` // Proxy to use
	Headless *bool  `json:"headless,omitempty"` // Default: true
}

// AutomationScreenshotConfig represents configuration for an AUTOMATION SCREENSHOT command.
type AutomationScreenshotConfig struct {
	SessionID string  `json:"session_id"`         // Session ID (required)
	Type      string  `json:"type,omitempty"`     // viewport, fullpage, element, clip
	Label     string  `json:"label,omitempty"`    // Optional label for filename
	Selector  string  `json:"selector,omitempty"` // CSS selector for element type
	Viewport  string  `json:"viewport,omitempty"` // Viewport preset name
	X         float64 `json:"x,omitempty"`        // Clip X
	Y         float64 `json:"y,omitempty"`        // Clip Y
	Width     float64 `json:"width,omitempty"`    // Clip width
	Height    float64 `json:"height,omitempty"`   // Clip height
}

// AutomationNavigateConfig represents configuration for an AUTOMATION NAVIGATE command.
type AutomationNavigateConfig struct {
	SessionID string `json:"session_id"` // Session ID (required)
	URL       string `json:"url"`        // URL to navigate to (required)
}

// AutomationEvaluateConfig represents configuration for an AUTOMATION EVALUATE command.
type AutomationEvaluateConfig struct {
	SessionID string `json:"session_id"` // Session ID (required)
	Script    string `json:"script"`     // JavaScript to evaluate (required)
}

// AlertReportPayload is sent from agnt run to daemon when alert patterns match.
type AlertReportPayload struct {
	PatternID   string `json:"pattern_id"`
	Severity    string `json:"severity"`    // "error", "warning", "info"
	Category    string `json:"category"`    // "go", "dotnet", "webpack", "generic", "custom"
	Description string `json:"description"` // Pattern description
	Line        string `json:"line"`        // Matched output line
	ScriptID    string `json:"script_id"`   // Process that produced the line
	ProjectPath string `json:"project_path,omitempty"`
	Timestamp   string `json:"timestamp"` // RFC3339
}

// AlertPinPayload addresses one error for ALERTS PIN / UNPIN. ID is the
// unified-error id (alert store) or incident fingerprint the agent saw in a
// prior get_errors/get_incidents result. Scoping fields mirror
// AlertQueryFilter (session-scope chokepoint; the MCP connection names the
// project explicitly).
type AlertPinPayload struct {
	ID  string `json:"id"`
	Tag string `json:"tag,omitempty"` // agent note stored with the pin

	SessionCode string `json:"session_code,omitempty"`
	Directory   string `json:"directory,omitempty"`
}

// AlertClearFilter scopes ALERTS CLEAR. By default the clear is bounded to
// the caller's project; ProcessID narrows it to one process; Global clears
// every project (audited loud path). Pinned errors are never cleared.
type AlertClearFilter struct {
	ProcessID string `json:"process_id,omitempty"`

	Global      *bool  `json:"global,omitempty"`
	SessionCode string `json:"session_code,omitempty"`
	Directory   string `json:"directory,omitempty"`
}

// AlertQueryFilter filters for ALERTS QUERY.
//
// Scoping fields (Global / SessionCode / Directory) feed the session-scope
// chokepoint on the daemon side: by default the query is scoped to the
// caller's project; Global bypasses the scope (cross-project); SessionCode /
// Directory let an MCP client that is not session-bound on its daemon
// connection name the project explicitly (mirrors DirectoryFilter).
type AlertQueryFilter struct {
	Since     string `json:"since,omitempty"`      // RFC3339 or duration like "5m"
	ProcessID string `json:"process_id,omitempty"` // Filter to specific process
	Severity  string `json:"severity,omitempty"`   // Filter by severity
	Limit     int    `json:"limit,omitempty"`      // Max results (0 = all)

	Global      *bool  `json:"global,omitempty"`       // Explicit session-scope override
	SessionCode string `json:"session_code,omitempty"` // Explicit session to scope to
	Directory   string `json:"directory,omitempty"`    // Explicit project directory to scope to
}

// StreamEventFilter filters events for STREAM-EVENTS command.
type StreamEventFilter struct {
	Types          []string `json:"types,omitempty"`        // Log entry types: error, http, panel_message, process, etc.
	ProxyID        string   `json:"proxy_id,omitempty"`     // Filter to specific proxy
	ProjectPath    string   `json:"project_path,omitempty"` // Legacy alias for Directory
	SessionCode    string   `json:"session_code,omitempty"` // Explicit session to scope to
	Directory      string   `json:"directory,omitempty"`    // Explicit project directory to scope to
	Global         bool     `json:"global,omitempty"`       // Legacy source-compatible audited cross-project subscription
	GlobalOverride *bool    `json:"-"`                      // Presence-bearing per-call scope override
	ProcessID      string   `json:"process_id,omitempty"`   // Filter to specific process output
	Severity       string   `json:"severity,omitempty"`     // Filter by severity: error, warning, info
	Grep           string   `json:"grep,omitempty"`         // Substring match on process output lines
	GrepStream     string   `json:"grep_stream,omitempty"`  // Filter process output stream: "stdout" or "stderr"
}

// GlobalSetting reports both the requested value and whether the caller
// explicitly supplied it. The distinction lets project default-global apply
// only when the STREAM-EVENTS request omitted the field.
func (f StreamEventFilter) GlobalSetting() (bool, bool) {
	if f.GlobalOverride != nil {
		return *f.GlobalOverride, true
	}
	if f.Global {
		return true, true
	}
	return false, false
}

func (f StreamEventFilter) MarshalJSON() ([]byte, error) {
	type wire struct {
		Types       []string `json:"types,omitempty"`
		ProxyID     string   `json:"proxy_id,omitempty"`
		ProjectPath string   `json:"project_path,omitempty"`
		SessionCode string   `json:"session_code,omitempty"`
		Directory   string   `json:"directory,omitempty"`
		Global      *bool    `json:"global,omitempty"`
		ProcessID   string   `json:"process_id,omitempty"`
		Severity    string   `json:"severity,omitempty"`
		Grep        string   `json:"grep,omitempty"`
		GrepStream  string   `json:"grep_stream,omitempty"`
	}
	var global *bool
	if f.GlobalOverride != nil {
		global = f.GlobalOverride
	} else if f.Global {
		global = Bool(true)
	}
	return json.Marshal(wire{
		Types: f.Types, ProxyID: f.ProxyID, ProjectPath: f.ProjectPath,
		SessionCode: f.SessionCode, Directory: f.Directory, Global: global,
		ProcessID: f.ProcessID, Severity: f.Severity, Grep: f.Grep, GrepStream: f.GrepStream,
	})
}

func (f *StreamEventFilter) UnmarshalJSON(data []byte) error {
	type wire struct {
		Types       []string `json:"types,omitempty"`
		ProxyID     string   `json:"proxy_id,omitempty"`
		ProjectPath string   `json:"project_path,omitempty"`
		SessionCode string   `json:"session_code,omitempty"`
		Directory   string   `json:"directory,omitempty"`
		Global      *bool    `json:"global,omitempty"`
		ProcessID   string   `json:"process_id,omitempty"`
		Severity    string   `json:"severity,omitempty"`
		Grep        string   `json:"grep,omitempty"`
		GrepStream  string   `json:"grep_stream,omitempty"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*f = StreamEventFilter{
		Types: w.Types, ProxyID: w.ProxyID, ProjectPath: w.ProjectPath,
		SessionCode: w.SessionCode, Directory: w.Directory, GlobalOverride: w.Global,
		ProcessID: w.ProcessID, Severity: w.Severity, Grep: w.Grep, GrepStream: w.GrepStream,
	}
	f.Global = w.Global != nil && *w.Global
	return nil
}

// HookPayload is sent from the `agnt hook` CLI dispatcher to the daemon when
// a Claude Code (or other agent) hook fires. The wire shape is intentionally
// tiny so the CLI can enqueue-and-exit in ~fork+write+ack time: the hook is
// synchronous from Claude's perspective and any added latency is directly
// visible to the user.
//
// `Event` is the hook name (PreToolUse, Notification, Stop, …). `Payload` is
// the raw JSON blob Claude Code wrote to stdin, preserved verbatim so
// downstream consumers can read any future fields without the CLI knowing
// about them. `Tags` carries caller-provided key/value context — project
// path, agent id, session code — so the daemon can filter and route without
// re-parsing the opaque payload.
type HookPayload struct {
	Event   string            `json:"event"`
	Payload json.RawMessage   `json:"payload,omitempty"`
	Tags    map[string]string `json:"tags,omitempty"`
}

// ShimExecRequest is the SHIM EXEC payload sent by `agnt shim exec` from
// inside a managed shell. Cwd is the shell's working directory at
// invocation time (may differ from ProjectPath for monorepo subdirs).
type ShimExecRequest struct {
	ProjectPath string   `json:"project_path"`
	SessionCode string   `json:"session_code,omitempty"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
}

// ShimExecResponse is the daemon's routing decision. Action "handled"
// means the daemon ran (or reused) a managed process and ExitCode/Output
// carry the result; "passthrough" tells the shim to exec the real binary;
// "blocked" refuses the command. Message is printed to stderr by the shim,
// Output to stdout. ToolHint names the MCP tool the agent could have used
// directly.
type ShimExecResponse struct {
	Action   string `json:"action"` // handled | passthrough | blocked
	ExitCode int    `json:"exit_code"`
	Message  string `json:"message,omitempty"`
	Output   string `json:"output,omitempty"`
	ToolHint string `json:"tool_hint,omitempty"`
}

// ShimRegisterRequest is the SHIM REGISTER payload sent after a client
// installs a project's shim bin dir, so the daemon can track liveness and
// clean up on shutdown/session end.
type ShimRegisterRequest struct {
	ProjectPath string `json:"project_path"`
	BinDir      string `json:"bin_dir"`
	SessionCode string `json:"session_code,omitempty"`
}

// AutostartRunConfig is the JSON payload for AUTOSTART RUN. Sent from the MCP
// InitializedHandler to trigger project autostart in non-interactive (channel)
// mode where there is no PTY session. When NonInteractive is true, the
// port-conflict "prompt" policy falls back to "skip" with a warning because
// there is no stdin to present the prompt to.
type AutostartRunConfig struct {
	ProjectPath    string `json:"project_path"`
	NonInteractive bool   `json:"non_interactive"`
}

// IncidentQueryFilter is the request payload for INCIDENTS QUERY.
type IncidentQueryFilter struct {
	Severities   []string `json:"severities,omitempty"`   // critical/error/warning/info; empty = all
	Since        string   `json:"since,omitempty"`        // RFC3339 timestamp for lower bound
	Fingerprints []string `json:"fingerprints,omitempty"` // limit to specific fps
	Sources      []string `json:"sources,omitempty"`      // filter by Source enum values
	ProxyID      string   `json:"proxy_id,omitempty"`
	ProcessID    string   `json:"process_id,omitempty"`
	Detail       string   `json:"detail,omitempty"` // "summary" (default) | "full"
	MarkRead     bool     `json:"mark_read,omitempty"`
	Limit        int      `json:"limit,omitempty"` // 0 → 20; max 100
}

// IncidentQueryResult is the response payload for INCIDENTS QUERY.
type IncidentQueryResult struct {
	Incidents  []IncidentRecord `json:"incidents"`
	InboxStats InboxStatsRecord `json:"inbox_stats"`
	Cursor     string           `json:"cursor,omitempty"` // RFC3339 of last-seen for resumable pulls
	Truncated  bool             `json:"truncated"`
	// PipelineEnabled reports whether the caller currently has a registered
	// session inbox. It distinguishes an empty inbox from an unavailable session
	// pipeline (for example during teardown); project config never disables it.
	// get_errors uses this to decide whether the inbox shim is available.
	PipelineEnabled bool `json:"pipeline_enabled"`
}

// IncidentRecord is the wire shape for a single incident in a query result.
type IncidentRecord struct {
	ID          string              `json:"id"`
	Fingerprint string              `json:"fingerprint"`
	FirstSeen   string              `json:"first_seen"` // RFC3339
	LastSeen    string              `json:"last_seen"`  // RFC3339
	Count       int                 `json:"count"`
	Severity    string              `json:"severity"`
	Source      string              `json:"source"`
	Category    string              `json:"category,omitempty"`
	Summary     string              `json:"summary,omitempty"`
	Payload     *string             `json:"payload,omitempty"` // set when detail="full"
	Context     IncidentContext     `json:"context,omitempty"`
	Remediation IncidentRemediation `json:"remediation,omitempty"`
	Read        bool                `json:"read"`
}

// IncidentContext is the wire shape for incident context fields.
type IncidentContext struct {
	ProcessID   string `json:"process_id,omitempty"`
	ProxyID     string `json:"proxy_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	URL         string `json:"url,omitempty"`
	PID         int    `json:"pid,omitempty"`
	Port        int    `json:"port,omitempty"`
}

// IncidentRemediation is the wire shape for remediation hints.
type IncidentRemediation struct {
	PrimaryTool  string         `json:"primary_tool,omitempty"`
	PrimaryArgs  map[string]any `json:"primary_args,omitempty"`
	FallbackTool string         `json:"fallback_tool,omitempty"`
	SkillHint    string         `json:"skill_hint,omitempty"`
}

// SessionHostCreateConfig is the payload for SESSION-HOST CREATE. See
// docs/superpowers/specs/2026-07-03-remote-ssh-design.md §1.2.
type SessionHostCreateConfig struct {
	Name        string            `json:"name,omitempty"`
	ProjectPath string            `json:"project_path"`
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	Cols        int               `json:"cols"`
	Rows        int               `json:"rows"`
	Env         map[string]string `json:"env,omitempty"`
}

// SessionHostCreateResult is the response to SESSION-HOST CREATE.
type SessionHostCreateResult struct {
	SessionID   string `json:"session_id"`
	SessionPGID int    `json:"session_pgid"`
}

// SessionHostResizeConfig is the payload for a "resize" frame on SESSION-HOST
// ATTACH, and for the SESSION-HOST RESIZE convenience command.
type SessionHostResizeConfig struct {
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// PublishCreateRequest is the payload for PUBLISH CREATE. The walkthrough is
// carried as raw JSON so this low-level protocol package stays decoupled from
// internal/publish; the daemon handler decodes + validates it through the P2
// validators before it is ever stored.
type PublishCreateRequest struct {
	Walkthrough json.RawMessage `json:"walkthrough"`
}

// PublishCreateResult is the response to PUBLISH CREATE. Token is the plaintext
// share token, returned exactly ONCE here (never persisted, never in STATUS) —
// per the P1 security spec §3. ID is the viewer-safe share id used by the other
// control-plane verbs; ShareURL is the public path bearing the token.
type PublishCreateResult struct {
	ID       string `json:"id"`
	Token    string `json:"token"`
	ShareURL string `json:"share_url"`
	Digest   string `json:"digest"`
}

// PublishRotateRequest is the payload for PUBLISH ROTATE.
type PublishRotateRequest struct {
	ID string `json:"id"`
}

// PublishRotateResult is the response to PUBLISH ROTATE. Token is the fresh
// plaintext token, returned once; the prior token is dead immediately.
type PublishRotateResult struct {
	ID       string `json:"id"`
	Token    string `json:"token"`
	ShareURL string `json:"share_url"`
}

// PublishRevokeRequest is the payload for PUBLISH REVOKE.
type PublishRevokeRequest struct {
	ID string `json:"id"`
}

// PublishStatusRequest is the payload for PUBLISH STATUS.
type PublishStatusRequest struct {
	ID string `json:"id"`
}

// PublishShareInfo is the redaction-safe wire view of a share. It NEVER carries
// the plaintext token — only a short hash prefix for correlation (INV-9).
type PublishShareInfo struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Steps           int    `json:"steps"`
	Digest          string `json:"digest"`
	TokenHashPrefix string `json:"token_hash_prefix"`
	Revoked         bool   `json:"revoked"`
	CreatedAt       string `json:"created_at"`
	RotatedAt       string `json:"rotated_at,omitempty"`
	RevokedAt       string `json:"revoked_at,omitempty"`
}

// PublishListResult is the response to PUBLISH LIST.
type PublishListResult struct {
	Shares []PublishShareInfo `json:"shares"`
}

// PublishFeedbackRequest is the payload for PUBLISH FEEDBACK — the owner-scoped
// control-plane read of anonymous viewer feedback for a share (spec §2a
// "publish feedback list"). ID is the viewer-safe share id (ownership is
// enforced daemon-side, exactly like status/revoke/rotate). Cursor + Limit
// paginate the append-ordered rows.
type PublishFeedbackRequest struct {
	ID     string `json:"id"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// PublishFeedbackRow is one redaction-safe feedback row on the wire. It carries
// NO share token (feedback records never hold one — redaction is structural).
// Body is the raw, inert viewer payload (spec §7 / INV-7): it is DATA, never a
// command, and any HTML consumer MUST escape it before rendering.
type PublishFeedbackRow struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
}

// PublishFeedbackResult is the response to PUBLISH FEEDBACK. Rows are the page;
// NextCursor is "" at the end of the ring. Total is the share's stored row count
// and Dropped is the cumulative rate-limit-shed count — the observability signal
// (spec §5) that the public limiter is throttling abuse. NO token appears here.
type PublishFeedbackResult struct {
	Rows       []PublishFeedbackRow `json:"rows"`
	NextCursor string               `json:"next_cursor,omitempty"`
	Total      int                  `json:"total"`
	Dropped    int64                `json:"dropped"`
}

// InboxStatsRecord is the wire shape for inbox health summary.
type InboxStatsRecord struct {
	Critical int   `json:"critical"`
	Error    int   `json:"error"`
	Warning  int   `json:"warning"`
	Info     int   `json:"info"`
	Dropped  int64 `json:"dropped"`
	New      int   `json:"new"` // unread entries
}
