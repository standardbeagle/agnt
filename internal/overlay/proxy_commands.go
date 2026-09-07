package overlay

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
)

// Overview palette commands that act on a proxy: opening a tunnel in front of
// one, and pinning this node's tailnet address as its status URL.

// resolveProxy picks the proxy a command should act on. With no id given it
// resolves to the only proxy when there is exactly one — the common case, and
// the one where making the developer retype an id they can see on screen would
// be pure friction. With several it refuses and NAMES them, because guessing
// "the first one" would silently point a public tunnel at the wrong service.
func resolveProxy(proxies []ProxyInfo, id string) (ProxyInfo, error) {
	if len(proxies) == 0 {
		return ProxyInfo{}, fmt.Errorf("no proxies are running")
	}
	if id != "" {
		for _, p := range proxies {
			if p.ID == id {
				return p, nil
			}
		}
		var matches []ProxyInfo
		for _, p := range proxies {
			_, localID, scoped := strings.Cut(p.ID, ":")
			if p.ConfigName == id || (scoped && localID == id) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return ProxyInfo{}, fmt.Errorf("several proxies match %q — use one of: %s", id, proxyIDList(matches))
		}
		return ProxyInfo{}, fmt.Errorf("no proxy %q (running: %s)", id, proxyIDList(proxies))
	}
	if len(proxies) == 1 {
		return proxies[0], nil
	}
	return ProxyInfo{}, fmt.Errorf("several proxies are running — name one: %s", proxyIDList(proxies))
}

func proxyIDList(proxies []ProxyInfo) string {
	ids := make([]string, 0, len(proxies))
	for _, p := range proxies {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

// listenPortOf extracts the TCP port a proxy is bound to. The tunnel has to be
// pointed at the proxy's own port, not the backend's: tunnelling straight to
// the backend would bypass the proxy and with it every piece of instrumentation
// the overlay depends on.
func listenPortOf(p ProxyInfo) (int, error) {
	idx := strings.LastIndex(p.ListenAddr, ":")
	if idx == -1 {
		return 0, fmt.Errorf("proxy %s has no listen port yet", p.ID)
	}
	port, err := strconv.Atoi(p.ListenAddr[idx+1:])
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("proxy %s has an unreadable listen address %q", p.ID, p.ListenAddr)
	}
	return port, nil
}

// tunnelProviders is the set the daemon accepts. Kept here so the overlay
// rejects a typo with the legal set in hand rather than making the developer
// wait for a round trip to find out.
var tunnelProviders = []string{"cloudflare", "ngrok", "tailscale"}

func validTunnelProvider(name string) bool {
	for _, p := range tunnelProviders {
		if p == name {
			return true
		}
	}
	return false
}

// runTunnelCommand handles `:tunnel <provider> [proxy]`.
func (r *InputRouter) runTunnelCommand(args string) error {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return fmt.Errorf("tunnel needs a provider: %s", strings.Join(tunnelProviders, " | "))
	}
	provider := strings.ToLower(fields[0])
	if !validTunnelProvider(provider) {
		return fmt.Errorf("unknown provider %q (use %s)", provider, strings.Join(tunnelProviders, " | "))
	}
	var proxyID string
	if len(fields) > 1 {
		proxyID = fields[1]
	}

	proxy, err := resolveProxy(r.overlay.GetStatus().Proxies, proxyID)
	if err != nil {
		return err
	}
	port, err := listenPortOf(proxy)
	if err != nil {
		return err
	}

	url, err := r.scriptController.StartTunnel(provider, proxy.ID, port)
	if err != nil {
		return err
	}
	if url == "" {
		// The daemon only answers once it has a URL, so an empty one means the
		// provider came up without publishing an address — say so rather than
		// reporting a success the developer cannot use.
		return fmt.Errorf("tunnel started but published no URL")
	}
	r.overlay.Notify(Notification{
		Level: LevelInfo,
		Text:  fmt.Sprintf("%s tunnel for %s: %s", provider, proxy.ID, url),
	})
	return nil
}

// runTailscaleURLCommand handles `:tailscale-url [proxy]`: take the tailnet
// address this node already resolves to, write it into .agnt.kdl as the
// proxy's status-url, and live-apply it.
//
// Writing the config is the point of the command. Setting it only in memory
// would look identical on screen and evaporate on the next daemon restart,
// which is exactly the kind of state a developer would then have to rediscover.
func (r *InputRouter) runTailscaleURLCommand(args string) error {
	proxy, err := resolveProxy(r.overlay.GetStatus().Proxies, strings.TrimSpace(args))
	if err != nil {
		return err
	}
	if proxy.TailscaleURL == "" {
		// getTailscaleDNS caches asynchronously, so an empty value means either
		// no tailscale on this machine or a first call that has not resolved
		// yet. Both are worth distinguishing from "pinned nothing".
		return fmt.Errorf("no tailnet address for %s yet — is tailscale running on this machine?", proxy.ID)
	}

	projectPath := r.scriptController.ProjectPath()
	if projectPath == "" {
		return fmt.Errorf("no project directory to write .agnt.kdl into")
	}
	configPath := filepath.Join(projectPath, config.AgntConfigFileName)
	configName := proxy.ConfigName
	if configName == "" {
		configName = proxy.ID
	}
	if err := config.SetProxyStatusURL(configPath, configName, proxy.TailscaleURL); err != nil {
		return err
	}
	if err := r.scriptController.ReconcileConfig(); err != nil {
		// The file is already written, so the pin survives a restart either
		// way; only the live update failed. Say which half happened.
		return fmt.Errorf("wrote status-url to .agnt.kdl but could not apply it live: %w", err)
	}
	r.overlay.Notify(Notification{
		Level: LevelInfo,
		Text:  fmt.Sprintf("pinned %s as the status URL for %s", proxy.TailscaleURL, proxy.ID),
	})
	return nil
}
