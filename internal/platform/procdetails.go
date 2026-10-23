package platform

import "time"

// ProcDetails is what an operator needs to recognise a process before it is
// killed: who started it, from where, how long ago, and what it is costing.
// Fields the host cannot report are left zero.
type ProcDetails struct {
	PID        int
	PPID       int
	Command    string
	Cmdline    string
	Cwd        string
	StartTime  time.Time     // zero when unknown
	Uptime     time.Duration // zero when unknown
	RSSBytes   uint64        // resident memory; zero when unknown
	CPUSeconds float64       // cumulative user+system CPU time; zero when unknown
	Threads    int           // zero when unknown
	User       string        // owning uid (numeric); empty when unknown
}

// DescribePID gathers ProcDetails for pid. ok is false when the process is
// gone or unreadable; a partially filled value is still returned with ok true
// when only some fields are available.
func DescribePID(pid int) (ProcDetails, bool) {
	return describePID(pid)
}
