package overlay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
// It uses a shared DaemonClient for all requests.
// By default, it only fetches processes/proxies from the current project directory.
type StatusFetcher struct {
	conn        DaemonClient
	overlay     *Overlay
	interval    time.Duration
	projectPath string // Current project directory for filtering

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewStatusFetcher creates a new StatusFetcher using a shared connection.
// It automatically detects the current working directory for scoping.
func NewStatusFetcher(conn DaemonClient, overlay *Overlay, interval time.Duration) *StatusFetcher {
	// Get current working directory for session scoping
	projectPath, err := os.Getwd()
	if err != nil {
		projectPath = "" // Fall back to global if can't get cwd
	}

	return &StatusFetcher{
		conn:        conn,
		overlay:     overlay,
		interval:    interval,
		projectPath: projectPath,
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
	err := f.conn.EnsureConnected()
	if err != nil {
		status.DaemonConnected = ConnectionDisconnected
		f.overlay.UpdateStatus(status)
		return
	}

	// Simple ping by requesting process list (lightweight)
	pingMs := time.Since(start).Milliseconds()
	status.DaemonConnected = ConnectionConnected
	status.DaemonPingMs = pingMs

	// Fetch scripts (persistent state from ScriptRegistry)
	scripts, err := f.fetchScripts()
	if err == nil {
		status.Scripts = scripts
	}

	// Fetch processes (still needed for proxy linking and URLs)
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

	// Fetch browser sessions from each proxy
	sessions, err := f.fetchBrowserSessions(proxies)
	if err == nil {
		status.BrowserSessions = sessions
	}

	// Fetch recent errors from proxy logs
	recentErrors, err := f.fetchRecentErrors()
	if err == nil {
		status.RecentErrors = recentErrors
	}

	// Fetch startup log for the log panel
	startupLog, err := f.fetchStartupLog()
	if err == nil {
		status.StartupLog = startupLog
	}

	f.overlay.UpdateStatus(status)
}

func (f *StatusFetcher) fetchScripts() ([]ScriptInfo, error) {
	result, err := f.conn.RequestJSON(protocol.VerbScript, protocol.DirectoryFilter{Directory: f.projectPath}, protocol.SubVerbList)
	if err != nil {
		return nil, err
	}

	scriptsRaw, ok := result["scripts"].([]interface{})
	if !ok {
		return nil, nil
	}

	scripts := make([]ScriptInfo, 0, len(scriptsRaw))
	for _, s := range scriptsRaw {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}

		info := ScriptInfo{}
		if name, ok := sm["name"].(string); ok {
			info.Name = name
		}
		if pid, ok := sm["process_id"].(string); ok {
			info.ProcessID = pid
		}
		if state, ok := sm["state"].(string); ok {
			info.State = state
		}
		if cmd, ok := sm["command"].(string); ok {
			info.Command = cmd
		}
		if sc, ok := sm["start_count"].(float64); ok {
			info.StartCount = int64(sc)
		}
		if fc, ok := sm["fail_count"].(float64); ok {
			info.FailCount = int64(fc)
		}
		if le, ok := sm["last_error"].(string); ok {
			info.LastError = le
		}
		if ha, ok := sm["has_alerts"].(bool); ok {
			info.HasAlerts = ha
		}

		scripts = append(scripts, info)
	}

	// Sort by name for stable ordering
	sort.Slice(scripts, func(i, j int) bool {
		return scripts[i].Name < scripts[j].Name
	})

	return scripts, nil
}

func (f *StatusFetcher) fetchProcesses() ([]ProcessInfo, error) {
	// Use request builder - filter by session directory
	result, err := f.conn.RequestJSON(protocol.VerbProc, protocol.DirectoryFilter{Directory: f.projectPath}, protocol.SubVerbList)
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
		// Get URLs from server (persisted by URL tracker)
		if urls, ok := pm["urls"].([]interface{}); ok {
			for _, u := range urls {
				if urlStr, ok := u.(string); ok {
					info.URLs = append(info.URLs, urlStr)
				}
			}
		}
		processes = append(processes, info)
	}

	// Sort by ID for stable ordering
	sort.Slice(processes, func(i, j int) bool {
		return processes[i].ID < processes[j].ID
	})

	return processes, nil
}

