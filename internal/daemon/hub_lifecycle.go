package daemon

import (
	"context"
	"encoding/json"
	"errors"
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
//
// It restarts, in place, every running process and proxy in scope, each
// through the same helper its single-resource verb uses (restartProcess,
// restartProxy). A restarted resource therefore keeps what it was launched
// with — env, working directory, auto-restart registration, bound port, public
// URL, TLS setting, overlay binding, incident owner, persisted state. Tunnels
// and browser sessions are left running: a proxy comes back on the same port,
// so what points at it keeps working.
func (d *Daemon) hubHandleRestartAll(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "RESTART-ALL: args=%v", cmd.Args)

	// Same scope chokepoint as STOP-ALL: restart only the resolved project's
	// resources; explicit global:true restarts every project's.
	filter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid RESTART-ALL filter JSON: %v", err))
	}
	projectPath, global, err := d.resolveProjectScope(filter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
	}

	var procs []*goprocess.ManagedProcess
	var proxies []*proxy.ProxyServer
	if global {
		procs = d.hub.ProcessManager().List()
		proxies = d.proxym.ListScoped(scope.Unscoped("RESTART-ALL global: restart every proxy"))
	} else {
		procs = procsForProject(d.hub.ProcessManager().List(), projectPath)
		proxies = d.proxym.ListScoped(scope.Project(projectPath))
	}

	var procsTotal, procsRestarted, procsFailed int
	var proxyTotal, proxyRestarted, proxyFailed int

	for _, p := range procs {
		if p.State().String() != "running" {
			continue
		}
		procsTotal++
		if _, restartErr := d.restartProcess(ctx, p, conn.SessionCode()); restartErr != nil {
			debug.Error("daemon", "Failed to restart process %s: %v", p.ID, restartErr)
			entry := &StartupLogEntry{
				ProcessID: p.ID,
				Level:     "error",
				EventType: "restart_failed",
				Message:   fmt.Sprintf("RESTART-ALL: failed to restart: %v", restartErr),
				Timestamp: time.Now(),
			}
			var startupErr *StartupError
			if errors.As(restartErr, &startupErr) {
				entry.Output = startupErr.Output
				entry.Port = startupErr.Port
			}
			d.startupErrorStore.Add(entry)
			procsFailed++
			continue
		}
		procsRestarted++
	}

	for _, p := range proxies {
		if !p.IsRunning() {
			continue
		}
		proxyTotal++
		if _, restartErr := d.restartProxy(ctx, p, conn.SessionCode()); restartErr != nil {
			debug.Error("daemon", "Failed to restart proxy %s: %v", p.ID, restartErr)
			d.recordStartupEntry(p.ID, "", "error", "proxy_restart_failed",
				fmt.Sprintf("RESTART-ALL: failed to restart proxy: %v", restartErr), 0)
			proxyFailed++
			continue
		}
		proxyRestarted++
	}

	resp := map[string]interface{}{
		// Not a success when something that was running did not come back.
		"success":             procsFailed == 0 && proxyFailed == 0,
		"processes_restarted": procsRestarted,
		"processes_failed":    procsFailed,
		"proxies_restarted":   proxyRestarted,
		"proxies_failed":      proxyFailed,
		"message": fmt.Sprintf("Restarted %d/%d processes, %d/%d proxies",
			procsRestarted, procsTotal, proxyRestarted, proxyTotal),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProcRestart handles PROC RESTART <id>.
// Stops a process and restarts it with the same configuration.
// If a rogue process is using the expected port, it will be killed first.
