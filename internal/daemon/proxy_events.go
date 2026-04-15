package daemon

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// buildProxyServerConfig builds a proxy.ProxyConfig from a .agnt.kdl
// ProxyConfig, honoring explicit ListenPort / SkipTLSVerify when set.
//
// Behavior:
//   - ListenPort <= 0 in the .agnt.kdl config → pass -1 to trigger the
//     hash-based stable allocator (preserves pre-feature behavior).
//   - ListenPort > 0 → pass the literal port and set StrictListenPort
//     so Start() will NOT silently fall back to :0 on bind conflict.
//   - SkipTLSVerify is a pure pass-through; the runtime already
//     supports it (see proxy/server.go).
//
// Callers are responsible for pre-flight port conflict detection
// when ListenPort is set — the daemon autostart path surfaces the
// owning process via the startupErrorStore before Create() even
// runs, so the conflict is visible to the AI agent even if the
// runtime bind error would otherwise be terse.
func buildProxyServerConfig(id, targetURL, projectPath string, cfg *config.ProxyConfig) proxy.ProxyConfig {
	listenPort := -1 // default: hash-based allocator
	strict := false
	if cfg != nil && cfg.ListenPort > 0 {
		listenPort = cfg.ListenPort
		strict = true
	}
	var (
		maxLog        int
		bind          string
		skipTLSVerify bool
	)
	if cfg != nil {
		maxLog = cfg.MaxLogSize
		bind = cfg.Bind
		skipTLSVerify = cfg.SkipTLSVerify
	}
	return proxy.ProxyConfig{
		ID:               id,
		TargetURL:        targetURL,
		ListenPort:       listenPort,
		StrictListenPort: strict,
		MaxLogSize:       maxLog,
		AutoRestart:      true,
		Path:             projectPath,
		BindAddress:      bind,
		SkipTLSVerify:    skipTLSVerify,
	}
}

// handleProxyEvents runs the proxy event handling loop.
// It listens for events and creates/destroys proxies accordingly.
func (d *Daemon) handleProxyEvents() {
	defer d.wg.Done()
	debug.Log("daemon", "Proxy event handler started")

	for {
		select {
		case <-d.ctx.Done():
			debug.Log("daemon", "Proxy event handler stopping (context done)")
			return
		case event := <-d.proxyEvents:
			debug.Log("daemon", "Received proxy event: type=%d scriptID=%s URL=%s path=%s", event.Type, event.ScriptID, event.URL, event.Path)
			switch event.Type {
			case URLDetected:
				d.handleURLDetected(event)
			case ExplicitStart:
				d.handleExplicitStart(event)
			case ScriptStopped:
				d.handleScriptStopped(event)
			case FallbackPortCheck:
				d.handleFallbackPortCheck(event)
			default:
				debug.Warn("daemon", "Unknown proxy event type: %d", event.Type)
			}
		}
	}
}

