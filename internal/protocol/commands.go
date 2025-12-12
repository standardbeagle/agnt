// Package protocol defines the text-based IPC protocol for daemon communication.
package protocol

// Command represents a parsed command from the client.
type Command struct {
	Verb    string   // Primary command verb (RUN, PROC, PROXY, etc.)
	SubVerb string   // Optional sub-verb (STATUS, OUTPUT, START, etc.)
	Args    []string // Positional arguments
	Data    []byte   // Optional binary/JSON data payload
}

// Command verbs
const (
	VerbRun         = "RUN"
	VerbRunJSON     = "RUN-JSON"
	VerbProc        = "PROC"
	VerbProxy       = "PROXY"
	VerbProxyLog    = "PROXYLOG"
	VerbCurrentPage = "CURRENTPAGE"
	VerbDetect      = "DETECT"
	VerbOverlay     = "OVERLAY"
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
	SubVerbSet = "SET"
	// SubVerbGet, SubVerbClear reused
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
}

// ProxyStartConfig represents configuration for a PROXY START command.
type ProxyStartConfig struct {
	ID         string `json:"id"`
	TargetURL  string `json:"target_url"`
	Port       int    `json:"port"`
	MaxLogSize int    `json:"max_log_size,omitempty"`
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
type DirectoryFilter struct {
	Directory string `json:"directory,omitempty"` // Current working directory (defaults to "." if empty)
	Global    bool   `json:"global,omitempty"`    // If true, ignore directory filtering
}
