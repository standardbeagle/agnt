package daemon

import (
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// handleProxyEvents runs the proxy event handling loop.
// It listens for events and creates/destroys proxies accordingly.
func (d *Daemon) handleProxyEvents() {
	defer d.wg.Done()

	for {
		select {
		case <-d.ctx.Done():
			return
		case event := <-d.proxyEvents:
			switch event.Type {
			case URLDetected:
				d.handleURLDetected(event)
			case ExplicitStart:
				d.handleExplicitStart(event)
			case ScriptStopped:
				d.handleScriptStopped(event)
			default:
				log.Printf("[WARN] Unknown proxy event type: %d", event.Type)
			}
		}
	}
}

// handleURLDetected handles URL detection events from scripts.
// Creates proxies for any proxy configs linked to the script.
func (d *Daemon) handleURLDetected(event ProxyEvent) {
	log.Printf("[DEBUG] URL detected from %s: %s (path: %s)", event.ScriptID, event.URL, event.Path)

	// Get project path from event
	projectPath := event.Path
	if projectPath == "" {
		log.Printf("[WARN] No project path in URL detection event for script %s", event.ScriptID)
		return
	}

	// Extract script name from process ID (format: {basename}:{scriptName})
	parts := strings.SplitN(event.ScriptID, ":", 2)
	if len(parts) < 2 {
		log.Printf("[WARN] Cannot parse script name from script ID: %s", event.ScriptID)
		return
	}
	scriptName := parts[1]

	// Load agnt configuration
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		log.Printf("[WARN] Failed to load agnt config for %s: %v", projectPath, err)
		return
	}

	// Find proxy configs linked to this script
	for proxyName, proxyConfig := range agntConfig.Proxies {
		if proxyConfig.Script != scriptName {
			continue // Not linked to this script
		}

		// Create proxy for this URL
		proxyID := makeProxyIDFromURL(projectPath, proxyName, event.URL)

		// Check if proxy already exists
		if _, err := d.proxym.Get(proxyID); err == nil {
			log.Printf("[DEBUG] Proxy %s already exists, skipping", proxyID)
			continue
		}

		// Check proxy limit per session
		d.scriptProxyMu.RLock()
		currentCount := len(d.scriptProxies[event.ScriptID])
		d.scriptProxyMu.RUnlock()

		if currentCount >= 5 {
			log.Printf("[WARN] Proxy limit (5) reached for script %s, skipping %s", event.ScriptID, event.URL)
			continue
		}

		// Create proxy
		proxyServerConfig := proxy.ProxyConfig{
			ID:          proxyID,
			TargetURL:   event.URL,
			ListenPort:  -1, // Auto-assign
			MaxLogSize:  proxyConfig.MaxLogSize,
			AutoRestart: true,
			Path:        projectPath,
		}

		server, err := d.proxym.Create(d.ctx, proxyServerConfig)
		if err != nil {
			log.Printf("[ERROR] Failed to create proxy %s: %v", proxyID, err)
			continue
		}

		// Find session for this project to get session-specific overlay endpoint
		if session, ok := d.sessionRegistry.FindByDirectory(projectPath); ok && session.OverlayPath != "" {
			server.SetOverlayEndpoint(session.OverlayPath)
			log.Printf("[DEBUG] Set session-specific overlay endpoint for proxy %s: %s", proxyID, session.OverlayPath)
		} else if overlayEndpoint := d.OverlayEndpoint(); overlayEndpoint != "" {
			// Fallback to global overlay endpoint if no session found
			server.SetOverlayEndpoint(overlayEndpoint)
			log.Printf("[DEBUG] Set global overlay endpoint for proxy %s: %s", proxyID, overlayEndpoint)
		}

		// Track script → proxy association
		d.trackScriptProxy(event.ScriptID, proxyID)

		log.Printf("[DEBUG] Created proxy %s targeting %s", proxyID, event.URL)
	}
}

