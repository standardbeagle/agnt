// Package daemon — proxy wait-for wiring.
//
// This file is the daemon-side half of the proxy readiness gate
// (see internal/proxy/readiness.go). It translates a proxy's
// `wait-for` config entries into daemon-side process IDs, primes
// the proxy server's gate, and spawns one waiter goroutine per
// dependency that blocks on ReadySignaler.WaitReadyCtx. When a
// dependency fires its ready signal (URL detected or port bound),
// the waiter calls MarkDependencyReady; the last waiter flips the
// gate open and emits a proxy_ready diagnostic through the alert
// hub so the overlay status bar can clear the "waiting" indicator.
//
// Lifecycle:
//
//	proxy created (URLDetected or ExplicitStart)
//	  → registerProxyDependencies
//	    → proxy.SetDependencies([processID...])
//	    → for each dep: go waitForProxyDep(ctx, proxyID, dep)
//
// Waiter cancellation:
//
//	Waiters use the daemon context. When the daemon shuts down or
//	the context is cancelled, outstanding waiters exit without
//	calling MarkDependencyReady. This is safe because the proxy
//	itself is torn down on ScriptStopped / StopByProjectPath events
//	before the daemon context fires.

package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// defaultReadinessWatchdogGrace bounds how long a proxy may sit gated before
// the daemon surfaces a warning. A genuinely slow backend (e.g. dotnet cold
// start) is "Starting", not failed, so the gate legitimately waits — but a gate
// still closed after this window usually means a `wait-for` dependency is not
// an autostart script, or never binds its declared port, and so will never
// signal ready. The Silent Failure Prohibition (.claude/rules/daemon-architecture.md)
// requires that be surfaced rather than hang mute behind an endless 503
// agnt_proxy_not_ready. Set on the Daemon at construction; tests override the
// per-instance field. Must not be shortened in production.
const defaultReadinessWatchdogGrace = 30 * time.Second

// registerProxyDependencies resolves the proxy config's WaitFor list
// into process IDs (rooted at projectPath), primes the proxy server's
// readiness gate, and spawns a waiter goroutine per dependency.
//
// Passes a no-op when the proxy has no `wait-for` declared — the gate
// stays open and no goroutines are spawned. This is the zero-cost
// path for the common case.
func (d *Daemon) registerProxyDependencies(
	server *proxy.ProxyServer,
	proxyID string,
	proxyCfg *config.ProxyConfig,
	projectPath string,
) {
	if server == nil || proxyCfg == nil || len(proxyCfg.WaitFor) == 0 {
		return
	}

	// Resolve script names to daemon process IDs. The proxy's own
	// linked script is allowed in the list — the gate treats it as
	// another dependency even though it's already running enough for
	// URL detection to have fired. In practice that's harmless: its
	// ready signal has already fired, so the waiter returns
	// immediately.
	depIDs := make([]string, 0, len(proxyCfg.WaitFor))
	for _, scriptName := range proxyCfg.WaitFor {
		depIDs = append(depIDs, makeProcessID(projectPath, scriptName))
	}

	server.SetDependencies(depIDs)
	debug.Log("daemon", "proxy %s gated on %d dependencies: %v", proxyID, len(depIDs), depIDs)

	// Spawn one waiter per dep. Each waiter blocks on the
	// ReadySignaler's per-process channel and calls MarkDependencyReady
	// when the signal fires. The last successful mark flips the gate
	// from closed to open and emits a proxy_ready diagnostic.
	for _, depID := range depIDs {
		go d.waitForProxyDependency(proxyID, depID)
	}

	// Watchdog: surface a warning if the gate is still closed after the grace
	// window. Satisfies the Silent Failure Prohibition — a proxy gated forever
	// on a dependency that will never signal (not autostart, never binds its
	// port) must not hang mute.
	go d.watchProxyReadiness(proxyID)
}