// handleURLDetected handles URL detection events from scripts.
// Creates proxies for any proxy configs linked to the script.
func (d *Daemon) handleURLDetected(event ProxyEvent) {
	debug.Log("daemon", "URL detected from %s: %s (path: %s)", event.ScriptID, event.URL, event.Path)

	// Get project path from event
	projectPath := event.Path
	if projectPath == "" {
		debug.Warn("daemon", "No project path in URL detection event for script %s", event.ScriptID)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: event.ScriptID,
			Level:     "warning",
			EventType: "proxy_creation_failed",
			Message:   "no project path in URL detection event, proxy not created",
			Timestamp: time.Now(),
		})
		return
	}

	// Extract script name from process ID (format: {basename}:{scriptName})
	parts := strings.SplitN(event.ScriptID, ":", 2)
	if len(parts) < 2 {
		debug.Warn("daemon", "Cannot parse script name from script ID: %s", event.ScriptID)
		return
	}
	scriptName := parts[1]
	debug.Log("daemon", "Extracted script name: %q from process ID: %s", scriptName, event.ScriptID)

	// Load agnt configuration
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		debug.Warn("daemon", "Failed to load agnt config for %s: %v", projectPath, err)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: event.ScriptID,
			Level:     "warning",
			EventType: "proxy_creation_failed",
			Message:   fmt.Sprintf("failed to load agnt config for %s: %v", projectPath, err),
			Timestamp: time.Now(),
		})
		return
	}
	debug.Log("daemon", "Loaded %d proxies from config", len(agntConfig.Proxies))

	// Find proxy configs linked to this script
	for proxyName, proxyConfig := range agntConfig.Proxies {
		debug.Log("daemon", "Checking proxy %q (script=%q) against scriptName=%q", proxyName, proxyConfig.Script, scriptName)
		if proxyConfig.Script != scriptName {
			debug.Log("daemon", "Proxy %s script %q != %q, skipping", proxyName, proxyConfig.Script, scriptName)
			continue // Not linked to this script
		}
		debug.Log("daemon", "Proxy %s matches script %s, will create proxy for URL %s", proxyName, scriptName, event.URL)

		// Check URL pattern filter if configured
		if proxyConfig.URLPattern != "" {
			matched, err := regexp.MatchString(proxyConfig.URLPattern, event.URL)
			if err != nil {
				debug.Warn("daemon", "Invalid url-pattern regex for proxy %s: %v", proxyName, err)
				continue
			}
			if !matched {
				debug.Log("daemon", "URL %s does not match pattern %q for proxy %s, skipping", event.URL, proxyConfig.URLPattern, proxyName)
				continue
			}
		}

		// Create proxy for this URL
		proxyID := makeProxyIDFromURL(projectPath, proxyName, event.URL)

		// Check if proxy already exists
		if _, err := d.proxym.Get(proxyID); err == nil {
			debug.Log("daemon", "Proxy %s already exists, skipping", proxyID)
			continue
		}

		// Check proxy limit per session
		d.scriptProxyMu.RLock()
		currentCount := len(d.scriptProxies[event.ScriptID])
		d.scriptProxyMu.RUnlock()

		if currentCount >= 5 {
			debug.Warn("daemon", "Proxy limit (5) reached for script %s, skipping %s", event.ScriptID, event.URL)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: event.ScriptID,
				Level:     "warning",
				EventType: "proxy_limit_reached",
				Message:   fmt.Sprintf("proxy limit (5) reached, skipping URL %s", event.URL),
				Timestamp: time.Now(),
			})
			continue
		}

		// Create proxy
		proxyServerConfig := buildProxyServerConfig(proxyID, event.URL, projectPath, proxyConfig)

		server, err := d.proxym.Create(d.ctx, proxyServerConfig)
		if err != nil {
			debug.Error("daemon", "Failed to create proxy %s: %v", proxyID, err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: event.ScriptID,
				Level:     "error",
				EventType: "proxy_creation_failed",
				Message:   fmt.Sprintf("failed to create proxy %s: %v", proxyID, err),
				Timestamp: time.Now(),
			})
			continue
		}

		d.wireProxyLogger(server)

		// Find session for this project to get session-specific overlay endpoint
		if session, ok := d.sessionRegistry.FindByDirectory(projectPath); ok && session.OverlayPath != "" {
			server.SetOverlayEndpoint(session.OverlayPath)
			debug.Log("daemon", "Set session-specific overlay endpoint for proxy %s: %s", proxyID, session.OverlayPath)
		} else if overlayEndpoint := d.OverlayEndpoint(); overlayEndpoint != "" {
			// Fallback to global overlay endpoint if no session found
			server.SetOverlayEndpoint(overlayEndpoint)
			debug.Log("daemon", "Set global overlay endpoint for proxy %s: %s", proxyID, overlayEndpoint)
		} else {
			debug.Log("daemon", "No overlay endpoint found for URL-detected proxy %s (path=%q) — proxy→agent messages will not work", proxyID, projectPath)
		}

		// Track script -> proxy association
		d.trackScriptProxy(event.ScriptID, proxyID)

		// Register wait-for dependencies — gates the proxy's
		// forwarding path until every listed script signals ready.
		d.registerProxyDependencies(server, proxyID, proxyConfig, projectPath)

		debug.Log("daemon", "Created proxy %s targeting %s", proxyID, event.URL)
	}
}

