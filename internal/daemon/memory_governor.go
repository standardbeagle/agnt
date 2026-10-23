package daemon

import (
	"fmt"
	"runtime"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/memgov"
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
	d.mem.Start(d.ctx)
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
