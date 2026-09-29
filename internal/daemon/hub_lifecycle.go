package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/scope"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// procsForProject filters a process list down to the ones owned by projectPath.
// Path comparison is normalized so it matches the scope chokepoint's output.
func procsForProject(procs []*goprocess.ManagedProcess, projectPath string) []*goprocess.ManagedProcess {
	norm := normalizePath(projectPath)
	out := make([]*goprocess.ManagedProcess, 0, len(procs))
	for _, p := range procs {
		if normalizePath(p.ProjectPath) == norm {
			out = append(out, p)
		}
	}
	return out
}

func (d *Daemon) hubHandleStopAll(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "STOP-ALL: args=%v", cmd.Args)

	// Route through the mandatory session-scope chokepoint. A non-global caller
	// with no resolvable session fails loud rather than tearing down every
	// project's resources (the tenancy leak this split fixes). An explicit
	// global:true is the deliberate, audited daemon-wide path.
	filter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid STOP-ALL filter JSON: %v", err))
	}
	projectPath, global, err := d.resolveProjectScope(filter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
	}

	// Count resources before stopping, scoped to the resolved project.
	var procsBefore, proxiesBefore, tunnelsBefore int
	if global {
		procsBefore = len(d.hub.ProcessManager().List())
		proxiesBefore = len(d.proxym.ListScoped(scope.Unscoped("STOP-ALL global: count every proxy")))
		tunnelsBefore = d.tunnelm.ActiveCount()
		d.StopAllResources(ctx)
	} else {
		procsBefore = len(procsForProject(d.hub.ProcessManager().List(), projectPath))
		proxiesBefore = len(d.proxym.ListScoped(scope.Project(projectPath)))
		tunnelsBefore = len(d.tunnelm.ListByPath(projectPath))
		d.StopProjectResources(ctx, projectPath)
	}

	resp := map[string]interface{}{
		"success":           true,
		"processes_stopped": procsBefore,
		"proxies_stopped":   proxiesBefore,
		"tunnels_stopped":   tunnelsBefore,
		"message":           fmt.Sprintf("Stopped %d processes, %d proxies, %d tunnels", procsBefore, proxiesBefore, tunnelsBefore),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleRestartAll handles the RESTART-ALL command.
// Stops all resources and restarts them with the same configuration.

func (d *Daemon) hubHandleRestartAll(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "RESTART-ALL: args=%v", cmd.Args)

	// Same scope chokepoint as STOP-ALL: snapshot/restart only the resolved
	// project's resources; explicit global:true restarts every project's.
	filter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid RESTART-ALL filter JSON: %v", err))
	}
	projectPath, global, err := d.resolveProjectScope(filter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
	}

	// Capture running resources before stop, scoped to the resolved project.
	var runningProcs []*goprocess.ManagedProcess
	var runningProxies []*proxy.ProxyServer
	if global {
		runningProcs = d.hub.ProcessManager().List()
		runningProxies = d.proxym.ListScoped(scope.Unscoped("RESTART-ALL global: restart every proxy"))
	} else {
		runningProcs = procsForProject(d.hub.ProcessManager().List(), projectPath)
		runningProxies = d.proxym.ListScoped(scope.Project(projectPath))
	}

	// Build restart manifests from running resources. Where the daemon still
	// holds the declared .agnt.kdl config for a script/proxy (scriptConfigs /
	// proxyConfigs, populated by the autostart and explicit-start paths), the
	// manifest carries that config and restart reuses the exact same
	// construction helpers autostart uses (StartScriptExplicit,
	// buildProxyServerConfig) — so env, ports, url-matchers, auto-restart,
	// bind/status/public URL, skip-tls-verify and the cloudflare-tunnel block
	// all survive RESTART-ALL. Only resources with no stored config (ad-hoc
	// MCP-started processes/proxies never declared in .agnt.kdl) fall back to
	// the narrower manifest rebuilt from the live ManagedProcess/ProxyServer.
	type procManifest struct {
		ID          string
		Name        string
		Command     string
		Args        []string
		Env         []string
		ProjectPath string
		WorkingDir  string
		ScriptCfg   *config.ScriptConfig // nil when not config-declared
	}
	type proxyManifest struct {
		ID            string
		Name          string
		TargetURL     string
		MaxLogSize    int
		ProjectPath   string
		BindAddress   string
		AllowExternal bool
		NamedTunnel   *proxy.NamedTunnelConfig
		Cfg           *config.ProxyConfig // nil when not config-declared
	}

	var procsToRestart []procManifest
	var proxiesToRestart []proxyManifest

	// Per-project config cache: RESTART-ALL may span many projects (global)
	// or just one; load each project's .agnt.kdl at most once regardless of
	// how many of its scripts/proxies are being restarted.
	loadedConfigs := make(map[string]*config.AgntConfig)
	loadProjectConfig := func(projectPath string) *config.AgntConfig {
		norm := normalizePath(projectPath)
		if cfg, ok := loadedConfigs[norm]; ok {
			return cfg
		}
		cfg, err := config.LoadAgntConfig(projectPath)
		if err != nil {
			debug.Warn("daemon", "RESTART-ALL: failed to load .agnt.kdl for %s: %v", projectPath, err)
			cfg = nil
		}
		loadedConfigs[norm] = cfg
		return cfg
	}

	for _, p := range runningProcs {
		if p.State().String() != "running" {
			continue
		}
		name := stripProcessPrefix(p.ID)
		var scriptCfg *config.ScriptConfig
		if v, ok := d.scriptConfigs.Load(p.ID); ok {
			scriptCfg = v.(*config.ScriptConfig)
		}
		procsToRestart = append(procsToRestart, procManifest{
			ID:          p.ID,
			Name:        name,
			Command:     p.Command,
			Args:        p.Args,
			Env:         p.Env,
			ProjectPath: p.ProjectPath,
			WorkingDir:  p.WorkingDir,
			ScriptCfg:   scriptCfg,
		})
		// Mark this stop as daemon-initiated so the health.OutageClassifier
		// biases the imminent outage toward Rebuild rather than Crash.
		d.healthTracker.MarkDaemonInitiatedStop(p.ID)
	}

	for _, p := range runningProxies {
		if !p.IsRunning() {
			continue
		}
		var name string
		var proxyCfg *config.ProxyConfig
		if v, ok := d.proxyConfigs.Load(p.ID); ok {
			name = v.(configuredProxy).name
			if agc := loadProjectConfig(p.Path); agc != nil {
				proxyCfg = agc.Proxies[name]
			}
		}
		proxiesToRestart = append(proxiesToRestart, proxyManifest{
			ID:            p.ID,
			Name:          name,
			TargetURL:     p.TargetURL.String(),
			MaxLogSize:    int(p.Logger().Stats().MaxSize),
			ProjectPath:   p.Path,
			BindAddress:   p.BindAddress,
			AllowExternal: p.AllowExternal,
			NamedTunnel:   p.NamedTunnelConfig(),
			Cfg:           proxyCfg,
		})
	}

	// Stop the snapshotted resources (global daemon-wide, or project-scoped).
	if global {
		d.StopAllResources(ctx)
	} else {
		d.StopProjectResources(ctx, projectPath)
	}

	// Wait a moment for cleanup
	time.Sleep(100 * time.Millisecond)

	// Restart processes
	var procsRestarted, procsFailed int
	var proxyRestarted, proxyFailed int

	for _, pm := range procsToRestart {
		var startupErr *StartupError
		if pm.ScriptCfg != nil {
			// Config-declared script: reuse the exact autostart construction
			// path (env, expected ports, url-matchers, auto-restart, hooks)
			// instead of the narrower ad-hoc manifest.
			var proxyConfigs map[string]*config.ProxyConfig
			if agc := loadProjectConfig(pm.ProjectPath); agc != nil {
				proxyConfigs = agc.Proxies
			}
			if err := d.StartScriptExplicit(ctx, pm.Name, pm.ScriptCfg, pm.ProjectPath, proxyConfigs); err != nil {
				startupErr = &StartupError{ProcessID: pm.ID, ErrorType: "restart_failed", Message: err.Error()}
			}
		} else {
			// Ad-hoc process with no stored config: best-effort rebuild from
			// the live ManagedProcess. Carries Env forward (previously
			// dropped as nil); ExpectedPorts/AutoRestart are inherently
			// unknown for a process .agnt.kdl never declared.
			_, startupErr = d.startScriptWithRetry(ctx, pm.ID, pm.ProjectPath, pm.WorkingDir, pm.Command, pm.Args, pm.Env, nil, false)
		}
		if startupErr != nil {
			debug.Error("daemon", "Failed to restart process %s: %v", pm.ID, startupErr)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: pm.ID,
				Level:     "error",
				EventType: "restart_failed",
				Message:   fmt.Sprintf("RESTART-ALL: failed to restart: %s", startupErr.Message),
				Output:    startupErr.Output,
				Port:      startupErr.Port,
				Timestamp: time.Now(),
			})
			procsFailed++
		} else {
			procsRestarted++
		}
	}

	// Restart proxies
	for _, pm := range proxiesToRestart {
		var proxyServerConfig proxy.ProxyConfig
		if pm.Cfg != nil {
			// Config-declared proxy: reuse the same construction helper the
			// autostart/explicit-start event path uses, so listen-port,
			// skip-tls-verify, bind, status/public URL and the
			// cloudflare-tunnel block all survive the restart.
			proxyServerConfig = buildProxyServerConfig(pm.ID, pm.TargetURL, pm.ProjectPath, pm.Cfg)
		} else {
			proxyServerConfig = proxy.ProxyConfig{
				ID:            pm.ID,
				TargetURL:     pm.TargetURL,
				ListenPort:    -1, // hash-based auto-port, matches buildProxyServerConfig's default
				MaxLogSize:    pm.MaxLogSize,
				Path:          pm.ProjectPath,
				BindAddress:   pm.BindAddress,
				AllowExternal: pm.AllowExternal,
				NamedTunnel:   pm.NamedTunnel,
			}
		}
		proxyServer, err := d.proxym.Create(ctx, proxyServerConfig)
		if err != nil {
			debug.Error("daemon", "Failed to restart proxy %s: %v", pm.ID, err)
			d.recordStartupEntry(pm.ID, "", "error", "proxy_restart_failed",
				fmt.Sprintf("RESTART-ALL: failed to restart proxy: %v", err), 0)
			proxyFailed++
		} else {
			d.wireProxyLogger(proxyServer)
			proxyRestarted++
		}
	}

	resp := map[string]interface{}{
		"success":             true,
		"processes_restarted": procsRestarted,
		"processes_failed":    procsFailed,
		"proxies_restarted":   proxyRestarted,
		"proxies_failed":      proxyFailed,
		"message": fmt.Sprintf("Restarted %d/%d processes, %d/%d proxies",
			procsRestarted, len(procsToRestart), proxyRestarted, len(proxiesToRestart)),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProcRestart handles PROC RESTART <id>.
// Stops a process and restarts it with the same configuration.
// If a rogue process is using the expected port, it will be killed first.
