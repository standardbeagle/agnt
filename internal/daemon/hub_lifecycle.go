package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
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
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
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

	// Build restart manifests from running resources
	type procManifest struct {
		ID          string
		Command     string
		Args        []string
		ProjectPath string
		WorkingDir  string
	}
	type proxyManifest struct {
		ID            string
		TargetURL     string
		Port          int
		MaxLogSize    int
		ProjectPath   string
		BindAddress   string
		AllowExternal bool
	}

	var procsToRestart []procManifest
	var proxiesToRestart []proxyManifest

	for _, p := range runningProcs {
		if p.State().String() == "running" {
			procsToRestart = append(procsToRestart, procManifest{
				ID:          p.ID,
				Command:     p.Command,
				Args:        p.Args,
				ProjectPath: p.ProjectPath,
				WorkingDir:  p.WorkingDir,
			})
			// Mark this stop as daemon-initiated so the health.OutageClassifier
			// biases the imminent outage toward Rebuild rather than Crash.
			d.healthTracker.MarkDaemonInitiatedStop(p.ID)
		}
	}

	for _, p := range runningProxies {
		if p.IsRunning() {
			proxiesToRestart = append(proxiesToRestart, proxyManifest{
				ID:            p.ID,
				TargetURL:     p.TargetURL.String(),
				Port:          0, // Will use auto-port
				MaxLogSize:    int(p.Logger().Stats().MaxSize),
				ProjectPath:   p.Path,
				BindAddress:   p.BindAddress,
				AllowExternal: p.AllowExternal,
			})
		}
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
		// Use startScriptWithRetry for EADDRINUSE recovery
		_, startupErr := d.startScriptWithRetry(ctx, pm.ID, pm.ProjectPath, pm.WorkingDir, pm.Command, pm.Args, nil, nil, false)
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
		proxyServer, err := d.proxym.Create(ctx, proxy.ProxyConfig{
			ID:            pm.ID,
			TargetURL:     pm.TargetURL,
			ListenPort:    pm.Port,
			MaxLogSize:    pm.MaxLogSize,
			Path:          pm.ProjectPath,
			BindAddress:   pm.BindAddress,
			AllowExternal: pm.AllowExternal,
		})
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
