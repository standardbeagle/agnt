package platform

// ProcInfo describes a running process discovered via OS-level scanning.
type ProcInfo struct {
	PID     int
	Command string // basename of the executable
	Cmdline string // full command line
	Cwd     string // working directory (may be empty on some platforms)
}

// KillPID sends SIGTERM to the process group for pid, waits up to
// gracefulTimeout seconds, then escalates to SIGKILL. On Windows it uses
// TerminateProcess.
func KillPID(pid int, gracefulTimeout int) error {
	return killPID(pid, gracefulTimeout)
}
