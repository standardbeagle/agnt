package daemon

import (
	"context"
	"sort"

	"github.com/standardbeagle/agnt/internal/config"
)

// PortConflict describes an unmanaged process blocking a declared port.
type PortConflict struct {
	ScriptName  string `json:"script_name"`
	Port        int    `json:"port"`
	PIDs        []int  `json:"pids"`
	ProcessName string `json:"process_name,omitempty"`
}

// detectPortConflicts scans all declared ports from autostart scripts and
// returns conflicts where unmanaged processes hold those ports.
func detectPortConflicts(ctx context.Context, scripts map[string]*config.ScriptConfig, managedPIDs map[int]bool) []PortConflict {
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