// handleExplicitStart handles explicit proxy start events (fully-specified proxies).
func (d *Daemon) handleExplicitStart(event ProxyEvent) {
	if event.Config == nil || event.ProxyID == "" {
		debug.Warn("daemon", "Invalid ExplicitStart event: missing config or proxyID")
		return
	}

	// Check if already exists
	if _, err := d.proxym.Get(event.ProxyID); err == nil {
		debug.Log("daemon", "Proxy %s already exists, skipping", event.ProxyID)
		return
	}

	// Determine target URL from config
	var targetURL string
	if event.Config.URL != "" {
		targetURL = event.Config.URL
	} else if event.Config.Port > 0 {
		host := event.Config.Host
		if host == "" {
			host = "localhost"
		}
		targetURL = fmt.Sprintf("http://%s:%d", host, event.Config.Port)
	} else if event.Config.Target != "" {
		targetURL = event.Config.Target
	} else {
		debug.Warn("daemon", "ExplicitStart event for %s has no target URL", event.ProxyID)
		return
	}

	// Create proxy
	proxyServerConfig := buildProxyServerConfig(event.ProxyID, targetURL, event.Path, event.Config)

	server, err := d.proxym.Create(d.ctx, proxyServerConfig)
	if err != nil {
		debug.Error("daemon", "Failed to create proxy %s: %v", event.ProxyID, err)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: event.ProxyID,
			Level:     "error",
			EventType: "proxy_creation_failed",
			Message:   fmt.Sprintf("failed to create explicit proxy %s: %v", event.ProxyID, err),
			Timestamp: time.Now(),
		})
		return
	}

	d.wireProxyLogger(server)

	// Find session for this project to get session-specific overlay endpoint
	if event.Path != "" {
		if session, ok := d.sessionRegistry.FindByDirectory(event.Path); ok && session.OverlayPath != "" {
			server.SetOverlayEndpoint(session.OverlayPath)
			debug.Log("daemon", "Set session-specific overlay endpoint for explicit proxy %s: %s", event.ProxyID, session.OverlayPath)
		} else if overlayEndpoint := d.OverlayEndpoint(); overlayEndpoint != "" {
			// Fallback to global overlay endpoint if no session found
			server.SetOverlayEndpoint(overlayEndpoint)
			debug.Log("daemon", "Set global overlay endpoint for explicit proxy %s: %s", event.ProxyID, overlayEndpoint)
		} else {
			debug.Log("daemon", "No overlay endpoint found for explicit proxy %s (path=%q) — proxy→agent messages will not work", event.ProxyID, event.Path)
		}
	} else if overlayEndpoint := d.OverlayEndpoint(); overlayEndpoint != "" {
		// Fallback to global overlay endpoint if no path specified
		server.SetOverlayEndpoint(overlayEndpoint)
		debug.Log("daemon", "Set global overlay endpoint for explicit proxy %s: %s", event.ProxyID, overlayEndpoint)
	} else {
		debug.Log("daemon", "No overlay endpoint found for explicit proxy %s (no path, no global endpoint) — proxy→agent messages will not work", event.ProxyID)
	}

	// Register wait-for dependencies for explicit proxies too.
	// Explicit proxies (e.g. `autostart true` without a script link)
	// can still declare `wait-for` to hold forwarding until backend
	// scripts bind. The dependency names resolve against the project
	// scripts just like the script-linked path.
	d.registerProxyDependencies(server, event.ProxyID, event.Config, event.Path)

	debug.Log("daemon", "Created explicit proxy %s targeting %s", event.ProxyID, targetURL)
}

