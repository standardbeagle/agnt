package overlay

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"devtool-mcp/internal/daemon"
	"devtool-mcp/internal/protocol"
)

// StatusFetcher fetches status from the daemon periodically.
type StatusFetcher struct {
	client     *daemon.Client
	overlay    *Overlay
	interval   time.Duration
	socketPath string

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewStatusFetcher creates a new StatusFetcher.
func NewStatusFetcher(socketPath string, overlay *Overlay, interval time.Duration) *StatusFetcher {
	opts := []daemon.ClientOption{}
	if socketPath != "" {
		opts = append(opts, daemon.WithSocketPath(socketPath))
	}

	return &StatusFetcher{
		client:     daemon.NewClient(opts...),
		overlay:    overlay,
		interval:   interval,
		socketPath: socketPath,
	}
}

// Start starts the status fetcher.
func (f *StatusFetcher) Start(ctx context.Context) {
	ctx, f.cancel = context.WithCancel(ctx)

	f.wg.Add(1)
	go f.run(ctx)
}

// Stop stops the status fetcher.
func (f *StatusFetcher) Stop() {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
}

// Refresh triggers an immediate status refresh.
func (f *StatusFetcher) Refresh() {
	f.fetchStatus()
}

func (f *StatusFetcher) run(ctx context.Context) {
	defer f.wg.Done()

	// Initial fetch
	f.fetchStatus()

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.fetchStatus()
		}
	}
}

func (f *StatusFetcher) fetchStatus() {
	status := Status{
		LastUpdate: time.Now(),
	}

	// Check daemon connection with ping
	start := time.Now()
	err := f.client.Connect()
	if err != nil {
		status.DaemonConnected = ConnectionDisconnected
		f.overlay.UpdateStatus(status)
		return
	}
	defer f.client.Close()

	// Simple ping by requesting process list (lightweight)
	pingMs := time.Since(start).Milliseconds()
	status.DaemonConnected = ConnectionConnected
	status.DaemonPingMs = pingMs

	// Fetch processes
	processes, err := f.fetchProcesses()
	if err == nil {
		status.Processes = processes
	}

	// Fetch proxies
	proxies, err := f.fetchProxies()
	if err == nil {
		status.Proxies = proxies
	}

	// Fetch recent errors from proxy logs
	errors, err := f.fetchRecentErrors()
	if err == nil {
		status.RecentErrors = errors
	}

	f.overlay.UpdateStatus(status)
}

func (f *StatusFetcher) fetchProcesses() ([]ProcessInfo, error) {
	// Use ProcList with global filter to get all processes
	result, err := f.client.ProcList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		return nil, err
	}

	// Parse the result
	processesRaw, ok := result["processes"].([]interface{})
	if !ok {
		return nil, nil
	}

	processes := make([]ProcessInfo, 0, len(processesRaw))
	for _, p := range processesRaw {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		info := ProcessInfo{}
		if id, ok := pm["id"].(string); ok {
			info.ID = id
		}
		if cmd, ok := pm["command"].(string); ok {
			info.Command = cmd
		}
		if state, ok := pm["state"].(string); ok {
			info.State = state
		}
		if runtime, ok := pm["runtime_ms"].(float64); ok {
			info.Runtime = time.Duration(runtime) * time.Millisecond
		}
		processes = append(processes, info)
	}

	return processes, nil
}

func (f *StatusFetcher) fetchProxies() ([]ProxyInfo, error) {
	// Use ProxyList with global filter to get all proxies
	result, err := f.client.ProxyList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		return nil, err
	}

	// Parse the result
	proxiesRaw, ok := result["proxies"].([]interface{})
	if !ok {
		return nil, nil
	}

	proxies := make([]ProxyInfo, 0, len(proxiesRaw))
	for _, p := range proxiesRaw {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		info := ProxyInfo{}
		if id, ok := pm["id"].(string); ok {
			info.ID = id
		}
		if target, ok := pm["target_url"].(string); ok {
			info.TargetURL = target
		}
		if listen, ok := pm["listen_addr"].(string); ok {
			info.ListenAddr = listen
		}

		// Check stats for error count
		if stats, ok := pm["stats"].(map[string]interface{}); ok {
			if errCount, ok := stats["error_count"].(float64); ok {
				info.ErrorCount = int(errCount)
				info.HasErrors = info.ErrorCount > 0
			}
		}

		proxies = append(proxies, info)
	}

	return proxies, nil
}

