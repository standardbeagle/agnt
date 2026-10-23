package daemon

import (
	"fmt"
	"runtime"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/memgov"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
)

// sessionReleaseEstimate is what a finished session is assumed to have freed,
// for the governor's "large release" trigger. It is deliberately a fixed
// estimate: the governor only needs to know that a project's proxies, logs and
// output rings just went away, not exactly how much.
const sessionReleaseEstimate = 8 << 20

// startMemoryGovernor installs the soft heap ceiling and starts the governor's
// check loop. Called from Start only: NewForTest daemons leave d.mem nil, and a
// process-wide memory limit has no place in a test binary.
func (d *Daemon) startMemoryGovernor() {
	limit := memgov.ApplyLimit()
	d.mem = memgov.New(memgov.Config{
		Limit: limit,
		OnReclaim: func(level memgov.Level, freed int64, names []string) {
			debug.Log("daemon", "memory governor: %s reclaim freed ~%dKB from %v", level, freed>>10, names)
			if level == memgov.Hard {
				// A Hard reclaim drops history an agent may have wanted, so it
				// must be visible, not just in the debug log.
				d.daemonStartupLog("warning", "memory_pressure_reclaim",
					fmt.Sprintf("heap neared the %dMB ceiling; trimmed %v (freed ~%dKB). Raise AGNT_MEMORY_LIMIT to keep more history.",
						limit>>20, names, freed>>10))
			}
		},
	})
	d.mem.Register(proxyHistoryReclaimer{d})
	d.mem.Register(incidentDedupReclaimer{d})
	d.mem.Register(sessionHostReclaimer{d})
	d.mem.Start(d.ctx)
}

// proxyHistoryReclaimer trims every proxy's traffic log and idle page sessions.
// It looks proxies up at reclaim time instead of registering one reclaimer per
// proxy, so there is no registration to retire when a proxy stops.
type proxyHistoryReclaimer struct{ d *Daemon }

func (proxyHistoryReclaimer) Name() string { return "proxy_history" }

func (r proxyHistoryReclaimer) all() []*proxy.ProxyServer {
	if r.d.proxym == nil {
		return nil
	}
	return r.d.proxym.ListScoped(scope.Unscoped("memory governor: reclaim spans every project's proxies"))
}

func (r proxyHistoryReclaimer) Size() int64 {
	var n int64
	for _, p := range r.all() {
		n += p.RetainedBytes()
	}
	return n
}

func (r proxyHistoryReclaimer) Reclaim(l memgov.Level) int64 {
	var n int64
	for _, p := range r.all() {
		n += p.Reclaim(l == memgov.Hard)
	}
	return n
}

// incidentDedupReclaimer drops expired dedup fingerprints (each holds two full
// events) from idle sessions.
type incidentDedupReclaimer struct{ d *Daemon }

func (incidentDedupReclaimer) Name() string { return "incident_dedup" }

func (r incidentDedupReclaimer) Size() int64 {
	if r.d.incidentBus == nil {
		return 0
	}
	// Roughly two events per fingerprint; sized in fingerprints x 1KB.
	return int64(r.d.incidentBus.DedupLen()) << 10
}

func (r incidentDedupReclaimer) Reclaim(memgov.Level) int64 {
	if r.d.incidentBus == nil {
		return 0
	}
	before, after := r.d.incidentBus.TrimDedup()
	return int64(before-after) << 10
}

// memoryStats is the governor's report for the doctor, with the runtime's own
// numbers so heap and OS-held memory can be compared.
func (d *Daemon) memoryStats() map[string]interface{} {
	if d.mem == nil {
		return nil
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]interface{}{
		"governor":       d.mem.Stats(),
		"heap_sys_mb":    int(m.HeapSys >> 20),
		"heap_idle_mb":   int(m.HeapIdle >> 20),
		"heap_released":  int(m.HeapReleased >> 20),
		"num_gc":         m.NumGC,
		"gc_cpu_percent": fmt.Sprintf("%.2f", m.GCCPUFraction*100),
	}
}

// sessionHostReclaimer retires session-host sessions that exited past their
// retention. The registry prunes only when a session is added, so without this
// an idle daemon kept each finished session's 1MiB scrollback.
type sessionHostReclaimer struct{ d *Daemon }

func (sessionHostReclaimer) Name() string { return "sessionhost_exited" }

func (r sessionHostReclaimer) Size() int64 {
	if r.d.sessionHosts == nil {
		return 0
	}
	return int64(r.d.sessionHosts.ExitedCount()) << 20
}

func (r sessionHostReclaimer) Reclaim(memgov.Level) int64 {
	if r.d.sessionHosts == nil {
		return 0
	}
	return int64(r.d.sessionHosts.PruneExited()) << 20
}
