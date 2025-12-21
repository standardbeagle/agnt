// Package protocol defines the text-based IPC protocol for daemon communication.
package protocol

// Command represents a parsed command from the client.
type Command struct {
	Verb        string   // Primary command verb (RUN, PROC, PROXY, etc.)
	SubVerb     string   // Optional sub-verb (STATUS, OUTPUT, START, etc.)
	Args        []string // Positional arguments
	Data        []byte   // Optional binary/JSON data payload
	SessionCode string   // Session code for scoping (required unless Global flag is set)
}

// Command verbs
const (
	VerbRun         = "RUN"
	VerbRunJSON     = "RUN-JSON"
	VerbProc        = "PROC"
	VerbProxy       = "PROXY"
	VerbProxyLog    = "PROXYLOG"
	VerbCurrentPage = "CURRENTPAGE"
	VerbTunnel      = "TUNNEL"
	VerbChaos       = "CHAOS"
	VerbDetect      = "DETECT"
	VerbOverlay     = "OVERLAY"
	VerbSession     = "SESSION"
	VerbPing        = "PING"
	VerbInfo        = "INFO"
	VerbShutdown    = "SHUTDOWN"
)

// Process sub-verbs
const (
	SubVerbStatus      = "STATUS"
	SubVerbOutput      = "OUTPUT"
	SubVerbStop        = "STOP"
	SubVerbList        = "LIST"
	SubVerbCleanupPort = "CLEANUP-PORT"
)

// Proxy sub-verbs
const (
	SubVerbStart = "START"
	SubVerbExec  = "EXEC"
	SubVerbToast = "TOAST"
	// SubVerbStop, SubVerbStatus, SubVerbList reused from process
)

// ProxyLog sub-verbs
const (
	SubVerbQuery = "QUERY"
	SubVerbClear = "CLEAR"
	SubVerbStats = "STATS"
)

// CurrentPage sub-verbs
const (
	SubVerbGet = "GET"
	// SubVerbList, SubVerbClear reused
)

// Overlay sub-verbs
const (
	SubVerbSet      = "SET"
	SubVerbActivity = "ACTIVITY"
	// SubVerbGet, SubVerbClear reused
)

// Chaos sub-verbs
const (
	SubVerbEnable     = "ENABLE"
	SubVerbDisable    = "DISABLE"
	SubVerbAddRule    = "ADD-RULE"
	SubVerbRemoveRule = "REMOVE-RULE"
	SubVerbListRules  = "LIST-RULES"
	SubVerbPreset     = "PRESET"
	SubVerbReset      = "RESET"
	// SubVerbStatus, SubVerbStats, SubVerbClear, SubVerbSet reused
)

// Session sub-verbs
const (
	SubVerbRegister   = "REGISTER"
	SubVerbUnregister = "UNREGISTER"
	SubVerbHeartbeat  = "HEARTBEAT"
	SubVerbSend       = "SEND"
	SubVerbSchedule   = "SCHEDULE"
	SubVerbCancel     = "CANCEL"
	SubVerbTasks      = "TASKS"
	SubVerbFind       = "FIND" // Find session by directory ancestry
	SubVerbAttach     = "ATTACH"
	// SubVerbList, SubVerbGet, SubVerbStatus reused
)

// RunConfig represents configuration for a RUN command.
type RunConfig struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Mode       string   `json:"mode"` // background, foreground, foreground-raw
	ScriptName string   `json:"script_name,omitempty"`
	Raw        bool     `json:"raw,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	Env        []string `json:"env,omitempty"` // Environment variables from client (KEY=VALUE format)
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

// OutputFilter represents filters for PROC OUTPUT command.
type OutputFilter struct {
	Stream string `json:"stream,omitempty"` // stdout, stderr, combined
	Tail   int    `json:"tail,omitempty"`
	Head   int    `json:"head,omitempty"`
	Grep   string `json:"grep,omitempty"`
	GrepV  bool   `json:"grep_v,omitempty"`
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
}

// DirectoryFilter represents directory scoping for list operations.
// Priority: Global > SessionCode > Directory
type DirectoryFilter struct {
	SessionCode string `json:"session_code,omitempty"` // Session code for scoping (preferred)
	Directory   string `json:"directory,omitempty"`    // Current working directory (legacy, use session_code instead)
	Global      bool   `json:"global,omitempty"`       // If true, ignore all filtering
}

// ToastConfig represents configuration for a PROXY TOAST command.
type ToastConfig struct {
	Type     string `json:"type"`               // success, error, warning, info
	Title    string `json:"title,omitempty"`    // Toast title (optional)
	Message  string `json:"message"`            // Toast message
	Duration int    `json:"duration,omitempty"` // Duration in ms (0 for default)
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
type SessionRegisterConfig struct {
	OverlayPath string   `json:"overlay_path"`   // Unix socket path for overlay
	ProjectPath string   `json:"project_path"`   // Directory where session was started
	Command     string   `json:"command"`        // Command being run (e.g., "claude")
	Args        []string `json:"args,omitempty"` // Command arguments
}

// SessionScheduleConfig represents configuration for a SESSION SCHEDULE command.
type SessionScheduleConfig struct {
	SessionCode string `json:"session_code"` // Target session
	Duration    string `json:"duration"`     // Go duration string (e.g., "5m", "1h30m")
	Message     string `json:"message"`      // Message to deliver
	ProjectPath string `json:"project_path"` // For project-scoped storage
}
