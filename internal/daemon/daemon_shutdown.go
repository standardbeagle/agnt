package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/browser"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
)

func (d *Daemon) restoreProxies() {
	if d.stateMgr == nil {
		return
	}

	proxies := d.stateMgr.GetProxies()
	if len(proxies) == 0 {
		return
	}

	// Removed startup log: restoring %d proxies from state

	overlayEndpoint := d.OverlayEndpoint()

	for _, pc := range proxies {
		config := proxy.ProxyConfig{
			ID:          pc.ID,
			TargetURL:   pc.TargetURL,
			ListenPort:  pc.Port,
			MaxLogSize:  pc.MaxLogSize,
			AutoRestart: true,
			Path:        pc.Path,
		}

		proxyServer, err := d.proxym.Create(d.ctx, config)
		if err != nil {
			debug.Log("daemon", "failed to restore proxy %s: %v", pc.ID, err)
			// Remove from state if it can't be restored
			d.stateMgr.RemoveProxy(pc.ID)
			continue
		}

		d.wireProxyLogger(proxyServer)

		// Configure overlay endpoint: prefer session-scoped, fall back to global
		if pc.Path != "" {
			if session, ok := d.sessionRegistry.FindByDirectory(pc.Path); ok && session.OverlayPath != "" {
				proxyServer.SetOverlayEndpoint(session.OverlayPath)
			} else if overlayEndpoint != "" {
				proxyServer.SetOverlayEndpoint(overlayEndpoint)
			}
		} else if overlayEndpoint != "" {
			proxyServer.SetOverlayEndpoint(overlayEndpoint)
		}

		// Register a proxy-kind admin entry so the restored proxy appears in
		// SCRIPT LIST (and therefore the overlay status bar) just like a
		// freshly-started one. Without this, a daemon restart silently drops
		// the proxy from the admin surface even though proxym has it running
		// again — T5 closes the gap between the start path
		// (handleExplicitStart → registerExplicitProxyEntry) and the restart
		// path.
		d.registerExplicitProxyEntry(pc.Path, pc.ID, proxyServer.ListenAddr)

		// Removed startup log: restored proxy %s -> %s on port %d
	}
}

func (d *Daemon) checkBrowserVersion() {
	info := browser.CheckVersion()
	if warning := browser.FormatWarning(info); warning != "" {
		debug.Log("daemon", "browser check: %s", warning)
		d.startupErrorStore.Add(&StartupLogEntry{
			Level: "warning", EventType: "browser_check",
			Message:   warning,
			Timestamp: time.Now(),
		})
	} else if info != nil {
		debug.Log("daemon", "browser: Chrome %s at %s", info.FullVersion, info.Path)
	}
}

func (d *Daemon) cleanupOrphans() {
	if d.pidTracker == nil {
		return
	}

	killedCount, err := d.pidTracker.CleanupOrphans(os.Getpid())
	if err != nil {
		debug.Log("daemon", "failed to cleanup orphans: %v", err)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: "",
			Level:     "warning",
			EventType: "orphan_cleanup_failed",
			Message:   fmt.Sprintf("failed to cleanup orphans: %v", err),
			Timestamp: time.Now(),
		})
		return
	}

	if killedCount > 0 {
		debug.Log("daemon", "cleaned up %d orphaned process(es) from previous crash", killedCount)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: "",
			Level:     "info",
			EventType: "orphan_cleanup",
			Message:   fmt.Sprintf("cleaned up %d orphaned process(es) from previous crash", killedCount),
			Timestamp: time.Now(),
		})
	}

	// Set current daemon PID for future crash detection
	if err := d.pidTracker.SetDaemonPID(os.Getpid()); err != nil {
		debug.Log("daemon", "failed to set daemon PID: %v", err)
	}
}

