package daemon

import (
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/devoidc"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
)

// applyDevOIDC installs the project's dev OIDC issuer on a new proxy. It is
// called from the proxy-creation chokepoint (wireProxyLogger), so every
// creation path gets it.
func (d *Daemon) applyDevOIDC(server *proxy.ProxyServer) {
	if server == nil || server.Path == "" {
		return
	}
	cfg, err := config.LoadAgntConfig(server.Path)
	if err != nil || cfg == nil {
		// A config that does not load is reported by the paths that start
		// scripts and proxies from it; here it only means "no issuer".
		server.SetDevOIDC(nil)
		return
	}
	server.SetDevOIDC(d.devIssuerFor(server.Path, cfg.DevOIDC, server))
}

// applyDevOIDCProject re-applies the dev-oidc block to every running proxy
// of the project after a config reconcile, so editing personas or clients
// takes effect without restarting anything.
func (d *Daemon) applyDevOIDCProject(projectPath string, cfg *config.AgntConfig) {
	if cfg == nil {
		return
	}
	for _, p := range d.proxym.ListScoped(scope.Project(projectPath)) {
		p.SetDevOIDC(d.devIssuerFor(projectPath, cfg.DevOIDC, p))
	}
}

// devIssuerFor returns the project's issuer updated to block, creating it on
// first use, or nil when the project declares no dev-oidc block. One issuer
// (one signing key) serves every proxy of the project.
func (d *Daemon) devIssuerFor(projectPath string, block *config.DevOIDCConfig, server *proxy.ProxyServer) *devoidc.Issuer {
	key := normalizePath(projectPath)
	if block == nil {
		d.devIssuers.Delete(key)
		return nil
	}
	cfg := devIssuerConfig(block)
	if v, ok := d.devIssuers.Load(key); ok {
		is := v.(*devoidc.Issuer)
		is.SetConfig(cfg)
		return is
	}
	is, err := devoidc.New(cfg)
	if err != nil {
		server.Logger().LogDiagnostic(proxy.ProxyDiagnostic{
			Timestamp: time.Now(), Level: proxy.DiagnosticError, Category: "dev-oidc",
			Event: "dev_oidc_failed", Message: fmt.Sprintf("dev-oidc issuer not started: %v", err),
			Data: map[string]any{"proxy_id": server.ID},
		})
		return nil
	}
	if actual, loaded := d.devIssuers.LoadOrStore(key, is); loaded {
		is = actual.(*devoidc.Issuer)
		is.SetConfig(cfg)
	}
	return is
}

// devIssuerConfig translates the validated dev-oidc block.
func devIssuerConfig(b *config.DevOIDCConfig) devoidc.Config {
	out := devoidc.Config{
		Issuer:         b.Issuer,
		Clients:        make(map[string]devoidc.Client, len(b.Clients)),
		Personas:       make(map[string]devoidc.Persona, len(b.Personas)),
		DefaultPersona: b.DefaultPersona,
		Allow:          b.Allow,
	}
	for id, c := range b.Clients {
		out.Clients[id] = devoidc.Client{
			ID: id, RedirectURIs: c.RedirectURIs, Secret: c.Secret, Audience: c.Audience,
			LoginPath: c.LoginPath, SessionCookies: c.SessionCookies,
		}
	}
	for name, p := range b.Personas {
		out.Personas[name] = devoidc.Persona{
			Name: name, Email: p.Email, DisplayName: p.Name, Roles: p.Roles, Claims: p.Claims,
		}
	}
	return out
}
