package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/scope"

	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// unmarshalCommand decodes cmd.Data into T. Returns zero value of T with nil
// error when Data is empty (valid case — no config provided). Returns an error
// when Data is non-empty but cannot be decoded.
func unmarshalCommand[T any](cmd *hubproto.Command) (T, error) {
	var zero T
	if len(cmd.Data) == 0 {
		return zero, nil
	}
	var v T
	if err := json.Unmarshal(cmd.Data, &v); err != nil {
		return zero, err
	}
	return v, nil
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

// getSessionScoped retrieves a resource with session-scoped fuzzy matching.
// If the connection has an associated session, only resources in that session's
// project path are considered for fuzzy lookup. Exact ID matches always work.
func getSessionScoped[T any](d *Daemon, conn *hubpkg.Connection, id string, getFn func(string, string) (T, error)) (T, error) {
	var pathFilter string
	if sessionCode := conn.SessionCode(); sessionCode != "" {
		if session, ok := d.sessionRegistry.Get(sessionCode); ok {
			pathFilter = session.ProjectPath
		}
	}
	return getFn(id, pathFilter)
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

// errNoSessionScope is returned by resolveProjectScope when a non-global
// query arrives on a connection with no resolvable session. It mirrors the
// INCIDENTS QUERY "no session attached" contract: scope is mandatory, so an
// unscopable non-global call fails loud rather than silently leaking every
// project's data.
var errNoSessionScope = errors.New("no session attached — call SESSION ATTACH first or pass global:true")

// resolveProjectScope is the session-scope chokepoint. It is the single
// resolution point every non-debug list/query handler routes through (wired
// in C4/C5). Given a per-call DirectoryFilter and the connection's bound
// session code, it returns the project path the handler must filter by:
//
//   - global == true  → ("", true, nil): no project filter (cross-project).
//   - explicit SessionCode → that session's project path (error if unknown).
//   - explicit Directory   → the normalized directory.
//   - otherwise the connection's bound session's project path.
//   - none of the above   → ("", false, errNoSessionScope): fail loud.
//
// Per-call global always wins. Resolution order mirrors the existing
// proc/proxy LIST handlers so behavior is uniform across the surface.
func (d *Daemon) resolveProjectScope(filter protocol.DirectoryFilter, connSessionCode string) (projectPath string, global bool, err error) {
	globalValue, globalSpecified := filter.GlobalSetting()
	if globalSpecified && globalValue {
		return "", true, nil
	}
	var path string
	if filter.SessionCode != "" {
		session, ok := d.sessionRegistry.Get(filter.SessionCode)
		if !ok {
			return "", false, fmt.Errorf("session %q not found", filter.SessionCode)
		}
		path = normalizePath(session.ProjectPath)
	} else if filter.Directory != "" {
		path = normalizePath(filter.Directory)
	} else if connSessionCode != "" {
		if session, ok := d.sessionRegistry.Get(connSessionCode); ok {
			path = normalizePath(session.ProjectPath)
		}
	}
	if path == "" {
		return "", false, errNoSessionScope
	}
	if globalSpecified {
		return path, false, nil
	}
	cfg, loadErr := config.LoadAgntConfig(path)
	if loadErr != nil {
		return "", false, fmt.Errorf("load scope config for %s: %w", path, loadErr)
	}
	if cfg != nil && cfg.Scope != nil && cfg.Scope.DefaultGlobal {
		return "", true, nil
	}
	return path, false, nil
}

// resolveScope is the scoped-token form of resolveProjectScope. It is the
// single bridge between the legacy (path, global) resolution chain and the
// scope.Scope token consumed by delivery paths (proxy lookups, overlay
// broadcasts). A session-less, non-global call fails loud exactly as
// resolveProjectScope does, so callers cannot accidentally fall through to a
// global. An explicit global flag becomes an audited Unscoped scope.
func (d *Daemon) resolveScope(filter protocol.DirectoryFilter, connSessionCode string) (scope.Scope, error) {
	path, global, err := d.resolveProjectScope(filter, connSessionCode)
	if err != nil {
		return scope.Scope{}, err
	}
	if global {
		return scope.Unscoped("explicit global flag on hub query"), nil
	}
	return scope.Project(path), nil
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

// localhostPortREs match localhost-style URLs with an explicit port. Compiled
// once at package init rather than per extractPortFromURL call.
var localhostPortREs = []*regexp.Regexp{
	regexp.MustCompile(`localhost:(\d+)`),
	regexp.MustCompile(`127\.0\.0\.1:(\d+)`),
	regexp.MustCompile(`0\.0\.0\.0:(\d+)`),
	regexp.MustCompile(`\[::1\]:(\d+)`),
}

// extractPortFromURL extracts a port number from a URL string.

func extractPortFromURL(urlStr string) int {
	for _, re := range localhostPortREs {
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
	// The directory is a filesystem path, carried in the JSON data frame. A
	// malformed frame must not fall through to "." — that silently detects
	// whatever project the daemon's own cwd happens to sit in.
	meta, err := unmarshalCommand[struct {
		Directory string `json:"directory"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid DETECT payload: %v", err))
	}
	path := meta.Directory
	if path == "" {
		path = "."
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
//
// Transport-signal contract: in addition to process-state suppression,
// the gate watches for transport-error bursts (refused/EOF/dial timeout)
// and HTTP 2xx/3xx recoveries. A burst flips the proxy into synthetic
// outage and held entries collect in the HoldBuffer. A recovery signal
// drains the buffer — cascade entries are dropped, real errors emitted.
// See internal/daemon/hold_buffer.go and the OutageHold config block.
func (d *Daemon) wireProxyLogger(server *proxy.ProxyServer) {
	// Every daemon proxy-creation path funnels through here, so this is the
	// single chokepoint applying project-level .agnt.kdl settings that live
	// outside the per-proxy block (currently: auth-breakout, secret sink).
	d.applyAuthBreakout(server)
	d.wireSecretSink(server)
	if d.eventHub == nil {
		return
	}
	proxyID := server.ID
	owner, _ := d.incidentProxyOwner.Load(proxyID)
	lifetimeOwner, _ := owner.(*incidentResourceOwner)
	d.eventHub.RegisterProxyPath(proxyID, server.Path)
	server.Logger().SetOnLogEntry(func(entry proxy.LogEntry) {
		// Feed transport signals to the tracker before gating so the
		// classifier sees recovery signals immediately.
		d.feedTransportSignals(proxyID, entry)

		decision := d.proxyBroadcastDecision(proxyID, entry)
		switch decision {
		case gateAccept:
			d.fireGatedFanOutForOwner(entry, proxyID, 1, lifetimeOwner)
		case gateHold:
			hb := d.holdBuffer.Load()
			if hb == nil {
				return
			}
			fp := FingerprintForEntry(entry)
			cascade := d.classifyCascade(entry)
			hb.HoldForOwner(entry, proxyID, fp, cascade, lifetimeOwner)
		case gateDrop:
			// Drop without emission — used for diagnostic suppression
			// markers we never want to re-emit.
		}
	})
}

// registerIncidentProxyOwner records the exact session captured when a proxy
// is created. LoadOrStore makes ownership immutable for the resource lifetime.
type incidentResourceOwner struct {
	sessionCode string
}

func (d *Daemon) registerIncidentProxyOwner(proxyID, sessionCode string) {
	if proxyID == "" || sessionCode == "" {
		return
	}
	d.incidentProxyOwner.LoadOrStore(proxyID, &incidentResourceOwner{sessionCode: sessionCode})
}

// registerIncidentProxyResourceOwner transfers the process lifetime's exact
// typed owner to a proxy created on that process's behalf. Keeping the same
// pointer is required by stampIncidentOwner's lifetime identity check.
func (d *Daemon) registerIncidentProxyResourceOwner(proxyID string, owner *incidentResourceOwner) {
	if proxyID == "" || owner == nil || owner.sessionCode == "" {
		return
	}
	d.incidentProxyOwner.LoadOrStore(proxyID, owner)
}

func (d *Daemon) registerIncidentProcessOwner(processID, sessionCode string) {
	if processID != "" && sessionCode != "" {
		d.incidentProcessOwner.LoadOrStore(processID, &incidentResourceOwner{sessionCode: sessionCode})
	}
}

// retireIncidentProxyOwner ends the ownership binding with the proxy lifetime.
// A later proxy reusing the same ID must be able to establish a fresh owner.
func (d *Daemon) retireIncidentProxyOwner(proxyID string) {
	d.incidentProxyOwner.Delete(proxyID)
}

func (d *Daemon) retireIncidentProcessOwner(processID string) {
	d.incidentProcessOwner.Delete(processID)
}

// stampIncidentOwner stamps exact resource ownership and fails closed when it
// was not captured at creation time.
func (d *Daemon) stampIncidentOwner(ev *incident.IncidentEvent, owners *sync.Map, resourceID string, captured *incidentResourceOwner) bool {
	if ev == nil {
		return false
	}
	if ev.Ctx.SessionID != "" {
		return true
	}
	owner, ok := owners.Load(resourceID)
	if !ok || captured == nil || owner != captured {
		return false
	}
	ev.Ctx.SessionID = captured.sessionCode
	return true
}

// applyAuthBreakout installs the project's auth-breakout rules (top-level
// .agnt.kdl block) on a freshly created proxy. A load/parse failure is
// non-fatal here — the autostart path already surfaces config parse errors
// loudly — so the proxy just runs without breakout.
func (d *Daemon) applyAuthBreakout(server *proxy.ProxyServer) {
	if server == nil || server.Path == "" {
		return
	}
	cfg, err := config.LoadAgntConfig(server.Path)
	if err != nil || cfg == nil {
		return
	}
	ab := cfg.AuthBreakout
	if !ab.IsEnabled() {
		return
	}
	server.SetAuthBreakout(&proxy.AuthBreakout{
		Mode:     ab.GetMode(),
		Patterns: ab.GetPatterns(),
	})
	debug.Log("daemon", "auth breakout enabled for proxy %s (mode=%s, %d patterns)", server.ID, ab.GetMode(), len(ab.GetPatterns()))
}

// feedTransportSignals inspects the entry for transport-error or
// recovery signals and updates the HealthTracker accordingly. Diagnostic
// entries categorised "transport" trigger an err record; HTTP responses
// with status <500 (non-error) trigger a recovery signal (proof the
// upstream is reachable).
func (d *Daemon) feedTransportSignals(proxyID string, entry proxy.LogEntry) {
	if d.healthTracker == nil {
		return
	}
	now := time.Now()
	switch entry.Type {
	case proxy.LogTypeDiagnostic:
		if entry.Diagnostic == nil {
			return
		}
		if entry.Diagnostic.Category == "transport" || isTransportEvent(entry.Diagnostic.Event) {
			d.healthTracker.RecordTransportError(proxyID, now)
		}
	case proxy.LogTypeHTTP:
		if entry.HTTP == nil {
			return
		}
		// Anything below 500 is "upstream answered" — counts as recovery.
		// 5xx is server-error, not connection failure, so it doesn't
		// signal connection health either way.
		if entry.HTTP.StatusCode > 0 && entry.HTTP.StatusCode < 500 {
			d.healthTracker.RecordRecoverySignal(proxyID, now)
		}
	}
}

// classifyCascade reports whether the entry should be treated as a
// transport-cascade message that the buffer drops on recovery. Transport
// diagnostics and matching browser-JS errors qualify; HTTP entries do
// not (5xx that arrives during outage is upstream signal worth keeping).
func (d *Daemon) classifyCascade(entry proxy.LogEntry) bool {
	switch entry.Type {
	case proxy.LogTypeDiagnostic:
		return true
	case proxy.LogTypeError:
		hb := d.holdBuffer.Load()
		if entry.Error == nil || hb == nil {
			return false
		}
		return hb.MatchesJSCascade(entry.Error.Message + " " + entry.Error.Stack)
	default:
		return false
	}
}

// fireGatedFanOut delivers an entry to all consumers after the gate
// (or hold-buffer emit) has approved it: EventHub stream sinks AND the
// incident bus. Used both for the immediate-pass path and as the
// HoldBuffer emit callback. mergedCount > 1 indicates the entry coalesced
// during the hold window; today the count is informational only — sinks
// see the original entry — but a future pass can synthesise a count
// prefix on the summary.
func (d *Daemon) fireGatedFanOut(entry proxy.LogEntry, proxyID string, _ int) {
	owner, _ := d.incidentProxyOwner.Load(proxyID)
	d.fireGatedFanOutForOwner(entry, proxyID, 1, ownerAsIncidentResource(owner))
}

func ownerAsIncidentResource(owner any) *incidentResourceOwner {
	result, _ := owner.(*incidentResourceOwner)
	return result
}

func (d *Daemon) fireGatedFanOutForOwner(entry proxy.LogEntry, proxyID string, _ int, owner *incidentResourceOwner) {
	if d.eventHub != nil {
		d.eventHub.BroadcastLogEntry(entry, proxyID)
	}
	d.fireToIncidentBusForOwner(entry, proxyID, owner)
}

// fireHoldEmit is the HoldBuffer emit callback. Same fan-out as direct
// gate-accept but with the merged-count signal preserved for telemetry.
func (d *Daemon) fireHoldEmit(entry proxy.LogEntry, proxyID string, mergedCount int) {
	d.fireGatedFanOut(entry, proxyID, mergedCount)
}

func (d *Daemon) fireHoldEmitForOwner(entry proxy.LogEntry, proxyID string, mergedCount int, owner *incidentResourceOwner) {
	d.fireGatedFanOutForOwner(entry, proxyID, mergedCount, owner)
}

// fireToIncidentBus runs the right adapter for entry's type and publishes
// the result on the daemon's incident bus. This is the first production
// wire of the incident adapter layer (see Phase A migration in
// event_hub.go). For sessions without a registered pipeline the bus is a
// no-op, so this remains safe to call unconditionally.
func (d *Daemon) fireToIncidentBus(entry proxy.LogEntry, proxyID string) {
	owner, _ := d.incidentProxyOwner.Load(proxyID)
	d.fireToIncidentBusForOwner(entry, proxyID, ownerAsIncidentResource(owner))
}

func (d *Daemon) fireToIncidentBusForOwner(entry proxy.LogEntry, proxyID string, owner *incidentResourceOwner) {
	if d.incidentBus == nil {
		return
	}

	// Feed the swallowed-error heuristic before any suppression: a watched
	// injected fault must be recorded as pending even though it is kept out of
	// the inbox, and an app-side error clears recent pending faults. A fault is
	// "watched" only when the proxy's runtime swallow-detect panel toggle was
	// on at injection (stamped ChaosSwallowWatch); otherwise nothing is
	// recorded and the lazy sweeper never starts.
	if d.swallowDetector != nil {
		switch entry.Type {
		case proxy.LogTypeHTTP:
			if entry.HTTP != nil && entry.HTTP.ChaosSwallowWatch && entry.HTTP.StatusCode >= 400 {
				d.swallowDetector.RecordFault(proxyID, entry.HTTP.URL, entry.HTTP.StatusCode, time.Now())
				d.startSwallowSweeperOnce()
			}
		case proxy.LogTypeError:
			d.swallowDetector.RecordAppError(proxyID, time.Now())
		}
	}

	switch entry.Type {
	case proxy.LogTypeDiagnostic:
		if entry.Diagnostic == nil {
			return
		}
		if ev, ok := incident.FromProxyDiagnostic(*entry.Diagnostic, proxyID); ok {
			if d.stampIncidentOwner(&ev, &d.incidentProxyOwner, proxyID, owner) {
				d.incidentBus.Publish(ev)
			}
		}
	case proxy.LogTypeHTTP:
		if entry.HTTP == nil {
			return
		}
		// Chaos-injected faults are synthetic: they stay in the traffic log
		// (proxylog query surfaces them) and feed the swallow detector above,
		// but never enter the agent incident inbox — an injected 500 is not a
		// real backend error to chase.
		if entry.HTTP.Chaos {
			return
		}
		if ev, ok := incident.FromHTTPEntry(*entry.HTTP, proxyID); ok {
			if d.stampIncidentOwner(&ev, &d.incidentProxyOwner, proxyID, owner) {
				d.incidentBus.Publish(ev)
			}
		}
	case proxy.LogTypeError:
		if entry.Error == nil {
			return
		}
		ev := incident.FromFrontendError(*entry.Error, proxyID)
		if d.stampIncidentOwner(&ev, &d.incidentProxyOwner, proxyID, owner) {
			d.incidentBus.Publish(ev)
		}
	}
}

// isTransportEvent mirrors the transport-event classification used by
// internal/incident/adapter_diag.go so the gate and adapter agree.
func isTransportEvent(event string) bool {
	switch strings.ToLower(event) {
	case "refused", "timeout", "error", "dial", "tls", "io_error", "net_error":
		return true
	}
	return false
}

// gateDecision is the tri-state output of the broadcast gate.
type gateDecision int

const (
	// gateAccept allows the entry to flow to all consumers immediately.
	gateAccept gateDecision = iota
	// gateHold queues the entry into the HoldBuffer; emission is
	// deferred until the hold window expires or the proxy recovers.
	gateHold
	// gateDrop discards the entry without emission. Used only for
	// diagnostic suppression markers that have already been delivered
	// once and should not be re-emitted.
	gateDrop
)

// proxyBroadcastGate is preserved as a thin wrapper over
// proxyBroadcastDecision for legacy callers. true == accept, false ==
// drop-or-hold (the wrapper collapses both into "do not call
// BroadcastLogEntry directly"). New code should use the tri-state
// proxyBroadcastDecision.
func (d *Daemon) proxyBroadcastGate(proxyID string, entry proxy.LogEntry) bool {
	return d.proxyBroadcastDecision(proxyID, entry) == gateAccept
}

// proxyBroadcastDecision returns the gate decision for entry on proxyID:
// accept (forward immediately), hold (queue into HoldBuffer), or drop
// (discard).
//
// Diagnostic entries always accept — they are how the daemon
// communicates suppression state to the agent.
//
// For non-diagnostic entries:
//   - The HealthTracker is invoked for its side-effect (edge detection,
//     marker emission) regardless of the result.
//   - The OutageClassifier's tri-state SuppressionMode now considers BOTH
//     the linked process state (existing behaviour) AND the proxy's
//     synthetic transport-outage flag (new behaviour). When the proxy is
//     in transport outage but the process is healthy, ModeFull applies.
//   - ModeFull → hold. ModeDiagnosticOnly → hold for error-class entries,
//     accept for the rest. ModeOff → accept.
//
// The hold buffer (NewHoldBuffer) drives final disposition: held entries
// either replay on window expiry or drop on recovery.
func (d *Daemon) proxyBroadcastDecision(proxyID string, entry proxy.LogEntry) gateDecision {
	if entry.Type == proxy.LogTypeDiagnostic {
		return gateAccept
	}
	linkedProcessID := d.linkedScriptForProxy(proxyID)
	if linkedProcessID != "" {
		// Drive process-edge detection bookkeeping for the classifier.
		_ = d.healthTracker.IsInSuppressionWindow(proxyID, linkedProcessID)
	}

	mode := d.outageClassifier.SuppressionModeProxy(proxyID, linkedProcessID)
	switch mode {
	case ModeFull:
		return gateHold
	case ModeDiagnosticOnly:
		if isErrorClassEntry(entry) {
			return gateHold
		}
		return gateAccept
	case ModeOff:
		fallthrough
	default:
		// Just past the grace window — emit the close marker now
		// (idempotent) so the agent sees suppression end at the same
		// instant errors actually start flowing again.
		if linkedProcessID != "" {
			d.healthTracker.MaybeCloseGraceWindow(proxyID, linkedProcessID)
		}
		return gateAccept
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
