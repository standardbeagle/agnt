package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/go-cli-server/client"
	"github.com/standardbeagle/go-cli-server/socket"
)

// hookSendDeadline bounds the total wall time an `agnt hook` CLI invocation
// will spend talking to the daemon. Claude Code blocks on hook exit, so this
// number is visible as added latency on every tool call. 50ms is the agreed
// upper bound — anything longer and the CLI hits one of the sentinel errors
// below and falls through to silent exit 0 so the hook remains invisible.
const hookSendDeadline = 50 * time.Millisecond

// ErrHookDaemonDown is returned from HookSend when the daemon socket is not
// reachable (connection refused, socket file missing, etc). The CLI maps
// this to silent exit 0 — hooks are fire-and-forget and a missing daemon is
// not a user-visible failure.
var ErrHookDaemonDown = errors.New("agnt hook: daemon not reachable")

// ErrHookDeadline is returned from HookSend when the write or ack did not
// complete inside hookSendDeadline. The CLI maps this to silent exit 0 —
// dropping a hook silently is far better than blocking Claude's tool call.
var ErrHookDeadline = errors.New("agnt hook: daemon enqueue deadline exceeded")

// Client is a client for communicating with the daemon over the socket.
// This wraps go-cli-server/client.Conn with agnt-specific methods.
type Client struct {
	conn *client.Conn
}

// clientConfig holds options for creating a client.
type clientConfig struct {
	socketPath string
	timeout    time.Duration
}

// ClientOption configures a Client.
type ClientOption func(*clientConfig)

// WithSocketPath sets the socket path for the client.
func WithSocketPath(path string) ClientOption {
	return func(c *clientConfig) {
		c.socketPath = path
	}
}

// WithTimeout sets the default timeout for operations.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) {
		c.timeout = d
	}
}

// NewClient creates a new daemon client.
func NewClient(opts ...ClientOption) *Client {
	cfg := &clientConfig{
		socketPath: DefaultSocketPath(),
		timeout:    30 * time.Second,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	conn := client.NewConn(
		client.WithSocketPath(cfg.socketPath),
		client.WithTimeout(cfg.timeout),
	)

	return &Client{conn: conn}
}

// NewClientWithPath creates a new daemon client with a specific socket path.
func NewClientWithPath(socketPath string) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	return &Client{
		conn: client.NewConn(
			client.WithSocketPath(socketPath),
			client.WithTimeout(30*time.Second),
		),
	}
}

// Connect connects to the daemon.
func (c *Client) Connect() error {
	debug.Log("client", "Connect: socket=%s", c.conn.SocketPath())
	err := c.conn.EnsureConnected()
	if err != nil {
		debug.Log("client", "Connect failed: %v", err)
	}
	return err
}

// Close closes the connection to the daemon.
func (c *Client) Close() error {
	return c.conn.Close()
}

// IsConnected returns whether the client is connected.
func (c *Client) IsConnected() bool {
	return c.conn.IsConnected()
}

// SocketPath returns the socket path.
func (c *Client) SocketPath() string {
	return c.conn.SocketPath()
}

// Ping sends a ping to the daemon and waits for a pong response.
func (c *Client) Ping() error {
	return c.conn.Ping()
}

// Info retrieves daemon information.
// Uses STATUS command to get full daemon info (Hub's INFO is minimal).
// Falls back to INFO if STATUS is not available (for backwards compatibility).
func (c *Client) Info() (*DaemonInfo, error) {
	debug.Log("client", "Info: requesting daemon status")
	// Try STATUS first (returns full daemon info)
	result, err := c.conn.Request(protocol.VerbStatus).JSON()
	if err != nil {
		debug.Log("client", "STATUS failed, falling back to INFO: %v", err)
		// Fall back to INFO for older daemons that don't have STATUS
		result, err = c.conn.Request(protocol.VerbInfo).JSON()
		if err != nil {
			debug.Log("client", "Info failed: %v", err)
			return nil, err
		}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result: %w", err)
	}

	var info DaemonInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal info: %w", err)
	}

	return &info, nil
}

