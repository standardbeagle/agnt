package daemonclient

import (
	"time"

	goclient "github.com/standardbeagle/go-cli-server/client"
)

// AutoStartConfig holds configuration for auto-starting the daemon.
type AutoStartConfig struct {
	// SocketPath is the socket path to connect to.
	SocketPath string
	// DaemonPath is the path to the daemon executable.
	DaemonPath string
	// StartTimeout is how long to wait for the daemon to start.
	StartTimeout time.Duration
	// RetryInterval is how long to wait between connection attempts.
	RetryInterval time.Duration
	// MaxRetries is the maximum number of connection attempts.
	MaxRetries int
}

// DefaultAutoStartConfig returns sensible defaults.
func DefaultAutoStartConfig() AutoStartConfig {
	return AutoStartConfig{
		SocketPath:    DefaultSocketPath(),
		DaemonPath:    "", // Will use current executable
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}
}

// withDefaults fills zero-value fields so callers can safely pass a partial
// AutoStartConfig. In particular, the underlying hub client rejects an empty
// socket path and will not attempt auto-start.
func (c AutoStartConfig) withDefaults() AutoStartConfig {
	defaults := DefaultAutoStartConfig()
	if c.SocketPath == "" {
		c.SocketPath = defaults.SocketPath
	}
	if c.StartTimeout == 0 {
		c.StartTimeout = defaults.StartTimeout
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = defaults.RetryInterval
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = defaults.MaxRetries
	}
	return c
}

// toLibraryConfig converts agnt AutoStartConfig to go-cli-server config.
func (c AutoStartConfig) toLibraryConfig() goclient.AutoStartConfig {
	c = c.withDefaults()
	// An explicit DaemonPath always wins (callers that know exactly which
	// binary to spawn — upgrade, tests). Otherwise self-provision a fresh
	// `agnt-daemon` copy so autostart works from a bare `go install` and never
	// spawns a stale daemon. ensureDaemonBinary returns "" under test / on
	// failure, leaving the library's own copy-lookup + self-exec fallback.
	hubPath := c.DaemonPath
	if hubPath == "" {
		hubPath = ensureDaemonBinary()
	}
	return goclient.AutoStartConfig{
		SocketPath:     c.SocketPath,
		HubPath:        hubPath,
		HubArgs:        agntHubArgs(c.SocketPath),
		StartTimeout:   c.StartTimeout,
		RetryInterval:  c.RetryInterval,
		MaxRetries:     c.MaxRetries,
		ProcessMatcher: isAgntDaemonProcess,
	}
}

// agntHubArgs is the argv the auto-starter uses to spawn the daemon. agnt's
// hub is a subcommand (`agnt daemon start`), not the bare executable, so the
// library's generic default ("--socket <path>" alone) would launch the CLI with
// no subcommand and never bring up the hub. HubArgs fully replaces that default.
func agntHubArgs(socketPath string) []string {
	return []string{"daemon", "start", "--socket", socketPath}
}

// AutoStartClient creates a client that auto-starts the daemon if needed.
type AutoStartClient struct {
	*Client
	config AutoStartConfig
}

// NewAutoStartClient creates a new auto-start client.
func NewAutoStartClient(config AutoStartConfig) *AutoStartClient {
	config = config.withDefaults()
	return &AutoStartClient{
		Client: NewClient(
			WithSocketPath(config.SocketPath),
			WithTimeout(30*time.Second),
		),
		config: config,
	}
}

// Connect connects to the daemon, starting it if necessary.
func (c *AutoStartClient) Connect() error {
	// Use the library's auto-start mechanism
	conn, err := goclient.EnsureHubRunning(c.config.toLibraryConfig())
	if err != nil {
		return err
	}
	// Replace our Client's connection with the connected one
	c.Client.conn = conn
	return nil
}

// EnsureDaemonRunning ensures the daemon is running, starting it if needed.
// Returns a connected client.
func EnsureDaemonRunning(config AutoStartConfig) (*Client, error) {
	conn, err := goclient.EnsureHubRunning(config.toLibraryConfig())
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// StopDaemon connects to a running daemon and requests shutdown.
func StopDaemon(socketPath string) error {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}

	client := NewClient(WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		if err == ErrSocketNotFound {
			return nil // Daemon not running, nothing to stop
		}
		return err
	}
	defer client.Close()

	return client.Shutdown()
}

// IsDaemonRunning checks if the daemon is running at the given socket path.
func IsDaemonRunning(socketPath string) bool {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	return IsRunning(socketPath)
}