// handleFallbackPortCheck handles delayed fallback-port checks for script-linked
// proxies whose URL detection never fired. Scheduled by
// scheduleFallbackPortChecks 30 seconds after autostart, it either:
//
//  1. Finds an already-created proxy (URL detection won the race) and logs
//     startup_proxy_fallback_skipped_already_running, or
//  2. Creates a proxy targeting http://localhost:<fallback-port> using the
//     same proxy-id scheme as handleExplicitStart (makeProcessID), and logs
//     startup_proxy_fallback_used on success or startup_proxy_fallback_failed
//     on failure.
//
// Both success and failure entries flow through startupErrorStore so the
// decision is visible via get_errors and the overlay — no silent drops.
func (d *Daemon) handleFallbackPortCheck(event ProxyEvent) {
	if event.Config == nil || event.ProxyName == "" || event.Path == "" {
		debug.Warn("daemon", "Invalid FallbackPortCheck event: missing config, proxyName, or path")
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID:  event.ScriptID,
			ScriptName: event.ProxyName,
			Level:      "warning",
			EventType:  "startup_proxy_fallback_failed",
			Message:    "invalid FallbackPortCheck event: missing config, proxy name, or project path",
			Timestamp:  time.Now(),
		})
		return
	}

	if event.Config.FallbackPort <= 0 {
		debug.Warn("daemon", "FallbackPortCheck for proxy %s has no fallback-port configured", event.ProxyName)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID:  event.ScriptID,
			ScriptName: event.ProxyName,
			Level:      "warning",
			EventType:  "startup_proxy_fallback_failed",
			Message:    fmt.Sprintf("fallback-port check for proxy %s has no fallback-port configured", event.ProxyName),
			Timestamp:  time.Now(),
		})
		return
	}

	// Use the explicit-start proxy id scheme. Script-linked proxies created
	// via URLDetected use makeProxyIDFromURL (which embeds host+port), so we
	// also need to scan any existing proxies linked to this script to detect
	// the URL-detection-won-the-race case.
	fallbackProxyID := makeProcessID(event.Path, event.ProxyName)

	// Race check 1: an existing proxy with the fallback id (another fallback
	// check for the same name somehow fired twice, or explicit-start path
	// already created one).
	if _, err := d.proxym.Get(fallbackProxyID); err == nil {
		debug.Log("daemon", "Fallback proxy %s already exists, skipping", fallbackProxyID)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID:  event.ScriptID,
			ScriptName: event.ProxyName,
			Level:      "info",
			EventType:  "startup_proxy_fallback_skipped_already_running",
			Message:    fmt.Sprintf("fallback-port check for proxy %s: proxy already running (id=%s)", event.ProxyName, fallbackProxyID),
			Timestamp:  time.Now(),
		})
		return
	}

	// Race check 2: URL detection created a proxy via makeProxyIDFromURL
	// whose id has the form "{fallbackProxyID}:{host}-{port}". Match by
	// prefix so we only skip when a proxy for THIS specific proxy-name was
	// already created (not a sibling proxy config linked to the same
	// script). This matters when a single script has multiple linked
	// proxy configs and only some of them have URL detection succeed.
	if event.ScriptID != "" {
		idPrefix := fallbackProxyID + ":"
		d.scriptProxyMu.RLock()
		tracked := d.scriptProxies[event.ScriptID]
		var raceWinner string
		for _, id := range tracked {
			if strings.HasPrefix(id, idPrefix) {
				raceWinner = id
				break
			}
		}
		d.scriptProxyMu.RUnlock()
		if raceWinner != "" {
			debug.Log("daemon", "Fallback proxy %s: URL detection already created %s, skipping", event.ProxyName, raceWinner)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID:  event.ScriptID,
				ScriptName: event.ProxyName,
				Level:      "info",
				EventType:  "startup_proxy_fallback_skipped_already_running",
				Message:    fmt.Sprintf("fallback-port check for proxy %s: URL detection already created proxy %s for script %s", event.ProxyName, raceWinner, event.Config.Script),
				Timestamp:  time.Now(),
			})
			return
		}
	}

	// No proxy exists — create one targeting the fallback port.
	host := event.Config.Host
	if host == "" {
		host = "localhost"
	}
	targetURL := fmt.Sprintf("http://%s:%d", host, event.Config.FallbackPort)

	proxyServerConfig := buildProxyServerConfig(fallbackProxyID, targetURL, event.Path, event.Config)

	server, err := d.proxym.Create(d.ctx, proxyServerConfig)
	if err != nil {
		debug.Error("daemon", "Failed to create fallback proxy %s: %v", fallbackProxyID, err)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID:  event.ScriptID,
			ScriptName: event.ProxyName,
			Level:      "warning",
			EventType:  "startup_proxy_fallback_failed",
			Message:    fmt.Sprintf("failed to create fallback proxy %s targeting %s: %v", event.ProxyName, targetURL, err),
			Port:       event.Config.FallbackPort,
			Timestamp:  time.Now(),
		})
		return
	}

	d.wireProxyLogger(server)

	// Overlay endpoint: prefer the session bound to this project path.
	if session, ok := d.sessionRegistry.FindByDirectory(event.Path); ok && session.OverlayPath != "" {
		server.SetOverlayEndpoint(session.OverlayPath)
		debug.Log("daemon", "Set session-specific overlay endpoint for fallback proxy %s: %s", fallbackProxyID, session.OverlayPath)
	} else if overlayEndpoint := d.OverlayEndpoint(); overlayEndpoint != "" {
		server.SetOverlayEndpoint(overlayEndpoint)
		debug.Log("daemon", "Set global overlay endpoint for fallback proxy %s: %s", fallbackProxyID, overlayEndpoint)
	} else {
		debug.Log("daemon", "No overlay endpoint found for fallback proxy %s — proxy→agent messages will not work", fallbackProxyID)
	}

	// Track script → proxy so ScriptStopped tears this proxy down. Mirrors
	// the URL-detected path; keeps the reverse index consistent for the
	// health tracker and suppression logic.
	if event.ScriptID != "" {
		d.trackScriptProxy(event.ScriptID, fallbackProxyID)
	}

	// Register wait-for dependencies (script names in event.Config.WaitFor).
	d.registerProxyDependencies(server, fallbackProxyID, event.Config, event.Path)

	d.startupErrorStore.Add(&StartupLogEntry{
		ProcessID:  event.ScriptID,
		ScriptName: event.ProxyName,
		Level:      "info",
		EventType:  "startup_proxy_fallback_used",
		Message:    fmt.Sprintf("created fallback proxy %s for script %s targeting %s", event.ProxyName, event.Config.Script, targetURL),
		Port:       event.Config.FallbackPort,
		Timestamp:  time.Now(),
	})
	debug.Log("daemon", "Created fallback proxy %s targeting %s (script %s)", fallbackProxyID, targetURL, event.Config.Script)
}