// Shutdown requests the daemon to shut down.
func (c *Client) Shutdown() error {
	return c.conn.Request(protocol.VerbShutdown).OK()
}

// Detect detects the project type at the given path.
func (c *Client) Detect(path string) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbDetect)
	if path != "" && path != "." {
		req = c.conn.Request(protocol.VerbDetect, path)
	}
	return req.JSON()
}

// Run starts a process on the daemon.
// The config is marshaled to JSON and can be protocol.RunConfig or any struct
// that embeds it (e.g., with additional fields like no_auto_restart).
func (c *Client) Run(config interface{}) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbRunJSON).WithJSON(config).JSON()
}

// ProcRunConfig holds the JSON payload for PROC RUN. Mirrors the
// procRunPayload struct in hub_proc.go — kept in sync with the wire
// contract (extra / renamed fields require updating both sides).
type ProcRunConfig struct {
	Run         string            `json:"run,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URLMatchers []string          `json:"url_matchers,omitempty"`
	AutoRestart bool              `json:"auto_restart,omitempty"`
	ProjectPath string            `json:"project_path,omitempty"`
	// DependsOn lists script names this process must wait for before
	// launching. Empty means start immediately.
	DependsOn []string `json:"depends_on,omitempty"`
	// DependsOnTimeout is the per-process upper bound on the dep wait
	// in seconds. Zero (or omitted) → 30s default.
	DependsOnTimeout int `json:"depends_on_timeout,omitempty"`
}

// ProcRun starts an admin-aware process via PROC RUN. Unlike the top-level
// RUN verb (Client.Run), PROC RUN routes through the daemon's
// StartScriptExplicit so the new process becomes a process-kind admin
// registry entry visible in SCRIPT LIST and the overlay admin screen.
func (c *Client) ProcRun(name string, cfg ProcRunConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, "RUN", name).WithJSON(cfg).JSON()
}

// ProcRunGroupConfig holds the JSON payload for PROC RUN-GROUP. Mirrors
// the procRunGroupPayload struct in hub_proc.go.
type ProcRunGroupConfig struct {
	ProjectPath      string         `json:"project_path,omitempty"`
	DependsOnTimeout int            `json:"depends_on_timeout,omitempty"`
	Processes        []GroupProcess `json:"processes"`
}

// ProcRunGroup launches a multi-process startup group via PROC RUN-GROUP.
// Cycle detection runs before any process launches; on cycle detection
// the request returns ErrInvalidArgs with the cycle description. Per-
// process kickoff results are returned in declaration order; agents poll
// PROC STATUS to observe individual processes transitioning from
// "pending" → "starting" → "running".
func (c *Client) ProcRunGroup(cfg ProcRunGroupConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, protocol.SubVerbRunGroup).WithJSON(cfg).JSON()
}

// ProcStatus gets the status of a process.
func (c *Client) ProcStatus(processID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, protocol.SubVerbStatus, processID).JSON()
}

// ProcOutput gets the output of a process.
func (c *Client) ProcOutput(processID string, filter protocol.OutputFilter) (string, error) {
	args := []string{protocol.SubVerbOutput, processID}

	// Add filter args
	if filter.Stream != "" && filter.Stream != "combined" {
		args = append(args, fmt.Sprintf("stream=%s", filter.Stream))
	}
	if filter.Tail > 0 {
		args = append(args, fmt.Sprintf("tail=%d", filter.Tail))
	}
	if filter.Head > 0 {
		args = append(args, fmt.Sprintf("head=%d", filter.Head))
	}
	if filter.Grep != "" {
		args = append(args, fmt.Sprintf("grep=%s", filter.Grep))
	}
	if filter.GrepV {
		args = append(args, "grep_v")
	}

	return c.conn.Request(protocol.VerbProc, args...).String()
}

// ProcStop stops a process.
func (c *Client) ProcStop(processID string, force bool) (map[string]interface{}, error) {
	args := []string{protocol.SubVerbStop, processID}
	if force {
		args = append(args, "force")
	}
	return c.conn.Request(protocol.VerbProc, args...).JSON()
}

// ProcList lists all processes.
func (c *Client) ProcList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbProc, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// ProcCleanupPort kills processes on a specific port.
func (c *Client) ProcCleanupPort(port int) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, protocol.SubVerbCleanupPort, fmt.Sprintf("%d", port)).JSON()
}

// ProxyStartConfig holds configuration for starting a proxy.
type ProxyStartConfig struct {
	Path          string                 `json:"path,omitempty"`
	BindAddress   string                 `json:"bind_address,omitempty"`
	AllowExternal bool                   `json:"allow_external,omitempty"`
	PublicURL     string                 `json:"public_url,omitempty"`
	SkipTLSVerify bool                   `json:"skip_tls_verify,omitempty"`
	Tunnel        *protocol.TunnelConfig `json:"tunnel,omitempty"`
}

// ProxyStart starts a reverse proxy.
func (c *Client) ProxyStart(id, targetURL string, port, maxLogSize int, path string) (map[string]interface{}, error) {
	return c.ProxyStartWithConfig(id, targetURL, port, maxLogSize, ProxyStartConfig{Path: path})
}

// ProxyStartWithConfig starts a reverse proxy with extended configuration.
func (c *Client) ProxyStartWithConfig(id, targetURL string, port, maxLogSize int, config ProxyStartConfig) (map[string]interface{}, error) {
	args := []string{protocol.SubVerbStart, id, targetURL, fmt.Sprintf("%d", port)}
	if maxLogSize > 0 {
		args = append(args, fmt.Sprintf("%d", maxLogSize))
	}
	return c.conn.Request(protocol.VerbProxy, args...).WithJSON(config).JSON()
}

// ProxyStop stops a reverse proxy.
func (c *Client) ProxyStop(id string) error {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbStop, id).OK()
}

// ProxyStatus gets the status of a proxy.
func (c *Client) ProxyStatus(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbStatus, id).JSON()
}

// ProxyList lists all proxies.
func (c *Client) ProxyList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbProxy, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// ProxyExec executes JavaScript in connected browsers.
func (c *Client) ProxyExec(id, code string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbExec, id).WithData([]byte(code)).JSON()
}

// ProxyToast sends a toast notification to connected browsers.
func (c *Client) ProxyToast(id string, toast protocol.ToastConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbToast, id).WithJSON(toast).JSON()
}

// ProxyLogQuery queries proxy logs.
func (c *Client) ProxyLogQuery(proxyID string, filter protocol.LogQueryFilter) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxyLog, protocol.SubVerbQuery, proxyID).WithJSON(filter).JSON()
}

// ProxyLogClear clears proxy logs.
func (c *Client) ProxyLogClear(proxyID string) error {
	return c.conn.Request(protocol.VerbProxyLog, protocol.SubVerbClear, proxyID).OK()
}

// ProxyLogStats gets proxy log statistics.
func (c *Client) ProxyLogStats(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxyLog, protocol.SubVerbStats, proxyID).JSON()
}

// CurrentPageList lists active page sessions.
func (c *Client) CurrentPageList(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbCurrentPage, protocol.SubVerbList, proxyID).JSON()
}

// CurrentPageGet gets details for a specific page session.
func (c *Client) CurrentPageGet(proxyID, sessionID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbCurrentPage, protocol.SubVerbGet, proxyID, sessionID).JSON()
}

// CurrentPageClear clears page sessions.
func (c *Client) CurrentPageClear(proxyID string) error {
	return c.conn.Request(protocol.VerbCurrentPage, protocol.SubVerbClear, proxyID).OK()
}

// OverlaySet sets the overlay endpoint URL.
func (c *Client) OverlaySet(endpoint string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbOverlay, protocol.SubVerbSet, endpoint).JSON()
}

// OverlayGet gets the current overlay endpoint configuration.
func (c *Client) OverlayGet() (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbOverlay, protocol.SubVerbGet).JSON()
}

// OverlayClear clears the overlay endpoint configuration.
func (c *Client) OverlayClear() error {
	return c.conn.Request(protocol.VerbOverlay, protocol.SubVerbClear).OK()
}

// BroadcastActivity broadcasts an activity state update to connected browsers via specified proxies.
func (c *Client) BroadcastActivity(active bool, proxyIDs ...string) error {
	activeStr := "false"
	if active {
		activeStr = "true"
	}
	args := append([]string{protocol.SubVerbActivity, activeStr}, proxyIDs...)
	_, err := c.conn.Request(protocol.VerbOverlay, args...).JSON()
	return err
}

// BroadcastOutputPreview broadcasts output preview lines to connected browsers via proxies.
func (c *Client) BroadcastOutputPreview(lines []string, proxyIDs ...string) error {
	payload := map[string]interface{}{
		"lines":     lines,
		"proxy_ids": proxyIDs,
	}
	_, err := c.conn.Request(protocol.VerbOverlay, protocol.SubVerbOutputPreview).WithJSON(payload).JSON()
	return err
}

// TunnelStart starts a tunnel for a local port.
func (c *Client) TunnelStart(config protocol.TunnelStartConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbTunnel, protocol.SubVerbStart).WithJSON(config).JSON()
}

// TunnelStop stops a running tunnel.
func (c *Client) TunnelStop(id string) error {
	return c.conn.Request(protocol.VerbTunnel, protocol.SubVerbStop, id).OK()
}

// TunnelStatus gets the status of a tunnel.
func (c *Client) TunnelStatus(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbTunnel, protocol.SubVerbStatus, id).JSON()
}

// TunnelList lists all active tunnels.
func (c *Client) TunnelList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbTunnel, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// BrowserStart starts a browser instance.
func (c *Client) BrowserStart(config protocol.BrowserStartConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbBrowser, protocol.SubVerbStart).WithJSON(config).JSON()
}

// BrowserStop stops a running browser instance.
func (c *Client) BrowserStop(id string) error {
	return c.conn.Request(protocol.VerbBrowser, protocol.SubVerbStop, id).OK()
}

// BrowserStatus gets the status of a browser instance.
func (c *Client) BrowserStatus(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbBrowser, protocol.SubVerbStatus, id).JSON()
}

// BrowserList lists all active browser instances.
func (c *Client) BrowserList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbBrowser, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// AutomationStart starts a chromedp automation session.
func (c *Client) AutomationStart(config protocol.AutomationStartConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutomation, protocol.SubVerbStart).WithJSON(config).JSON()
}

// AutomationStop stops an automation session.
func (c *Client) AutomationStop(id string) error {
	return c.conn.Request(protocol.VerbAutomation, protocol.SubVerbStop, id).OK()
}

// AutomationStatus gets the status of an automation session.
func (c *Client) AutomationStatus(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutomation, protocol.SubVerbStatus, id).JSON()
}

// AutomationList lists all active automation sessions.
func (c *Client) AutomationList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbAutomation, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// AutomationScreenshot takes a screenshot in an automation session.
func (c *Client) AutomationScreenshot(config protocol.AutomationScreenshotConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutomation, protocol.SubVerbScreenshot).WithJSON(config).JSON()
}

// AutomationNavigate navigates to a URL in an automation session.
func (c *Client) AutomationNavigate(config protocol.AutomationNavigateConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutomation, protocol.SubVerbNavigate).WithJSON(config).JSON()
}

// AutomationEvaluate evaluates JavaScript in an automation session.
func (c *Client) AutomationEvaluate(config protocol.AutomationEvaluateConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutomation, protocol.SubVerbEvaluate).WithJSON(config).JSON()
}

// ChaosEnable enables chaos injection on a proxy.
func (c *Client) ChaosEnable(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbEnable, proxyID).JSON()
}

// ChaosDisable disables chaos injection on a proxy.
func (c *Client) ChaosDisable(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbDisable, proxyID).JSON()
}

// ChaosStatus gets the chaos status of a proxy.
func (c *Client) ChaosStatus(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbStatus, proxyID).JSON()
}

// ChaosPreset applies a preset chaos configuration to a proxy.
func (c *Client) ChaosPreset(proxyID, preset string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbPreset, proxyID, preset).JSON()
}

// ChaosSet sets the full chaos configuration on a proxy.
func (c *Client) ChaosSet(proxyID string, config protocol.ChaosConfigPayload) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbSet, proxyID).WithJSON(config).JSON()
}

// ChaosAddRule adds a single rule to a proxy's chaos engine.
func (c *Client) ChaosAddRule(proxyID string, rule protocol.ChaosRuleConfig) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbAddRule, proxyID).WithJSON(rule).JSON()
}

// ChaosRemoveRule removes a rule from a proxy's chaos engine.
func (c *Client) ChaosRemoveRule(proxyID, ruleID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbRemoveRule, proxyID, ruleID).JSON()
}

// ChaosListRules lists all chaos rules for a proxy.
func (c *Client) ChaosListRules(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbListRules, proxyID).JSON()
}

// ChaosStats gets chaos statistics for a proxy.
func (c *Client) ChaosStats(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbStats, proxyID).JSON()
}

// ChaosClear clears all chaos rules and resets stats for a proxy.
func (c *Client) ChaosClear(proxyID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, protocol.SubVerbClear, proxyID).JSON()
}

// ChaosListPresets returns the list of available chaos presets.
func (c *Client) ChaosListPresets() (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbChaos, "LIST-PRESETS").JSON()
}

// SessionRegister registers a new session with the daemon.
func (c *Client) SessionRegister(code string, overlayPath string, projectPath string, command string, args []string) (map[string]interface{}, error) {
	return c.SessionRegisterWithPGID(code, overlayPath, projectPath, command, args, 0)
}

// SessionRegisterWithPGID registers a new session and reports the POSIX
// process group ID of the PTY child (the session leader). On session
// shutdown the daemon will `killpg(sessionPGID, SIGTERM)` to reap every
// descendant the session produced — including background jobs the coding
// agent spawned via non-interactive bash. Pass 0 for the PGID on Windows
// or when the caller has no pgid to share.
func (c *Client) SessionRegisterWithPGID(code string, overlayPath string, projectPath string, command string, args []string, sessionPGID int) (map[string]interface{}, error) {
	return c.SessionRegisterWithContainment(code, overlayPath, projectPath, command, args, sessionPGID, 0)
}

// SessionRegisterWithContainment registers a new session and reports
// both the Unix session pgid and the Windows Job Object handle for the
// PTY child subtree. Callers on Unix pass sessionPGID and 0 for
// sessionJobHandle; callers on Windows pass 0 for sessionPGID and the
// uint64 form of a windows.Handle for sessionJobHandle. The daemon
// invokes whichever containment path matches the running OS at
// cleanup time; both are safe no-ops when their corresponding field
// is unset.
func (c *Client) SessionRegisterWithContainment(code string, overlayPath string, projectPath string, command string, args []string, sessionPGID int, sessionJobHandle uint64) (map[string]interface{}, error) {
	metadata := protocol.SessionRegisterConfig{
		OverlayPath:      overlayPath,
		ProjectPath:      projectPath,
		Command:          command,
		Args:             args,
		SessionPGID:      sessionPGID,
		SessionJobHandle: sessionJobHandle,
	}
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbRegister, code, overlayPath).WithJSON(metadata).JSON()
}

// SessionUnregister unregisters a session from the daemon.
func (c *Client) SessionUnregister(code string) error {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbUnregister, code).OK()
}

// SessionHeartbeat sends a heartbeat for a session.
func (c *Client) SessionHeartbeat(code string) error {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbHeartbeat, code).OK()
}

// SessionList lists active sessions.
func (c *Client) SessionList(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbSession, protocol.SubVerbList)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// SessionGet retrieves a specific session.
func (c *Client) SessionGet(code string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbGet, code).JSON()
}

// SessionSend sends an immediate message to a session.
func (c *Client) SessionSend(code string, message string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbSend, code).WithData([]byte(message)).JSON()
}

// SessionSchedule schedules a message for future delivery.
func (c *Client) SessionSchedule(code string, duration string, message string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbSchedule, code, duration).WithData([]byte(message)).JSON()
}

// SessionCancel cancels a scheduled task.
func (c *Client) SessionCancel(taskID string) error {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbCancel, taskID).OK()
}

// SessionTasks lists scheduled tasks.
func (c *Client) SessionTasks(dirFilter protocol.DirectoryFilter) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbSession, protocol.SubVerbTasks)
	if dirFilter.Directory != "" || dirFilter.Global {
		req = req.WithJSON(dirFilter)
	}
	return req.JSON()
}

// SessionGenerateCode requests a new session code from the daemon.
func (c *Client) SessionGenerateCode(command string) (string, error) {
	return fmt.Sprintf("%s-%d", command, time.Now().UnixNano()%10000), nil
}

// SessionFind finds a session by directory ancestry.
func (c *Client) SessionFind(directory string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbFind, directory).JSON()
}

// SessionAttach attaches to a session found by directory ancestry.
func (c *Client) SessionAttach(directory string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbSession, protocol.SubVerbAttach, directory).JSON()
}

// SessionURL reports a detected URL from an agnt run session.
// This triggers proxy creation for any matching proxy configurations.
func (c *Client) SessionURL(code string, url string, scriptName string) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbSession, protocol.SubVerbURL, code, url)
	if scriptName != "" {
		req = req.WithJSON(map[string]string{"script": scriptName})
	}
	return req.JSON()
}

// StoreGet retrieves a value from the key-value store.
func (c *Client) StoreGet(req protocol.StoreGetRequest) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbGet).WithJSON(req).JSON()
}

// StoreSet stores a value in the key-value store.
func (c *Client) StoreSet(req protocol.StoreSetRequest) error {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbSet).WithJSON(req).OK()
}

// StoreDelete deletes a value from the key-value store.
func (c *Client) StoreDelete(req protocol.StoreDeleteRequest) error {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbDelete).WithJSON(req).OK()
}

// StoreList lists all keys in a scope.
func (c *Client) StoreList(req protocol.StoreListRequest) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbList).WithJSON(req).JSON()
}

// StoreClear clears all entries in a scope.
func (c *Client) StoreClear(req protocol.StoreClearRequest) error {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbClear).WithJSON(req).OK()
}

// StoreGetAll retrieves all key-value pairs in a scope.
func (c *Client) StoreGetAll(req protocol.StoreGetAllRequest) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbStore, protocol.SubVerbGetAll).WithJSON(req).JSON()
}

// StopAll stops all running processes, proxies, and tunnels.
func (c *Client) StopAll() (map[string]interface{}, error) {
	return c.conn.Request("STOP-ALL").JSON()
}

// RestartAll restarts all running processes and proxies with their original configurations.
func (c *Client) RestartAll() (map[string]interface{}, error) {
	return c.conn.Request("RESTART-ALL").JSON()
}

// ProcRestart restarts a single process by ID.
func (c *Client) ProcRestart(processID string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProc, protocol.SubVerbRestart, processID).JSON()
}

// ProcAutoRestartConfig holds configuration for process auto-restart.
type ProcAutoRestartConfig struct {
	MaxRestarts    int  `json:"max_restarts,omitempty"`
	OnlyOnError    bool `json:"only_on_error,omitempty"`
	RestartDelayMs int  `json:"restart_delay_ms,omitempty"`
}

// ProcAutoRestart enables, disables, or queries auto-restart for a process.
// action can be "enable", "disable", or "status".
func (c *Client) ProcAutoRestart(processID, action string, config *ProcAutoRestartConfig) (map[string]interface{}, error) {
	req := c.conn.Request(protocol.VerbProc, "AUTORESTART", processID, action)
	if config != nil {
		req = req.WithJSON(config)
	}
	return req.JSON()
}

// ProxyRestart restarts a single proxy by ID.
func (c *Client) ProxyRestart(id string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbProxy, protocol.SubVerbRestart, id).JSON()
}

// AlertReport sends an alert report to the daemon.
func (c *Client) AlertReport(payload protocol.AlertReportPayload) error {
	return c.conn.Request(protocol.VerbAlerts, protocol.SubVerbReport).WithJSON(payload).OK()
}

// AlertQuery queries alerts from the daemon.
func (c *Client) AlertQuery(filter protocol.AlertQueryFilter) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAlerts, protocol.SubVerbQuery).WithJSON(filter).JSON()
}

// IncidentQuery queries the incident inbox for the current session.
func (c *Client) IncidentQuery(filter protocol.IncidentQueryFilter) (*protocol.IncidentQueryResult, error) {
	raw, err := c.conn.Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(filter).JSON()
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var result protocol.IncidentQueryResult
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AlertClear clears all alerts from the daemon.
func (c *Client) AlertClear() error {
	return c.conn.Request(protocol.VerbAlerts, protocol.SubVerbClear).OK()
}

// StartupLog queries the startup log from the daemon.
func (c *Client) StartupLog(limit int) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAlerts, protocol.SubVerbStartupLog).
		WithJSON(map[string]interface{}{"limit": limit}).JSON()
}

// Doctor runs health checks and returns a diagnostic report.
func (c *Client) Doctor(projectPath string) (map[string]interface{}, error) {
	args := []string{}
	if projectPath != "" {
		args = append(args, projectPath)
	}
	return c.conn.Request(protocol.VerbDoctor, args...).JSON()
}

// ScriptList lists all scripts for a project directory.
func (c *Client) ScriptList(projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbScript, protocol.SubVerbList).
		WithJSON(map[string]interface{}{"directory": projectPath}).JSON()
}

// ScriptGet retrieves full detail for a named script.
func (c *Client) ScriptGet(name, projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbScript, protocol.SubVerbGet, name).
		WithJSON(map[string]interface{}{"directory": projectPath}).JSON()
}

// ScriptOutput retrieves output history for a named script.
func (c *Client) ScriptOutput(name, projectPath string, tail int) (map[string]interface{}, error) {
	payload := map[string]interface{}{"directory": projectPath}
	if tail > 0 {
		payload["tail"] = tail
	}
	return c.conn.Request(protocol.VerbScript, protocol.SubVerbOutput, name).
		WithJSON(payload).JSON()
}

// ScriptRestart restarts a script by name.
func (c *Client) ScriptRestart(name, projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbScript, protocol.SubVerbRestart, name).
		WithJSON(map[string]interface{}{"directory": projectPath}).JSON()
}

// ScriptStop stops a script by name.
func (c *Client) ScriptStop(name, projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbScript, protocol.SubVerbStop, name).
		WithJSON(map[string]interface{}{"directory": projectPath}).JSON()
}

// AutostartClearPorts kills port blockers and resumes autostart for a project.
func (c *Client) AutostartClearPorts(projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutostart, protocol.SubVerbClearPorts, projectPath).JSON()
}

// AutostartContinue resumes autostart without killing port blockers.
func (c *Client) AutostartContinue(projectPath string) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAutostart, protocol.SubVerbContinue, projectPath).JSON()
}

// AutostartRun triggers a non-interactive autostart run for a project path.
// Used by the MCP InitializedHandler in channel mode. The daemon-side handler
// overrides port-conflict "prompt" to "skip" because there is no stdin.
func (c *Client) AutostartRun(projectPath string) (map[string]interface{}, error) {
	cfg := protocol.AutostartRunConfig{
		ProjectPath:    projectPath,
		NonInteractive: true,
	}
	return c.conn.Request(protocol.VerbAutostart, protocol.SubVerbAutostartRun).WithJSON(cfg).JSON()
}

// HookSend enqueues a Claude Code hook event in the daemon's ring buffer and
// returns as fast as possible. The protocol is fire-and-ack: the daemon's
// verb handler pushes the event into an in-memory ring buffer and replies OK
// synchronously off the drain path, so the CLI's wall-clock cost is
// dominated by the connect + write + single-line read.
//
// The total operation is bounded by hookSendDeadline. On deadline, refused
// connection, or EAGAIN we return a typed sentinel error so the CLI can map
// it to silent exit 0 — hooks must never block Claude's tool call with a
// visible error.
//
// Unlike the rest of this client HookSend does NOT reuse Client.conn. The
// shared Conn does not expose SetWriteDeadline on its underlying socket, and
// the hook hot path needs a deadline strictly scoped to this one call so
// that a slow or wedged daemon on a previous request cannot stretch into the
// hook. A dedicated short-lived socket is the cleanest way to get that
// containment on both Unix and Windows.
func (c *Client) HookSend(ctx context.Context, event string, payloadJSON json.RawMessage, tags map[string]string) error {
	if event == "" {
		return fmt.Errorf("HookSend: event required")
	}

	// Scope the deadline to the caller's context as well. If the caller
	// cancels (e.g. Claude killed the hook process) we want to bail out
	// immediately regardless of the 50ms budget.
	deadline := time.Now().Add(hookSendDeadline)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	conn, err := socket.Connect(c.conn.SocketPath())
	if err != nil {
		return mapHookSendError(err)
	}
	defer conn.Close()

	// Single deadline covers both the outbound write and the OK ack. The
	// daemon handler is non-blocking (ring buffer push + write OK), so the
	// ack ought to return within a microsecond or two of the write
	// completing. If it doesn't, we want to bail out, not wait.
	if err := conn.SetDeadline(deadline); err != nil {
		return mapHookSendError(err)
	}

	payload := protocol.HookPayload{
		Event:   event,
		Payload: payloadJSON,
		Tags:    tags,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("HookSend: marshal payload: %w", err)
	}

	w := protocol.NewWriter(conn)
	if err := w.WriteCommandWithSubVerb(protocol.VerbHook, "", nil, body); err != nil {
		return mapHookSendError(err)
	}

	p := protocol.NewParser(conn)
	resp, err := p.ParseResponse()
	if err != nil {
		return mapHookSendError(err)
	}

	switch resp.Type {
	case protocol.ResponseOK:
		return nil
	case protocol.ResponseErr:
		return fmt.Errorf("HookSend: daemon error: [%s] %s", resp.Code, resp.Message)
	default:
		return fmt.Errorf("HookSend: unexpected response type %q", resp.Type)
	}
}

// mapHookSendError translates a raw network/io error into the appropriate
// sentinel the CLI can test against. Keep this tight: we only want to hide
// the specific "daemon isn't talking to us in time" cases. Anything else
// (e.g. malformed data, server error) falls through as a wrapped error and
// the CLI will surface it on its verbose path.
func mapHookSendError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %v", ErrHookDaemonDown, err)
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("%w: %v", ErrHookDaemonDown, err)
	}
	if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
		return fmt.Errorf("%w: %v", ErrHookDeadline, err)
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return fmt.Errorf("%w: %v", ErrHookDeadline, err)
	}
	// Fall through: unknown error, surface it verbatim so the CLI's
	// verbose path can print it.
	return err
}

// StreamEvents opens a long-lived event stream from the daemon.
// It creates a dedicated connection and calls handler for each event received.
// The stream runs until ctx is cancelled or the connection drops.
func (c *Client) StreamEvents(ctx context.Context, filter protocol.StreamEventFilter, handler func(proxy.LogEntry) error) error {
	conn, err := net.Dial("unix", c.conn.SocketPath())
	if err != nil {
		return fmt.Errorf("stream connect failed: %w", err)
	}
	defer conn.Close()

	w := protocol.NewWriter(conn)
	filterData, _ := json.Marshal(filter)
	if err := w.WriteCommandWithSubVerb(protocol.VerbStreamEvents, "", nil, filterData); err != nil {
		return fmt.Errorf("stream send failed: %w", err)
	}

	p := protocol.NewParser(conn)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		resp, err := p.ParseResponse()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if err == io.EOF {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("stream read failed: %w", err)
		}

		switch resp.Type {
		case protocol.ResponseOK:
			continue
		case protocol.ResponseChunk:
			if len(resp.Data) > 0 {
				if string(resp.Data) == `{"type":"keepalive"}` {
					continue
				}
				var entry proxy.LogEntry
				if err := json.Unmarshal(resp.Data, &entry); err != nil {
					debug.Log("client", "StreamEvents: failed to unmarshal event: %v", err)
					continue
				}
				if err := handler(entry); err != nil {
					return err
				}
			}
		case protocol.ResponseEnd:
			return nil
		case protocol.ResponseErr:
			return fmt.Errorf("server error: [%s] %s", resp.Code, resp.Message)
		}
	}
}
