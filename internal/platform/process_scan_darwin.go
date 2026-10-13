//go:build darwin

package platform

import (
	"path/filepath"
	"strings"
)

// Scan returns all running processes on macOS.
//
// macOS has no /proc, so the /proc walk the Linux build uses fails outright
// here. Until this file existed that failure was every caller's answer:
// MembersOfPGID reported "enumeration failed" for every group, so the
// daemon's session pgid reap refused each time, and the duplicate scanner
// and managed-tree walk saw no processes at all.
//
// PID and PPID come from the same sysctl kern.proc.all snapshot the orphan
// scanner uses (darwinSysctlSource). Cmdline is read per process from
// kern.procargs2, which the kernel refuses for other users' processes; those
// rows fall back to the kernel's short command name and are still returned,
// because callers that only need PIDs (process-group membership) must see
// them. Cwd is left empty: reading it costs one lsof exec per process
// (ReadProcCwd), which a full scan cannot afford. Callers that need a cwd
// ask ReadProcCwd for the specific PID.
func Scan() ([]ProcInfo, error) {
	return scanWith(darwinSysctlSource, ReadProcCmdline)
}

// scanWith is Scan with its two kernel reads injected.
func scanWith(src procSourceFn, cmdlineFn func(int) string) ([]ProcInfo, error) {
	rows, err := src()
	if err != nil {
		return nil, err
	}
	procs := make([]ProcInfo, 0, len(rows))
	for _, r := range rows {
		if r.PID <= 0 {
			continue
		}
		info := ProcInfo{PID: r.PID, PPID: r.PPID, Command: r.Comm, Cmdline: r.Comm}
		if cmdline := cmdlineFn(r.PID); cmdline != "" {
			info.Cmdline = cmdline
			if argv0 := strings.SplitN(cmdline, " ", 2)[0]; argv0 != "" {
				info.Command = filepath.Base(argv0)
			}
		}
		procs = append(procs, info)
	}
	return procs, nil
}
