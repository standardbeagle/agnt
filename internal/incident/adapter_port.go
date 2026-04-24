package incident

import "fmt"

// FromPortConflict converts a port-conflict detection record into an
// IncidentEvent. Uses plain primitives to avoid importing the daemon package.
//
// port is the blocked port number. pid is the blocking process's OS PID.
// processName is a human-readable label for the blocking process (may be empty).
func FromPortConflict(port, pid int, processName string) IncidentEvent {
	var msg string
	if processName != "" {
		msg = fmt.Sprintf("port %d blocked by PID %d (%s)", port, pid, processName)
	} else {
		msg = fmt.Sprintf("port %d blocked by PID %d", port, pid)
	}
	return NewIncidentEvent(
		SourcePortConflict,
		SeverityWarning,
		"port_conflict",
		msg,
		Context{PID: pid},
		nil,
	)
}
