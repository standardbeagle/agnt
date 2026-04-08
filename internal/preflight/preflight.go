package preflight

import (
	"context"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// PortConflict describes an unmanaged process blocking a declared port.
type PortConflict struct {
	ScriptName  string `json:"script_name"`
	Port        int    `json:"port"`
	PIDs        []int  `json:"pids"`
	ProcessName string `json:"process_name,omitempty"`
}

// DetectPortConflicts scans all declared ports from autostart scripts and
// returns conflicts where unmanaged processes hold those ports.
func DetectPortConflicts(ctx context.Context, scripts map[string]*config.ScriptConfig, managedPIDs map[int]bool) []PortConflict {
	var conflicts []PortConflict

	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sc := scripts[name]
		for _, port := range sc.Ports {
			pids := config.FindPIDsByPort(ctx, port)
			if len(pids) == 0 {
				continue
			}
			var unmanaged []int
			for _, pid := range pids {
				if managedPIDs != nil && managedPIDs[pid] {
					continue
				}
				unmanaged = append(unmanaged, pid)
			}
			if len(unmanaged) == 0 {
				continue
			}
			procName := config.ProcessNameByPID(unmanaged[0])
			conflicts = append(conflicts, PortConflict{
				ScriptName:  name,
				Port:        port,
				PIDs:        unmanaged,
				ProcessName: procName,
			})
		}
	}
	return conflicts
}

// KillResult reports what happened for each conflict.
type KillResult struct {
	PortConflict
	Killed bool   `json:"killed"`
	Error  string `json:"error,omitempty"`
}

// KillPortBlockers kills processes blocking declared ports using the
// ProcessManager's full escalation path (process groups + descendants).
// Verifies each port is free after kill.
func KillPortBlockers(ctx context.Context, pm *goprocess.ProcessManager, conflicts []PortConflict) []KillResult {
	results := make([]KillResult, len(conflicts))

	for i, c := range conflicts {
		results[i].PortConflict = c

		_, err := pm.KillProcessByPort(ctx, c.Port)
		if err != nil {
			results[i].Error = fmt.Sprintf("kill failed for port %d: %v", c.Port, err)
			continue
		}

		if WaitPortFree(c.Port, 2*time.Second) {
			results[i].Killed = true
		} else {
			results[i].Error = fmt.Sprintf("port %d still in use after kill", c.Port)
		}
	}

	return results
}

// WaitPortFree polls until port is free or timeout expires.
func WaitPortFree(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
