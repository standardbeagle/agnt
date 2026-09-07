package overlay

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
	proxypkg "github.com/standardbeagle/agnt/internal/proxy"
)

// Overview palette commands that act on a proxy: opening a tunnel in front of
// one, and moving one onto this node's tailnet address.

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

// runTailscaleCommand handles `:tailscale [proxy]`: serve the proxy on this
// node's tailnet address instead of loopback, and advertise it under the
// node's MagicDNS name.
//
// Both halves are needed. A proxy binds to 127.0.0.1 by default and answers
// nothing on the tailnet, so advertising the tailnet address alone would hand
// the developer a URL no other device can reach -- which is what this command
// used to do. The bind is written as the symbolic "tailscale" rather than the
// literal address because .agnt.kdl is shared across machines.
//
// Writing the config is how the rebind happens, not a side effect of it: the
// proxy's bind is part of its reconcile signature, so applying the file stops
// the loopback listener and starts the proxy on the tailnet address, down the
// same path any other proxy config change takes. That also means the change
// survives a daemon restart, which a live-only rebind would not.
func (r *InputRouter) runTailscaleCommand(args string) error {
	target, err := resolveProxy(r.overlay.GetStatus().Proxies, strings.TrimSpace(args))
	if err != nil {
		return err
	}
	if target.TailscaleURL == "" {
		// getTailscaleDNS caches asynchronously, so an empty value means either
		// no tailscale on this machine or a first call that has not resolved
		// yet. Both are worth distinguishing from "pinned nothing".
		return fmt.Errorf("no tailnet address for %s yet — is tailscale running on this machine?", target.ID)
	}
	if target.ConfigName == "" {
		// A proxy started through the tool path has no config node, and config
		// reconcile deliberately leaves it alone. Writing the keys anyway would
		// create a node nothing starts and report success for a rebind that
		// never happens.
		return fmt.Errorf("%s was started by hand, not declared in %s — there is no config node to rebind", target.ID, config.AgntConfigFileName)
	}

	projectPath := r.scriptController.ProjectPath()
	if projectPath == "" {
		return fmt.Errorf("no project directory to write %s into", config.AgntConfigFileName)
	}
	configPath := filepath.Join(projectPath, config.AgntConfigFileName)
	if err := config.SetProxyProperties(configPath, target.ConfigName, [][2]string{
		{"bind", proxypkg.BindTailscale},
		{"status-url", target.TailscaleURL},
	}); err != nil {
		return err
	}
	if err := r.scriptController.ReconcileConfig(); err != nil {
		// The file is already written, so the change survives a restart either
		// way; only the live rebind failed. Say which half happened.
		return fmt.Errorf("wrote the tailnet bind to %s but could not apply it live: %w", config.AgntConfigFileName, err)
	}
	r.overlay.Notify(Notification{
		Level: LevelInfo,
		Text:  fmt.Sprintf("%s now serves on %s", target.ID, target.TailscaleURL),
	})
	return nil
}