// watchProxyReadiness emits a one-shot warning if proxyID is still gated after
// proxyReadinessWatchdogGrace. Returns silently if the proxy opened normally,
// was torn down, or the daemon shut down within the window.
func (d *Daemon) watchProxyReadiness(proxyID string) {
	grace := d.readinessWatchdogGrace
	if grace <= 0 {
		grace = defaultReadinessWatchdogGrace
	}

	select {
	case <-d.ctx.Done():
		return
	case <-time.After(grace):
	}

	server, err := d.proxym.Get(proxyID)
	if err != nil {
		return // proxy gone — nothing to warn about
	}
	if server.IsReadyForForwarding() {
		return // opened normally within the grace window
	}

	pending := server.PendingDependencies()
	msg := fmt.Sprintf(
		"proxy %s still gated after %s, waiting on: %s — verify these scripts are autostart and bind their declared ports; the proxy returns 503 until every dependency signals ready",
		proxyID, grace, strings.Join(pending, ", "))

	if d.startupErrorStore != nil {
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: proxyID,
			Level:     "warning",
			EventType: "proxy_readiness_stalled",
			Message:   msg,
			Timestamp: time.Now(),
		})
	}

	if d.alertHub != nil {
		entry := proxy.LogEntry{
			Type: proxy.LogTypeDiagnostic,
			Diagnostic: &proxy.ProxyDiagnostic{
				Level:     proxy.DiagnosticWarning,
				Category:  "readiness",
				Event:     "proxy_readiness_stalled",
				Message:   msg,
				Timestamp: time.Now(),
			},
		}
		// Recover from any panic in the hub — this runs on a background
		// watchdog goroutine and must never take down the daemon.
		defer func() { _ = recover() }()
		d.alertHub.BroadcastLogEntry(entry, proxyID)
	}
}

// waitForProxyDependency blocks until dep's ready signal fires (or
// the daemon context is cancelled), then removes it from the proxy's
// pending set. If this was the last pending dependency, emits a
// proxy_ready diagnostic through the alert hub for overlay / MCP
// stream subscribers.
//
// The lookup is fresh on every call: if the proxy has been stopped
// and removed while the waiter was blocked, the call is a no-op.
func (d *Daemon) waitForProxyDependency(proxyID, depID string) {
	if d.readySignaler == nil {
		return
	}

	// Block until the dep fires ready or the daemon ctx closes. The
	// ready signal fires when:
	//   1. URL detection fires (urltracker.onURLDetected)
	//   2. A port probe succeeds (ReadySignaler.StartPortProbe)
	// Both paths land on SignalReady, which closes the per-process
	// channel. Running With Errors still fires the port probe since
	// the process is bound and listening.
	if err := d.readySignaler.WaitReadyCtx(depID, d.ctx); err != nil {
		debug.Log("daemon", "proxy %s waiter for dep %s exited: %v", proxyID, depID, err)
		return
	}

	// Look up the proxy fresh — it may have been stopped while we
	// were blocked (ScriptStopped event, session cleanup, etc).
	server, err := d.proxym.Get(proxyID)
	if err != nil {
		debug.Log("daemon", "proxy %s gone before dep %s ready", proxyID, depID)
		return
	}

	openedNow := server.MarkDependencyReady(depID)
	debug.Log("daemon", "proxy %s dep %s ready (opened-now=%v)", proxyID, depID, openedNow)

	if openedNow {
		d.emitProxyReady(proxyID)
	}
}

// emitProxyReady broadcasts a diagnostic marker through the alert
// hub when a proxy transitions from waiting_for_dependencies to
// ready. The event is informational — the gate state change is
// already reflected in proxy status / proxy list output.
func (d *Daemon) emitProxyReady(proxyID string) {
	if d.alertHub == nil {
		return
	}
	entry := proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:     proxy.DiagnosticInfo,
			Category:  "readiness",
			Event:     "proxy_ready",
			Message:   fmt.Sprintf("proxy %s dependencies satisfied — forwarding enabled", proxyID),
			Timestamp: time.Now(),
		},
	}
	// Recover from any panic in the hub — this runs on a ready-signal
	// goroutine and must never take down the daemon.
	defer func() { _ = recover() }()
	d.alertHub.BroadcastLogEntry(entry, proxyID)
}
