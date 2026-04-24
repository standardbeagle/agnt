package incident

import "fmt"

// FromProcessExit converts a process death record into an IncidentEvent.
// Uses plain primitive params so the incident package stays free of daemon
// imports (avoiding a cycle when daemon wires the bus in L8).
//
// reason is "stopped" | "crash" | "signal". exitCode is the OS exit code.
// stderrTail is the last N lines of stderr captured on exit (may be empty).
func FromProcessExit(processID, reason string, exitCode int, stderrTail string) IncidentEvent {
	msg := fmt.Sprintf("process exited: code=%d reason=%s", exitCode, reason)
	if stderrTail != "" {
		msg += "\n" + stderrTail
	}
	return NewIncidentEvent(
		SourceProcessCrash,
		SeverityError,
		"process_exit",
		msg,
		Context{ProcessID: processID},
		nil,
	)
}
