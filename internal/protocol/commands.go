package protocol

// Agnt-specific command verbs (beyond those in go-mcp-hub).
const (
	VerbProxy       = "PROXY"
	VerbProxyLog    = "PROXYLOG"
	VerbCurrentPage = "CURRENTPAGE"
	VerbTunnel      = "TUNNEL"
	VerbChaos       = "CHAOS"
	VerbDetect      = "DETECT"
	VerbOverlay     = "OVERLAY"
)

// Agnt-specific sub-verbs (beyond those in go-mcp-hub).
const (
	SubVerbExec       = "EXEC"
	SubVerbToast      = "TOAST"
	SubVerbQuery      = "QUERY"
	SubVerbStats      = "STATS"
	SubVerbActivity   = "ACTIVITY"
	SubVerbEnable     = "ENABLE"
	SubVerbDisable    = "DISABLE"
	SubVerbAddRule    = "ADD-RULE"
	SubVerbRemoveRule = "REMOVE-RULE"
	SubVerbListRules  = "LIST-RULES"
	SubVerbPreset     = "PRESET"
	SubVerbReset      = "RESET"
	SubVerbSend       = "SEND"
	SubVerbSchedule   = "SCHEDULE"
	SubVerbCancel     = "CANCEL"
	SubVerbTasks      = "TASKS"
	SubVerbFind       = "FIND"
	SubVerbAttach     = "ATTACH"
)

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
// This extends the base hub SessionRegisterConfig with agnt-specific fields.
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