// startupPortCleanup scans ports referenced by persisted proxy state and kills
// any unmanaged processes holding them. This runs after cleanupOrphans() to
// handle the race where orphaned processes were killed but ports remain bound
// by zombie child processes that escaped the process group.
// Returns the number of ports cleaned.
func (d *Daemon) startupPortCleanup(ctx context.Context) int {
	ports := d.collectPersistedPorts()
	if len(ports) == 0 {
		return 0
	}

	managedPIDs := d.collectManagedPIDs()
	selfPID := os.Getpid()
	cleaned := 0

	for _, port := range ports {
		pids := config.FindPIDsByPort(ctx, port)
		var unmanaged []int
		for _, pid := range pids {
			if pid == selfPID || managedPIDs[pid] {
				continue
			}
			unmanaged = append(unmanaged, pid)
		}
		if len(unmanaged) == 0 {
			continue
		}

		procName := config.ProcessNameByPID(unmanaged[0])
		debug.Info("daemon", "startup port cleanup: port %d held by %s (PIDs: %v), killing", port, procName, unmanaged)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: "",
			Level:     "info",
			EventType: "startup_port_cleanup",
			Message:   fmt.Sprintf("port %d held by %s (PIDs: %v), killing", port, procName, unmanaged),
			Port:      port,
			Timestamp: time.Now(),
		})

		if killed, err := d.hub.ProcessManager().KillProcessByPort(ctx, port); err != nil {
			debug.Warn("daemon", "startup port cleanup: failed to kill process on port %d: %v", port, err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: "",
				Level:     "warning",
				EventType: "startup_port_cleanup_failed",
				Message:   fmt.Sprintf("failed to kill process on port %d: %v", port, err),
				Port:      port,
				Timestamp: time.Now(),
			})
		} else {
			debug.Log("daemon", "startup port cleanup: killed %d process(es) on port %d", len(killed), port)
			if waitPortFree(port, 2*time.Second) {
				cleaned++
			} else {
				debug.Warn("daemon", "startup port cleanup: port %d still in use after kill", port)
			}
		}
	}

	if cleaned > 0 {
		debug.Info("daemon", "startup port cleanup: freed %d port(s)", cleaned)
	}
	return cleaned
}

// collectPersistedPorts returns deduplicated ports from persisted proxy state.
func (d *Daemon) collectPersistedPorts() []int {
	if d.stateMgr == nil {
		return nil
	}
	seen := make(map[int]bool)
	var ports []int
	for _, pc := range d.stateMgr.GetProxies() {
		if pc.Port > 0 && !seen[pc.Port] {
			seen[pc.Port] = true
			ports = append(ports, pc.Port)
		}
	}
	return ports
}

