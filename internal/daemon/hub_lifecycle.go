package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleStopAll(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "STOP-ALL: args=%v", cmd.Args)
	// Count resources before stopping
	procsBefore := len(d.hub.ProcessManager().List())
	proxiesBefore := len(d.proxym.List())
	tunnelsBefore := d.tunnelm.ActiveCount()

	// Stop all resources
	d.StopAllResources(ctx)

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
	// Capture running resources before stop
	runningProcs := d.hub.ProcessManager().List()
	runningProxies := d.proxym.List()

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
			// Mark this stop as daemon-initiated so the OutageClassifier
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

	// Stop all resources
	d.StopAllResources(ctx)

	// Wait a moment for cleanup
	time.Sleep(100 * time.Millisecond)

	// Restart processes
	var procsRestarted, procsFailed int
	var proxyRestarted, proxyFailed int

	for _, pm := range procsToRestart {
		// Use startScriptWithRetry for EADDRINUSE recovery
		_, startupErr := d.startScriptWithRetry(ctx, pm.ID, pm.ProjectPath, pm.WorkingDir, pm.Command, pm.Args, nil, nil)
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
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: pm.ID,
				Level:     "error",
				EventType: "proxy_restart_failed",
				Message:   fmt.Sprintf("RESTART-ALL: failed to restart proxy: %v", err),
				Timestamp: time.Now(),
			})
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