func (f *StatusFetcher) fetchProxies() ([]ProxyInfo, error) {
	// Use request builder - filter by session directory
	result, err := f.conn.RequestJSON(protocol.VerbProxy, protocol.DirectoryFilter{Directory: f.projectPath}, protocol.SubVerbList)
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

	// Sort by ID for stable ordering
	sort.Slice(proxies, func(i, j int) bool {
		return proxies[i].ID < proxies[j].ID
	})

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
		// Use request builder for proxy log query
		filter := protocol.LogQueryFilter{
			Types: []string{"error"},
			Since: cutoff.Format(time.RFC3339),
			Limit: 10,
		}

		result, err := f.conn.RequestJSON(protocol.VerbProxyLog, filter, protocol.SubVerbQuery, proxy.ID)
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

func (f *StatusFetcher) fetchStartupLog() ([]StartupLogEntry, error) {
	result, err := f.conn.RequestJSON(protocol.VerbAlerts, map[string]interface{}{"limit": 100}, protocol.SubVerbStartupLog)
	if err != nil {
		return nil, err
	}

	entriesRaw, ok := result["entries"].([]interface{})
	if !ok {
		return nil, nil
	}

	var entries []StartupLogEntry
	for _, e := range entriesRaw {
		entry, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		var ts time.Time
		if tsStr, ok := entry["timestamp"].(string); ok {
			ts, _ = time.Parse(time.RFC3339, tsStr)
		}
		scriptName, _ := entry["script_name"].(string)
		level, _ := entry["level"].(string)
		eventType, _ := entry["event_type"].(string)
		message, _ := entry["message"].(string)

		entries = append(entries, StartupLogEntry{
			ScriptName: scriptName,
			Level:      level,
			EventType:  eventType,
			Message:    message,
			Timestamp:  ts,
		})
	}
	return entries, nil
}

func (f *StatusFetcher) fetchBrowserSessions(proxies []ProxyInfo) ([]BrowserSession, error) {
	var sessions []BrowserSession

	for _, proxy := range proxies {
		// Use request builder for current page list
		result, err := f.conn.RequestJSON(protocol.VerbCurrentPage, nil, protocol.SubVerbList, proxy.ID)
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

// DaemonBashRunner implements BashRunner using a shared daemon connection.
type DaemonBashRunner struct {
	conn    DaemonClient
	counter atomic.Int64
}

// NewDaemonBashRunner creates a new DaemonBashRunner using a shared connection.
func NewDaemonBashRunner(conn DaemonClient) *DaemonBashRunner {
	return &DaemonBashRunner{
		conn: conn,
	}
}

// platformShell returns the shell command and args for running a command string
// on the current platform.
func platformShell(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", command}
	}
	return "sh", []string{"-c", command}
}

// RunBashCommand runs a bash command via the daemon and returns the process ID.
func (r *DaemonBashRunner) RunBashCommand(command string) (string, error) {
	// Generate unique process ID
	count := r.counter.Add(1)
	processID := fmt.Sprintf("bash-%d-%d", time.Now().Unix(), count)

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	// Run the command via the daemon using request shell
	shell, shellArgs := platformShell(command)
	runConfig := protocol.RunConfig{
		ID:      processID,
		Path:    cwd,
		Mode:    "background",
		Raw:     true,
		Command: shell,
		Args:    shellArgs,
	}

	_, err = r.conn.RequestJSON(protocol.VerbRunJSON, runConfig)
	if err != nil {
		return "", fmt.Errorf("failed to run command: %w", err)
	}

	return processID, nil
}

// DaemonOutputFetcher implements ProcessOutputFetcher using a shared daemon connection.
type DaemonOutputFetcher struct {
	conn        DaemonClient
	projectPath string
}

// NewDaemonOutputFetcher creates a new DaemonOutputFetcher using a shared connection.
func NewDaemonOutputFetcher(conn DaemonClient) *DaemonOutputFetcher {
	projectPath, err := os.Getwd()
	if err != nil {
		projectPath = ""
	}
	return &DaemonOutputFetcher{
		conn:        conn,
		projectPath: projectPath,
	}
}

// GetProcessOutput fetches the last N lines of output for a process.
func (f *DaemonOutputFetcher) GetProcessOutput(processID string, tailLines int) (string, error) {
	output, err := f.conn.RequestString(protocol.VerbProc, protocol.SubVerbOutput, processID, "stream=combined", fmt.Sprintf("tail=%d", tailLines))
	if err != nil {
		return "", err
	}
	return output, nil
}

// GetScriptOutput fetches the last N lines of output for a script by name.
func (f *DaemonOutputFetcher) GetScriptOutput(scriptName string, tailLines int) (string, error) {
	payload := map[string]interface{}{
		"directory": f.projectPath,
	}
	if tailLines > 0 {
		payload["tail"] = tailLines
	}
	result, err := f.conn.RequestJSON(protocol.VerbScript, payload, protocol.SubVerbOutput, scriptName)
	if err != nil {
		return "", err
	}

	linesRaw, ok := result["lines"].([]interface{})
	if !ok {
		return "", nil
	}

	lines := make([]string, 0, len(linesRaw))
	for _, l := range linesRaw {
		if s, ok := l.(string); ok {
			lines = append(lines, s)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// DaemonScriptController implements ScriptController using a shared daemon connection.
type DaemonScriptController struct {
	conn        DaemonClient
	projectPath string
}

// NewDaemonScriptController creates a new DaemonScriptController.
func NewDaemonScriptController(conn DaemonClient) *DaemonScriptController {
	projectPath, err := os.Getwd()
	if err != nil {
		projectPath = ""
	}
	return &DaemonScriptController{
		conn:        conn,
		projectPath: projectPath,
	}
}

// StopScript stops a script by name.
func (c *DaemonScriptController) StopScript(name string) error {
	_, err := c.conn.RequestJSON(protocol.VerbScript, map[string]interface{}{"directory": c.projectPath}, protocol.SubVerbStop, name)
	return err
}

// RestartScript restarts a script by name.
func (c *DaemonScriptController) RestartScript(name string) error {
	_, err := c.conn.RequestJSON(protocol.VerbScript, map[string]interface{}{"directory": c.projectPath}, protocol.SubVerbRestart, name)
	return err
}

// StartScript starts a stopped script by name (uses restart which handles both cases).
func (c *DaemonScriptController) StartScript(name string) error {
	_, err := c.conn.RequestJSON(protocol.VerbScript, map[string]interface{}{"directory": c.projectPath}, protocol.SubVerbRestart, name)
	return err
}

// RunCommand runs an ad-hoc shell command as a background process.
func (c *DaemonScriptController) RunCommand(command string) error {
	_, err := c.conn.RequestJSON("RUN-JSON", map[string]interface{}{
		"command":   command,
		"mode":      "background",
		"directory": c.projectPath,
	}, "", "")
	return err
}