// handleScriptStopped handles script stopped events.
// Stops all proxies associated with the script.
func (d *Daemon) handleScriptStopped(event ProxyEvent) {
	debug.Log("daemon", "Script stopped: %s, cleaning up proxies", event.ScriptID)

	// Get all proxies for this script
	d.scriptProxyMu.RLock()
	proxyIDs := d.scriptProxies[event.ScriptID]
	d.scriptProxyMu.RUnlock()

	if len(proxyIDs) == 0 {
		debug.Log("daemon", "No proxies to clean up for script %s", event.ScriptID)
		return
	}

	// Stop each proxy
	for _, proxyID := range proxyIDs {
		debug.Log("daemon", "Stopping proxy %s (script: %s)", proxyID, event.ScriptID)
		if err := d.proxym.Stop(d.ctx, proxyID); err != nil {
			debug.Warn("daemon", "Failed to stop proxy %s: %v", proxyID, err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: event.ScriptID,
				Level:     "warning",
				EventType: "proxy_stop_failed",
				Message:   fmt.Sprintf("failed to stop proxy %s on script stop: %v", proxyID, err),
				Timestamp: time.Now(),
			})
		}
	}

	// Clear tracking
	d.clearScriptProxies(event.ScriptID)
}

// FlushScriptProxyConnections closes idle connections on all proxies linked to a script.
// Call this when a backend process restarts to avoid stale connection errors.
func (d *Daemon) FlushScriptProxyConnections(scriptID string) {
	d.scriptProxyMu.RLock()
	proxyIDs := d.scriptProxies[scriptID]
	d.scriptProxyMu.RUnlock()

	for _, proxyID := range proxyIDs {
		if ps, err := d.proxym.Get(proxyID); err == nil {
			ps.FlushConnections()
		}
	}
}

