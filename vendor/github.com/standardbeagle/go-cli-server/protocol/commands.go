// Package protocol defines the text-based IPC protocol for hub communication.
package protocol

// Command represents a parsed command from the client.
type Command struct {
	Verb        string   // Primary command verb (RUN, PROC, etc.)
	SubVerb     string   // Optional sub-verb (STATUS, OUTPUT, START, etc.)
	Args        []string // Positional arguments
	Data        []byte   // Optional binary/JSON data payload
}

// Built-in command verbs (core hub functionality)
const (
	VerbRun        = "RUN"
	VerbRunJSON    = "RUN-JSON"
	VerbProc       = "PROC"
	VerbSession    = "SESSION"    // Session management
	VerbSubprocess = "SUBPROCESS" // Subprocess management
	VerbScript     = "SCRIPT"     // Script registry management
	VerbPing       = "PING"
	VerbInfo       = "INFO"
	VerbShutdown   = "SHUTDOWN"
)

// Process sub-verbs
const (
	SubVerbStatus      = "STATUS"
	SubVerbOutput      = "OUTPUT"
	SubVerbStop        = "STOP"
	SubVerbList        = "LIST"
	SubVerbCleanupPort = "CLEANUP-PORT"
	SubVerbStdin       = "STDIN"  // Write to process stdin
	SubVerbStream      = "STREAM" // Stream stdout/stderr
)

// Session sub-verbs
const (
	SubVerbRegister   = "REGISTER"
	SubVerbUnregister = "UNREGISTER"
	SubVerbHeartbeat  = "HEARTBEAT"
	SubVerbGet        = "GET"
	SubVerbStart      = "START"
	SubVerbClear      = "CLEAR"
	SubVerbSet        = "SET"
	SubVerbRestart    = "RESTART"
)

// RunConfig represents configuration for a RUN command.
type RunConfig struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Mode        string   `json:"mode"` // background, foreground, foreground-raw
	ScriptName  string   `json:"script_name,omitempty"`
	Raw         bool     `json:"raw,omitempty"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Env         []string `json:"env,omitempty"`
	EnableStdin bool     `json:"enable_stdin,omitempty"` // Enable stdin pipe
}

// OutputFilter represents filters for PROC OUTPUT command.
type OutputFilter struct {
	Stream string `json:"stream,omitempty"` // stdout, stderr, combined
	Tail   int    `json:"tail,omitempty"`
	Head   int    `json:"head,omitempty"`
	Grep   string `json:"grep,omitempty"`
	GrepV  bool   `json:"grep_v,omitempty"`
}

// DirectoryFilter represents directory scoping for list operations.
type DirectoryFilter struct {
	SessionCode string `json:"session_code,omitempty"`
	Directory   string `json:"directory,omitempty"`
	Global      bool   `json:"global,omitempty"`
}

// SessionRegisterConfig represents configuration for a SESSION REGISTER command.
type SessionRegisterConfig struct {
	OverlayPath string   `json:"overlay_path,omitempty"`
	ProjectPath string   `json:"project_path"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
}

// SubprocessRegisterConfig represents configuration for SUBPROCESS REGISTER command.
// Clients use this to register a subprocess with the hub for command routing.
type SubprocessRegisterConfig struct {
	// ID is a unique identifier for this subprocess
	ID string `json:"id"`

	// Name is a human-readable name
	Name string `json:"name"`

	// Description provides details about what this subprocess does
	Description string `json:"description,omitempty"`

	// Commands this subprocess handles
	// Supports exact matches ("PROXY START") and prefixes ("PROXY *")
	Commands []string `json:"commands"`

	// Transport specifies how to communicate with the subprocess
	Transport SubprocessTransport `json:"transport"`

	// HealthCheck configuration
	HealthCheck SubprocessHealthCheck `json:"health_check,omitempty"`

	// AutoStart starts the subprocess immediately after registration
	AutoStart bool `json:"auto_start,omitempty"`

	// AutoRestart enables automatic restart on crash
	AutoRestart bool `json:"auto_restart,omitempty"`

	// MaxRestarts limits restart attempts (0 = unlimited)
	MaxRestarts int `json:"max_restarts,omitempty"`

	// RestartDelayMs is the delay between restart attempts in milliseconds
	RestartDelayMs int `json:"restart_delay_ms,omitempty"`
}

// SubprocessTransport specifies how to communicate with a subprocess.
type SubprocessTransport struct {
	// Type is the transport type: "unix", "tcp", "stdio"
	Type string `json:"type"`

	// Address is the endpoint address (socket path for unix, host:port for tcp)
	// Ignored for "stdio" type
	Address string `json:"address,omitempty"`

	// Command is the executable path (for "stdio" transport)
	Command string `json:"command,omitempty"`

	// Args are command arguments (for "stdio" transport)
	Args []string `json:"args,omitempty"`

	// Env are additional environment variables (for "stdio" transport)
	Env []string `json:"env,omitempty"`

	// TimeoutMs is the timeout for connection/command operations in milliseconds
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

// SubprocessHealthCheck configures health checking for a subprocess.
type SubprocessHealthCheck struct {
	// Enabled turns on health checking
	Enabled bool `json:"enabled"`

	// IntervalMs is the interval between health checks in milliseconds
	IntervalMs int `json:"interval_ms,omitempty"`

	// TimeoutMs is the timeout for each health check in milliseconds
	TimeoutMs int `json:"timeout_ms,omitempty"`

	// FailureThreshold is the number of consecutive failures before marking unhealthy
	FailureThreshold int `json:"failure_threshold,omitempty"`
}

// SubprocessInfo represents information about a registered subprocess.
type SubprocessInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"` // pending, starting, running, stopping, stopped, failed
	Healthy     bool   `json:"healthy"`

	// Commands this subprocess handles
	Commands []string `json:"commands"`

	// Statistics
	CommandsHandled int64 `json:"commands_handled"`
	CommandsFailed  int64 `json:"commands_failed"`
	RestartCount    int   `json:"restart_count"`

	// Timestamps (Unix milliseconds)
	LastCommandMs  int64 `json:"last_command_ms,omitempty"`
	LastHealthyMs  int64 `json:"last_healthy_ms,omitempty"`
	StateChangedMs int64 `json:"state_changed_ms,omitempty"`
}
