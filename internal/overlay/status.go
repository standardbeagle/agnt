package overlay

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// tailscaleDNS holds the cached Tailscale DNS name using lock-free atomics.
// Refresh happens asynchronously — callers always get the current (possibly stale) value.
var (
	tailscaleDNSPtr   atomic.Pointer[string] // cached DNS name (nil = never fetched)
	tailscaleDNSTime  atomic.Int64           // unix nanos of last successful refresh
	tailscaleDNSBusy  atomic.Bool            // true while a refresh goroutine is running
	tailscaleCacheTTL = 5 * time.Minute      // re-check every 5 minutes

	// tailscaleDetectFunc is the function that performs the actual DNS detection.
	// Replaced in tests to avoid exec calls.
	tailscaleDetectFunc = detectTailscaleDNS
)

// getTailscaleDNS returns the Tailscale DNS name if available, or empty string if not.
// This is lock-free: it reads from an atomic pointer and triggers background refresh
// when the cache expires. During refresh, callers receive the stale cached value.
func getTailscaleDNS() string {
	// Read current cached value (may be nil on first call)
	cached := tailscaleDNSPtr.Load()

	// Check if cache is still fresh
	lastRefresh := tailscaleDNSTime.Load()
	if lastRefresh > 0 && time.Since(time.Unix(0, lastRefresh)) < tailscaleCacheTTL {
		if cached != nil {
			return *cached
		}
		return ""
	}

	// Cache expired or never populated — trigger async refresh if not already running
	if tailscaleDNSBusy.CompareAndSwap(false, true) {
		detectFn := tailscaleDetectFunc // capture before goroutine to avoid race with test overrides
		go func() {
			defer tailscaleDNSBusy.Store(false)
			dnsName := detectFn()
			tailscaleDNSPtr.Store(&dnsName)
			tailscaleDNSTime.Store(time.Now().UnixNano())
		}()
	}

	// Return current value while refresh runs (stale is acceptable for DNS names)
	if cached != nil {
		return *cached
	}
	return ""
}

// detectTailscaleDNS runs tailscale status to get the DNS name.
// Delegates to platform.TailscaleDNSName so the same detection is reusable
// from packages (e.g. internal/tunnel) that cannot import overlay.
func detectTailscaleDNS() string {
	return platform.TailscaleDNSName(context.Background())
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

	// Fetch listening ports + orphaned process groups
	ports, orphans, err := f.fetchPorts()
	if err == nil {
		status.Ports = ports
		status.Orphans = orphans
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

// fetchPorts queries the daemon's PORTS inventory: every listening TCP port
// with a managed/unmanaged/conflict classification, plus orphaned process
// groups. Both slices are nil on error.
func (f *StatusFetcher) fetchPorts() ([]PortInfo, []OrphanInfo, error) {
	result, err := f.conn.RequestJSON(protocol.VerbPorts, protocol.DirectoryFilter{Directory: f.projectPath}, protocol.SubVerbQuery)
	if err != nil {
		return nil, nil, err
	}

	var ports []PortInfo
	if raw, ok := result["ports"].([]interface{}); ok {
		ports = make([]PortInfo, 0, len(raw))
		for _, p := range raw {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			info := PortInfo{}
			if v, ok := pm["port"].(float64); ok {
				info.Port = int(v)
			}
			if v, ok := pm["pid"].(float64); ok {
				info.PID = int(v)
			}
			if v, ok := pm["name"].(string); ok {
				info.Name = v
			}
			if v, ok := pm["windows"].(bool); ok {
				info.Windows = v
			}
			if v, ok := pm["status"].(string); ok {
				info.Status = v
			}
			if v, ok := pm["system"].(bool); ok {
				info.System = v
			}
			ports = append(ports, info)
		}
	}

	var orphans []OrphanInfo
	if raw, ok := result["orphans"].([]interface{}); ok {
		orphans = make([]OrphanInfo, 0, len(raw))
		for _, o := range raw {
			om, ok := o.(map[string]interface{})
			if !ok {
				continue
			}
			info := OrphanInfo{}
			if v, ok := om["pgid"].(float64); ok {
				info.PGID = int(v)
			}
			if v, ok := om["count"].(float64); ok {
				info.Count = int(v)
			}
			orphans = append(orphans, info)
		}
	}

	return ports, orphans, nil
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

		// Readiness-gate state: the daemon emits "status":
		// "waiting_for_dependencies" for gated proxies and a
		// "waiting_for" array with the pending script names.
		if state, ok := pm["status"].(string); ok {
			info.State = state
		}
		if waitingRaw, ok := pm["waiting_for"].([]interface{}); ok {
			for _, v := range waitingRaw {
				if s, ok := v.(string); ok {
					info.WaitingOn = append(info.WaitingOn, s)
				}
			}
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
	// Pass directory so the project-scope gate (resolveProjectScope) can scope
	// the query. Without it, a non-session-bound overlay connection fails the
	// scope check and the log panel comes back empty — every other fetcher
	// (scripts/processes/proxies/ports) already passes directory for this reason.
	result, err := f.conn.RequestJSON(protocol.VerbAlerts, map[string]interface{}{
		"limit":     100,
		"directory": f.projectPath,
	}, protocol.SubVerbStartupLog)
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

// KillPort kills whatever process is listening on the given TCP port. Reuses
// the existing PROC CLEANUP-PORT handler (process-group SIGTERM→SIGKILL, with
// WSL taskkill.exe routing for Windows-side owners).
func (c *DaemonScriptController) KillPort(port int) error {
	_, err := c.conn.RequestJSON(protocol.VerbProc, map[string]interface{}{"directory": c.projectPath}, protocol.SubVerbCleanupPort, fmt.Sprintf("%d", port))
	return err
}

// CleanOrphans reaps orphaned process groups (leader dead, members alive).
func (c *DaemonScriptController) CleanOrphans() error {
	_, err := c.conn.RequestJSON(protocol.VerbPorts, map[string]interface{}{"directory": c.projectPath}, protocol.SubVerbCleanOrphans)
	return err
}