func (d *Daemon) Stop(ctx context.Context) error {
	d.shutdownMu.Lock()
	if d.shutdown {
		d.shutdownMu.Unlock()
		return nil
	}
	d.shutdown = true
	d.shutdownMu.Unlock()

	debug.Log("daemon", "Daemon stopping")

	// Signal all goroutines to stop
	d.cancel()

	// Cancel all in-flight autostart runs before shutting down the Hub's
	// ProcessManager. Without this, an autostart goroutine in the middle of
	// ProcessManager.Start races with ProcessManager.Shutdown and triggers
	// the race detector. Autostart goroutines check ctx.Done() at each
	// iteration of the runLayer wait loop, so they will exit shortly after.
	if d.autostartManager != nil {
		d.autostartManager.CancelAll()
	}

	// Stop Hub (handles listener, clients, connections, and ProcessManager)
	if err := d.hub.Stop(ctx); err != nil {
		debug.Log("daemon", "error stopping hub: %v", err)
	}

	// Shutdown agnt-specific managers
	var errs []error

	// Stop scheduler
	d.scheduler.Stop(ctx)

	// Stop update checker
	if d.updateChecker != nil {
		d.updateChecker.Stop(ctx)
	}

	// Stop duplicate process scanner
	if d.dupScanner != nil {
		d.dupScanner.Stop()
	}

	// Stop process auto-restarter first (before processes are stopped)
	if d.autoRestarter != nil {
		d.autoRestarter.Shutdown(ctx)
	}

	if err := d.tunnelm.Shutdown(ctx); err != nil {
		debug.Error("daemon", "tunnel manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("tunnel manager: %w", err))
	}

	if err := d.browserm.Shutdown(ctx); err != nil {
		debug.Error("daemon", "browser manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("browser manager: %w", err))
	}

	if err := d.sessionm.Shutdown(ctx); err != nil {
		debug.Error("daemon", "session manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("session manager: %w", err))
	}

	if err := d.proxym.Shutdown(ctx); err != nil {
		debug.Error("daemon", "proxy manager shutdown error: %v", err)
		errs = append(errs, fmt.Errorf("proxy manager: %w", err))
	}

	// Flush and close state managers so pending writes reach disk
	if d.stateMgr != nil {
		if err := d.stateMgr.Close(); err != nil {
			debug.Error("daemon", "state manager close error: %v", err)
			errs = append(errs, fmt.Errorf("state manager: %w", err))
		}
	}
	if d.schedulerStateMgr != nil {
		if err := d.schedulerStateMgr.Close(); err != nil {
			debug.Error("daemon", "scheduler state manager close error: %v", err)
			errs = append(errs, fmt.Errorf("scheduler state manager: %w", err))
		}
	}

	// Preserve PID tracking file across daemon restarts. Do NOT clear it
	// here -- if any processes survived hub shutdown (e.g., grandchildren
	// that escaped process group signals), the next daemon startup will
	// find and kill them via cleanupOrphans(). Clearing the tracker here
	// would lose track of survivors, leaving them as permanent orphans.
	// The cleanupOrphans() path handles stale entries gracefully by
	// checking isProcessAlive before killing.

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Clean exit
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}

	// Socket cleanup is handled by Hub.Stop()

	debug.Log("daemon", "Daemon stopped")

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (d *Daemon) Wait() {
	<-d.ctx.Done()
	d.wg.Wait()
}

// SetOnShutdown registers a callback invoked when the hub receives a remote
// SHUTDOWN command.  The host process typically uses this to cancel its own
// signal context so it exits cleanly instead of lingering (important on
// Windows where Unix signals are not delivered).
func (d *Daemon) SetOnShutdown(fn func()) {
	d.hub.SetOnShutdown(fn)
}

func (d *Daemon) StopAllResources(ctx context.Context) {
	// Use a reasonable timeout for cleanup
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Stop all tunnels
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.tunnelm.StopAll(cleanupCtx); err != nil {
			debug.Log("daemon", "error stopping tunnels: %v", err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: "",
				Level:     "warning",
				EventType: "stop_failed",
				Message:   fmt.Sprintf("error stopping tunnels: %v", err),
				Timestamp: time.Now(),
			})
		}
	}()

	// Stop all browsers
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := d.browserm.StopAll(cleanupCtx); err != nil {
			debug.Log("daemon", "error stopping browsers: %v", err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: "",
				Level:     "warning",
				EventType: "stop_failed",
				Message:   fmt.Sprintf("error stopping browsers: %v", err),
				Timestamp: time.Now(),
			})
		}
	}()

	// Stop all proxies and update state
	wg.Add(1)
	go func() {
		defer wg.Done()
		stoppedIDs, err := d.proxym.StopAll(cleanupCtx)
		if err != nil {
			debug.Log("daemon", "error stopping proxies: %v", err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: "",
				Level:     "warning",
				EventType: "proxy_stop_failed",
				Message:   fmt.Sprintf("error stopping proxies: %v", err),
				Timestamp: time.Now(),
			})
		}
		// Remove stopped proxies from persisted state
		if d.stateMgr != nil {
			for _, id := range stoppedIDs {
				d.stateMgr.RemoveProxy(id)
			}
		}
	}()

	// Stop all processes
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.hub.ProcessManager().StopAll(cleanupCtx); err != nil {
			debug.Log("daemon", "error stopping processes: %v", err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: "",
				Level:     "warning",
				EventType: "stop_failed",
				Message:   fmt.Sprintf("error stopping processes: %v", err),
				Timestamp: time.Now(),
			})
		}
	}()

	wg.Wait()

	// Clear overlay endpoint since no clients are connected
	d.SetOverlayEndpoint("")

	debug.Log("daemon", "all resources stopped (last client disconnected)")
}

// CleanupSessionResources stops all processes and proxies for a specific session.