// trackScriptProxy records a script -> proxy association.
//
// Maintains both the forward index (scriptID → []proxyID, used to clean up
// proxies on script stop) and the reverse index (proxyID → scriptID, used
// by the health tracker to look up the linked process for suppression
// decisions on every proxy log entry).
func (d *Daemon) trackScriptProxy(scriptID, proxyID string) {
	d.scriptProxyMu.Lock()
	defer d.scriptProxyMu.Unlock()

	d.scriptProxies[scriptID] = append(d.scriptProxies[scriptID], proxyID)
	if d.proxyToScript == nil {
		d.proxyToScript = make(map[string]string)
	}
	d.proxyToScript[proxyID] = scriptID
	debug.Log("daemon", "Tracked proxy %s for script %s", proxyID, scriptID)
}

// linkedScriptForProxy returns the script ID a proxy is linked to, or
// empty string for unlinked proxies. Lock-free for hot-path callers when
// the entry already exists, taking only an RLock for the lookup.
func (d *Daemon) linkedScriptForProxy(proxyID string) string {
	d.scriptProxyMu.RLock()
	defer d.scriptProxyMu.RUnlock()
	if d.proxyToScript == nil {
		return ""
	}
	return d.proxyToScript[proxyID]
}

// getProxiesForScript returns all proxy IDs for a script.
func (d *Daemon) getProxiesForScript(scriptID string) []string {
	d.scriptProxyMu.RLock()
	defer d.scriptProxyMu.RUnlock()

	// Return copy
	proxies := d.scriptProxies[scriptID]
	result := make([]string, len(proxies))
	copy(result, proxies)
	return result
}

// clearScriptProxies removes all proxy tracking for a script.
//
// Drops both the forward index entry and any reverse index entries for
// proxies owned by the script. Also instructs the health tracker to
// forget the script so it does not retain a stale lastHealthyAt entry
// for a process that no longer exists.
func (d *Daemon) clearScriptProxies(scriptID string) {
	d.scriptProxyMu.Lock()
	proxyIDs := d.scriptProxies[scriptID]
	delete(d.scriptProxies, scriptID)
	if d.proxyToScript != nil {
		for _, proxyID := range proxyIDs {
			delete(d.proxyToScript, proxyID)
		}
	}
	d.scriptProxyMu.Unlock()

	if d.healthTracker != nil {
		d.healthTracker.Forget(scriptID)
	}
	if d.outageClassifier != nil {
		d.outageClassifier.Forget(scriptID)
	}
	debug.Log("daemon", "Cleared proxy tracking for script %s", scriptID)
}

// makeProxyIDFromURL creates a unique proxy ID from project path, proxy name, and URL.
// Format: {projectPath}:{proxyName}:{host}:{port}
func makeProxyIDFromURL(projectPath, proxyName, urlStr string) string {
	// Parse URL to extract host and port
	u, err := url.Parse(urlStr)
	if err != nil {
		// Fallback to simple ID if URL parsing fails
		return makeProcessID(projectPath, proxyName)
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		// Default ports
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	// Sanitize host for use in ID (replace dots and colons with dashes)
	cleanHost := strings.ReplaceAll(host, ".", "-")
	cleanHost = strings.ReplaceAll(cleanHost, ":", "-")

	return fmt.Sprintf("%s:%s-%s", makeProcessID(projectPath, proxyName), cleanHost, port)
}
