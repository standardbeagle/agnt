package overlay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// tailscaleDNSCache caches the Tailscale DNS name to avoid repeated exec calls.
var (
	tailscaleDNSCache  string
	tailscaleDNSCached bool
	tailscaleDNSMu     sync.RWMutex
	tailscaleCacheTime time.Time
	tailscaleCacheTTL  = 5 * time.Minute // Re-check every 5 minutes
)

// getTailscaleDNS returns the Tailscale DNS name if available, or empty string if not.
// Results are cached for efficiency.
func getTailscaleDNS() string {
	tailscaleDNSMu.RLock()
	if tailscaleDNSCached && time.Since(tailscaleCacheTime) < tailscaleCacheTTL {
		result := tailscaleDNSCache
		tailscaleDNSMu.RUnlock()
		return result
	}
	tailscaleDNSMu.RUnlock()

	// Need to fetch - acquire write lock
	tailscaleDNSMu.Lock()
	defer tailscaleDNSMu.Unlock()

	// Double-check after acquiring write lock
	if tailscaleDNSCached && time.Since(tailscaleCacheTime) < tailscaleCacheTTL {
		return tailscaleDNSCache
	}

	// Try to get Tailscale DNS name
	dnsName := detectTailscaleDNS()
	tailscaleDNSCache = dnsName
	tailscaleDNSCached = true
	tailscaleCacheTime = time.Now()

	return dnsName
}

// detectTailscaleDNS runs tailscale status to get the DNS name.
func detectTailscaleDNS() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	// Parse JSON to get DNSName
	var status struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return ""
	}

	// Remove trailing dot if present
	dnsName := strings.TrimSuffix(status.Self.DNSName, ".")
	return dnsName
}

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

	// Link processes and proxies together
	f.linkProcessesAndProxies(status.Processes, status.Proxies)

	// Fetch last output for running processes (limited to first few to avoid slowdown)
	f.fetchLastOutputForProcesses(status.Processes)

	// Fetch browser sessions from each proxy
	sessions, err := f.fetchBrowserSessions(proxies)
	if err == nil {
		status.BrowserSessions = sessions
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

		// Check for tunnel info
		if tunnelURL, ok := pm["tunnel_url"].(string); ok {
			info.TunnelURL = tunnelURL
		}
		if tunnelRunning, ok := pm["tunnel_running"].(bool); ok {
			info.TunnelRunning = tunnelRunning
		}

		// Add Tailscale URL if Tailscale is available
		if tailscaleDNS := getTailscaleDNS(); tailscaleDNS != "" && info.ListenAddr != "" {
			// Extract port from listen address
			port := ""
			if idx := strings.LastIndex(info.ListenAddr, ":"); idx != -1 {
				port = info.ListenAddr[idx:] // includes the colon
			}
			if port != "" {
				info.TailscaleURL = "http://" + tailscaleDNS + port
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

func (f *StatusFetcher) fetchBrowserSessions(proxies []ProxyInfo) ([]BrowserSession, error) {
	var sessions []BrowserSession

	for _, proxy := range proxies {
		// Use CurrentPageList to get page sessions for this proxy
		result, err := f.client.CurrentPageList(proxy.ID)
		if err != nil {
			continue
		}

		pagesRaw, ok := result["sessions"].([]interface{})
		if !ok {
			continue
		}

		for _, p := range pagesRaw {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}

			session := BrowserSession{
				ProxyID: proxy.ID,
			}

			if id, ok := pm["session_id"].(string); ok {
				session.SessionID = id
			}
			if url, ok := pm["url"].(string); ok {
				session.URL = url
			}
			if count, ok := pm["interaction_count"].(float64); ok {
				session.Interactions = int(count)
			}
			if count, ok := pm["mutation_count"].(float64); ok {
				session.Mutations = int(count)
			}
			if ts, ok := pm["last_activity"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					session.LastActivity = t
				}
			}

			sessions = append(sessions, session)
		}
	}

	return sessions, nil
}

// linkProcessesAndProxies links processes and proxies that are related.
// A proxy targets a process if the proxy's target URL port matches a port in the process's command.
func (f *StatusFetcher) linkProcessesAndProxies(processes []ProcessInfo, proxies []ProxyInfo) {
	// Build maps for linking
	proxyByID := make(map[string]int)   // proxy ID -> index
	portToProxy := make(map[string]int) // target port -> proxy index
	for i := range proxies {
		proxyByID[proxies[i].ID] = i

		targetURL := proxies[i].TargetURL
		if targetURL == "" {
			continue
		}
		parsed, err := url.Parse(targetURL)
		if err != nil {
			continue
		}
		port := parsed.Port()
		if port == "" {
			// Default ports
			if parsed.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		portToProxy[port] = i
	}

	// Match processes to proxies
	for i := range processes {
		proc := &processes[i]

		// First, try direct ID match (process "dev" links to proxy "dev")
		if proxyIdx, ok := proxyByID[proc.ID]; ok {
			proc.LinkedProxyID = proxies[proxyIdx].ID
			proxies[proxyIdx].LinkedProcessID = proc.ID
			continue
		}

		// Fall back to port matching in process ID or command
		checkStr := proc.ID + " " + proc.Command
		for port, proxyIdx := range portToProxy {
			// Look for common patterns: :PORT, PORT, -p PORT, --port PORT
			patterns := []string{
				":" + port,
				" " + port + " ",
				" " + port + "\n",
				"-p " + port,
				"--port " + port,
				"--port=" + port,
			}
			for _, pattern := range patterns {
				if strings.Contains(checkStr, pattern) || strings.HasSuffix(checkStr, " "+port) {
					proc.LinkedProxyID = proxies[proxyIdx].ID
					proxies[proxyIdx].LinkedProcessID = proc.ID
					break
				}
			}
			if proc.LinkedProxyID != "" {
				break
			}
		}
	}
}

// fetchLastOutputForProcesses fetches the last output line for each running process.
// Limited to first 6 processes to avoid slowing down the status update.
func (f *StatusFetcher) fetchLastOutputForProcesses(processes []ProcessInfo) {
	const maxProcesses = 6
	const maxOutputLen = 120 // Truncate long lines

	for i := range processes {
		if i >= maxProcesses {
			break
		}
		proc := &processes[i]
		if proc.State != "running" {
			continue
		}

		// Fetch last line of output
		filter := protocol.OutputFilter{
			Stream: "combined",
			Tail:   1,
		}
		output, err := f.client.ProcOutput(proc.ID, filter)
		if err != nil {
			continue
		}

		// Clean up the output
		output = strings.TrimSpace(output)
		if output == "" {
			continue
		}

		// Take only the last non-empty line
		lines := strings.Split(output, "\n")
		for j := len(lines) - 1; j >= 0; j-- {
			line := strings.TrimSpace(lines[j])
			if line != "" {
				output = line
				break
			}
		}

		// Truncate if too long
		if len(output) > maxOutputLen {
			output = output[:maxOutputLen-3] + "..."
		}

		proc.LastOutput = output
	}
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