// handleExplicitStart handles explicit proxy start events (fully-specified proxies).
func (d *Daemon) handleExplicitStart(event ProxyEvent) {
	if event.Config == nil || event.ProxyID == "" {
		log.Printf("[WARN] Invalid ExplicitStart event: missing config or proxyID")
		return
	}

	// Check if already exists
	if _, err := d.proxym.Get(event.ProxyID); err == nil {
		log.Printf("[DEBUG] Proxy %s already exists, skipping", event.ProxyID)
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
		log.Printf("[WARN] ExplicitStart event for %s has no target URL", event.ProxyID)
		return
	}

	// Create proxy
	proxyServerConfig := proxy.ProxyConfig{
		ID:          event.ProxyID,
		TargetURL:   targetURL,
		ListenPort:  -1, // Auto-assign
		MaxLogSize:  event.Config.MaxLogSize,
		AutoRestart: true,
		Path:        event.Path,
	}

	server, err := d.proxym.Create(d.ctx, proxyServerConfig)
	if err != nil {
		log.Printf("[ERROR] Failed to create proxy %s: %v", event.ProxyID, err)
		return
	}

	// Find session for this project to get session-specific overlay endpoint
	if event.Path != "" {
		if session, ok := d.sessionRegistry.FindByDirectory(event.Path); ok && session.OverlayPath != "" {
			server.SetOverlayEndpoint(session.OverlayPath)
			log.Printf("[DEBUG] Set session-specific overlay endpoint for explicit proxy %s: %s", event.ProxyID, session.OverlayPath)
		} else if overlayEndpoint := d.OverlayEndpoint(); overlayEndpoint != "" {
			// Fallback to global overlay endpoint if no session found
			server.SetOverlayEndpoint(overlayEndpoint)
			log.Printf("[DEBUG] Set global overlay endpoint for explicit proxy %s: %s", event.ProxyID, overlayEndpoint)
		}
	} else if overlayEndpoint := d.OverlayEndpoint(); overlayEndpoint != "" {
		// Fallback to global overlay endpoint if no path specified
		server.SetOverlayEndpoint(overlayEndpoint)
		log.Printf("[DEBUG] Set global overlay endpoint for explicit proxy %s: %s", event.ProxyID, overlayEndpoint)
	}

	log.Printf("[DEBUG] Created explicit proxy %s targeting %s", event.ProxyID, targetURL)
}

// handleScriptStopped handles script stopped events.
// Stops all proxies associated with the script.
func (d *Daemon) handleScriptStopped(event ProxyEvent) {
	log.Printf("[DEBUG] Script stopped: %s, cleaning up proxies", event.ScriptID)

	// Get all proxies for this script
	d.scriptProxyMu.RLock()
	proxyIDs := d.scriptProxies[event.ScriptID]
	d.scriptProxyMu.RUnlock()

	if len(proxyIDs) == 0 {
		log.Printf("[DEBUG] No proxies to clean up for script %s", event.ScriptID)
		return
	}

	// Stop each proxy
	for _, proxyID := range proxyIDs {
		log.Printf("[DEBUG] Stopping proxy %s (script: %s)", proxyID, event.ScriptID)
		if err := d.proxym.Stop(d.ctx, proxyID); err != nil {
			log.Printf("[WARN] Failed to stop proxy %s: %v", proxyID, err)
		}
	}

	// Clear tracking
	d.clearScriptProxies(event.ScriptID)
}

// trackScriptProxy records a script → proxy association.
func (d *Daemon) trackScriptProxy(scriptID, proxyID string) {
	d.scriptProxyMu.Lock()
	defer d.scriptProxyMu.Unlock()

	d.scriptProxies[scriptID] = append(d.scriptProxies[scriptID], proxyID)
	log.Printf("[DEBUG] Tracked proxy %s for script %s", proxyID, scriptID)
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
func (d *Daemon) clearScriptProxies(scriptID string) {
	d.scriptProxyMu.Lock()
	defer d.scriptProxyMu.Unlock()

	delete(d.scriptProxies, scriptID)
	log.Printf("[DEBUG] Cleared proxy tracking for script %s", scriptID)
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