func (f *StatusFetcher) fetchRecentErrors() ([]ErrorInfo, error) {
	// Query proxy logs for errors in the last 5 minutes
	// We'll query each proxy's error logs
	proxies, err := f.fetchProxies()
	if err != nil {
		return nil, err
	}

	var errors []ErrorInfo
	cutoff := time.Now().Add(-5 * time.Minute)

	for _, proxy := range proxies {
		// Use ProxyLogQuery to get error logs
		filter := protocol.LogQueryFilter{
			Types: []string{"error"},
			Since: cutoff.Format(time.RFC3339),
			Limit: 10,
		}

		result, err := f.client.ProxyLogQuery(proxy.ID, filter)
		if err != nil {
			continue
		}

		entriesRaw, ok := result["entries"].([]interface{})
		if !ok {
			continue
		}

		for _, e := range entriesRaw {
			entry, ok := e.(map[string]interface{})
			if !ok {
				continue
			}

			entryType, _ := entry["type"].(string)
			if entryType != "error" {
				continue
			}

			var timestamp time.Time
			if ts, ok := entry["timestamp"].(string); ok {
				timestamp, _ = time.Parse(time.RFC3339, ts)
			}

			var message string
			if errData, ok := entry["error"].(map[string]interface{}); ok {
				message, _ = errData["message"].(string)
			}

			errors = append(errors, ErrorInfo{
				Source:    "proxy:" + proxy.ID,
				Message:   message,
				Timestamp: timestamp,
			})
		}
	}

	return errors, nil
}

// DaemonBashRunner implements BashRunner using the daemon client.
type DaemonBashRunner struct {
	socketPath string
	counter    atomic.Int64
}

// DaemonOutputFetcher implements ProcessOutputFetcher using the daemon client.
type DaemonOutputFetcher struct {
	socketPath string
}

// NewDaemonOutputFetcher creates a new DaemonOutputFetcher.
func NewDaemonOutputFetcher(socketPath string) *DaemonOutputFetcher {
	return &DaemonOutputFetcher{
		socketPath: socketPath,
	}
}

// GetProcessOutput fetches the last N lines of output for a process.
func (f *DaemonOutputFetcher) GetProcessOutput(processID string, tailLines int) (string, error) {
	// Create daemon client
	opts := []daemon.ClientOption{}
	if f.socketPath != "" {
		opts = append(opts, daemon.WithSocketPath(f.socketPath))
	}
	client := daemon.NewClient(opts...)

	if err := client.Connect(); err != nil {
		return "", fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer client.Close()

	// Fetch output with tail filter
	filter := protocol.OutputFilter{
		Stream: "combined",
		Tail:   tailLines,
	}

	output, err := client.ProcOutput(processID, filter)
	if err != nil {
		return "", err
	}

	return output, nil
}

// NewDaemonBashRunner creates a new DaemonBashRunner.
func NewDaemonBashRunner(socketPath string) *DaemonBashRunner {
	return &DaemonBashRunner{
		socketPath: socketPath,
	}
}

// DaemonConnectorImpl implements DaemonConnector using auto-start client.
type DaemonConnectorImpl struct {
	socketPath string
}

// NewDaemonConnector creates a new DaemonConnector.
func NewDaemonConnector(socketPath string) *DaemonConnectorImpl {
	return &DaemonConnectorImpl{
		socketPath: socketPath,
	}
}

// Connect attempts to connect to the daemon, auto-starting it if needed.
func (c *DaemonConnectorImpl) Connect() error {
	socketPath := c.socketPath
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}

	// First clean up any zombie daemons
	daemon.CleanupZombieDaemons(socketPath)

	config := daemon.AutoStartConfig{
		SocketPath:    socketPath,
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}
	client := daemon.NewAutoStartClient(config)

	if err := client.Connect(); err != nil {
		return err
	}
	client.Close()
	return nil
}

// IsConnected returns true if currently connected to the daemon.
func (c *DaemonConnectorImpl) IsConnected() bool {
	socketPath := c.socketPath
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}
	return daemon.IsDaemonRunning(socketPath)
}

// RunBashCommand runs a bash command via the daemon and returns the process ID.
func (r *DaemonBashRunner) RunBashCommand(command string) (string, error) {
	// Create daemon client with auto-start capability
	socketPath := r.socketPath
	if socketPath == "" {
		socketPath = daemon.DefaultSocketPath()
	}
	config := daemon.AutoStartConfig{
		SocketPath:    socketPath,
		StartTimeout:  5 * time.Second,
		RetryInterval: 100 * time.Millisecond,
		MaxRetries:    50,
	}
	client := daemon.NewAutoStartClient(config)

	if err := client.Connect(); err != nil {
		return "", fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer client.Close()

	// Generate unique process ID
	count := r.counter.Add(1)
	processID := fmt.Sprintf("bash-%d-%d", time.Now().Unix(), count)

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	// Run the command via the daemon
	runConfig := protocol.RunConfig{
		ID:      processID,
		Path:    cwd,
		Mode:    "background",
		Raw:     true,
		Command: "sh",
		Args:    []string{"-c", command},
	}

	_, err = client.Run(runConfig)
	if err != nil {
		return "", fmt.Errorf("failed to run command: %w", err)
	}

	return processID, nil
}
