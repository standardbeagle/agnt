package daemon

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

// toLibraryConfig converts agnt AutoStartConfig to go-cli-server config.
func (c AutoStartConfig) toLibraryConfig() goclient.AutoStartConfig {
	return goclient.AutoStartConfig{
		SocketPath:     c.SocketPath,
		HubPath:        c.DaemonPath,
		StartTimeout:   c.StartTimeout,
		RetryInterval:  c.RetryInterval,
		MaxRetries:     c.MaxRetries,
		ProcessMatcher: isAgntDaemonProcess,
	}
}

// AutoStartClient creates a client that auto-starts the daemon if needed.
type AutoStartClient struct {
	*Client
	config AutoStartConfig
}

// NewAutoStartClient creates a new auto-start client.
func NewAutoStartClient(config AutoStartConfig) *AutoStartClient {
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
