//go:build !linux

package platform

// describePID reports what the process scan already knows (command line and
// cwd where the platform provides them). Uptime, memory and CPU come from
// /proc on Linux only; elsewhere those fields stay zero.
func describePID(pid int) (ProcDetails, bool) {
	procs, err := Scan()
	if err != nil {
		return ProcDetails{}, false
	}
	for _, p := range procs {
		if p.PID == pid {
			return ProcDetails{PID: p.PID, PPID: p.PPID, Command: p.Command, Cmdline: p.Cmdline, Cwd: p.Cwd}, true
		}
	}
	return ProcDetails{}, false
}
