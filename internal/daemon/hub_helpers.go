package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/browser"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tunnel"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func writeErr(conn *hubpkg.Connection, code hubproto.ErrorCode, component, format string, args ...interface{}) error {
	msg := fmt.Sprintf(format, args...)
	debug.Log(component, "error: %s (code=%v)", msg, code)
	return conn.WriteErr(code, msg)
}

// writeStructuredErr writes a structured error response and logs it for debugging.

func writeStructuredErr(conn *hubpkg.Connection, component string, err *hubproto.StructuredError) error {
	debug.Log(component, "error: %s - %s (code=%v)", err.Command, err.Message, err.Code)
	return conn.WriteStructuredErr(err)
}

// normalizePath normalizes a path for consistent comparison.

func normalizePath(path string) string {
	if path == "" || path == "." {
		return "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	// On Windows, normalize to lowercase for case-insensitive comparison.
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

// getSessionScopedProxy retrieves a proxy with session-scoped fuzzy matching.
// If the connection has an associated session, only proxies in that session's
// project path are considered for fuzzy lookup. Exact ID matches always work.

func (d *Daemon) getSessionScopedProxy(conn *hubpkg.Connection, proxyID string) (*proxy.ProxyServer, error) {
	// Get path filter from connection's session
	pathFilter := ""
	if sessionCode := conn.SessionCode(); sessionCode != "" {
		if session, ok := d.sessionRegistry.Get(sessionCode); ok {
			pathFilter = session.ProjectPath
		}
	}

	return d.proxym.GetWithPathFilter(proxyID, pathFilter)
}

// getSessionScopedTunnel retrieves a tunnel with session-scoped fuzzy matching.
// If the connection has an associated session, only tunnels in that session's
// project path are considered for fuzzy lookup. Exact ID matches always work.

func (d *Daemon) getSessionScopedTunnel(conn *hubpkg.Connection, tunnelID string) (*tunnel.Tunnel, error) {
	// Get path filter from connection's session
	pathFilter := ""
	if sessionCode := conn.SessionCode(); sessionCode != "" {
		if session, ok := d.sessionRegistry.Get(sessionCode); ok {
			pathFilter = session.ProjectPath
		}
	}

	return d.tunnelm.GetWithPathFilter(tunnelID, pathFilter)
}

// getSessionScopedBrowser retrieves a browser with session-scoped fuzzy matching.
// If the connection has an associated session, only browsers in that session's
// project path are considered for fuzzy lookup. Exact ID matches always work.

func (d *Daemon) getSessionScopedBrowser(conn *hubpkg.Connection, browserID string) (*browser.Browser, error) {
	// Get path filter from connection's session
	pathFilter := ""
	if sessionCode := conn.SessionCode(); sessionCode != "" {
		if session, ok := d.sessionRegistry.Get(sessionCode); ok {
			pathFilter = session.ProjectPath
		}
	}

	return d.browserm.GetWithPathFilter(browserID, pathFilter)
}

// getSessionProjectPath returns the project path from the connection's session.

func (d *Daemon) getSessionProjectPath(conn *hubpkg.Connection) string {
	if sessionCode := conn.SessionCode(); sessionCode != "" {
		if session, ok := d.sessionRegistry.Get(sessionCode); ok {
			return session.ProjectPath
		}
	}
	return ""
}

// RogueProcessInfo contains information about a detected rogue process.
type RogueProcessInfo struct {
	Port       int   // The port being used
	PIDs       []int // PIDs of processes using the port
	IsManaged  bool  // Whether any of the PIDs are managed by agnt
	HasWarning bool  // Whether to show warning
}

// detectRogueProcess checks if there's an unmanaged process using the port
// associated with a stopped/failed process. Returns info about the rogue process.

func (d *Daemon) detectRogueProcess(ctx context.Context, proc *goprocess.ManagedProcess) *RogueProcessInfo {
	// Only check stopped/failed processes
	state := proc.State()
	if state != goprocess.StateStopped && state != goprocess.StateFailed {
		return nil
	}

	// Try to determine the expected port
	port := d.getExpectedPortForProcess(proc)
	if port <= 0 {
		return nil
	}

	// Check if port is in use
	pids := config.FindPIDsByPort(ctx, port)
	if len(pids) == 0 {
		return nil
	}

	// Check if any of these PIDs are managed by agnt
	isManaged := false
	for _, pid := range pids {
		if d.hub.ProcessManager().IsManagedPID(pid) {
			isManaged = true
			break
		}
	}

	// If port is in use by unmanaged process, return warning info
	if !isManaged {
		return &RogueProcessInfo{
			Port:       port,
			PIDs:       pids,
			IsManaged:  false,
			HasWarning: true,
		}
	}

	return nil
}

// getExpectedPortForProcess extracts the expected port for a process.
// Checks URLs from URL tracker, then falls back to command-line parsing.

func (d *Daemon) getExpectedPortForProcess(proc *goprocess.ManagedProcess) int {
	// First, check URLs from URL tracker
	urls := d.urlTracker.GetURLs(proc.ID)
	for _, urlStr := range urls {
		if port := extractPortFromURL(urlStr); port > 0 {
			return port
		}
	}

	// Fall back to command-line parsing
	return extractPortFromCommand(proc.Command, proc.Args)
}

// extractPortFromURL extracts a port number from a URL string.

func extractPortFromURL(urlStr string) int {
	// Simple pattern match for localhost URLs with ports
	patterns := []string{
		`localhost:(\d+)`,
		`127\.0\.0\.1:(\d+)`,
		`0\.0\.0\.0:(\d+)`,
		`\[::1\]:(\d+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(urlStr); len(matches) > 1 {
			if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
				return port
			}
		}
	}
	return 0
}

// registerAgntCommands registers agnt-specific commands with the Hub.
// This enables Hub's command dispatch to route these commands to the daemon's handlers.
// Note: Registering a command that Hub already registered will override Hub's handler.

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}

// hubHandleDetect handles the DETECT command.
func (d *Daemon) hubHandleDetect(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "DETECT: args=%v", cmd.Args)
	path := "."
	if len(cmd.Args) > 0 {
		path = cmd.Args[0]
	}

	proj, err := project.Detect(path)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"type":            proj.Type,
		"path":            proj.Path,
		"package_manager": proj.PackageManager,
		"scripts":         project.GetCommandNames(proj),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteJSON(data)
}

// wireProxyLogger connects a proxy's traffic logger to the alert hub
// so that log entries are broadcast to active stream sinks.
//
// The TrafficLogger ring buffer always receives the entry first (the
// ring write happens upstream in TrafficLogger.LogEntry, before the
// SetOnLogEntry callback runs), so `proxylog query` continues to surface
// suppressed entries on demand.
//
// Suppression contract: when the proxy is linked to a process that is in
// a transient/unhealthy state (Starting, Stopping, Failed, or within the
// 5s grace window after returning to Running), the gate drops the
// broadcast to the alert hub. See proxyBroadcastGate for the gate logic
// and internal/daemon/health_tracker.go for the full state matrix.
func (d *Daemon) wireProxyLogger(server *proxy.ProxyServer) {
	if d.alertHub == nil {
		return
	}
	proxyID := server.ID
	server.Logger().SetOnLogEntry(func(entry proxy.LogEntry) {
		if !d.proxyBroadcastGate(proxyID, entry) {
			return
		}
		d.alertHub.BroadcastLogEntry(entry, proxyID)
	})
}

// proxyBroadcastGate returns true if entry should be forwarded to the
// alert hub for proxyID, false if it should be suppressed. Diagnostic
// entries always pass through (they're how the daemon communicates the
// suppression state itself to the agent). Extracted from wireProxyLogger
// so it can be unit-tested without spinning up a real ProxyServer.
//
// The gate consults the OutageClassifier for tri-state suppression:
//
//	ModeOff             — broadcast normally
//	ModeFull            — drop everything except diagnostics
//	ModeDiagnosticOnly  — drop error-class entries, forward warnings
//
// HealthTracker.IsInSuppressionWindow remains the upstream signal: the
// classifier reads its bookkeeping (lastObservedState, lastHealthyAt,
// outageStartedAt) which is populated as a side-effect of the call.
// We invoke it once for its side-effects and then read SuppressionMode.
func (d *Daemon) proxyBroadcastGate(proxyID string, entry proxy.LogEntry) bool {
	if entry.Type == proxy.LogTypeDiagnostic {
		return true
	}
	linkedProcessID := d.linkedScriptForProxy(proxyID)
	if linkedProcessID == "" {
		// Unlinked proxies never suppress. Match the legacy behaviour.
		return true
	}
	// Drive the tracker bookkeeping. The boolean result is no longer
	// the gate decision, but the call is required: it triggers edge
	// detection, populates outageStartedAt, and emits open markers.
	_ = d.healthTracker.IsInSuppressionWindow(proxyID, linkedProcessID)

	mode := d.outageClassifier.SuppressionMode(linkedProcessID)
	switch mode {
	case ModeFull:
		return false
	case ModeDiagnosticOnly:
		// The TrafficLogger only distinguishes error / non-error log
		// types today. We forward anything that isn't the canonical
		// "error" class so warning-level customs and HTTP 4xx-as-info
		// can still surface.
		if isErrorClassEntry(entry) {
			return false
		}
		return true
	case ModeOff:
		fallthrough
	default:
		// Just past the grace window — emit the close marker now
		// (idempotent) so the agent sees suppression end at the same
		// instant errors actually start flowing again.
		d.healthTracker.MaybeCloseGraceWindow(proxyID, linkedProcessID)
		return true
	}
}

// isErrorClassEntry reports whether a proxy log entry is "error class"
// for the purposes of ModeDiagnosticOnly suppression. Frontend errors,
// HTTP 5xx responses, and explicit error-level diagnostics all qualify.
// Diagnostics are handled before this gate function runs, so callers
// only see non-diagnostic types here.
func isErrorClassEntry(entry proxy.LogEntry) bool {
	switch entry.Type {
	case proxy.LogTypeError:
		return true
	case proxy.LogTypeHTTP:
		if entry.HTTP != nil && entry.HTTP.StatusCode >= 500 {
			return true
		}
		return false
	default:
		return false
	}
}
