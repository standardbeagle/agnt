package incident

// FromHookStopFailure creates an IncidentEvent for a Claude Code StopFailure
// hook event (the agent turn ended due to an API error). Uses plain primitives
// to avoid importing the daemon package.
//
// sessionID is the Claude Code session that failed.
// errMsg is the short error label. errDetails carries the full error body (may be empty).
func FromHookStopFailure(sessionID, errMsg, errDetails string) IncidentEvent {
	msg := errMsg
	if errDetails != "" {
		msg += "\n" + errDetails
	}
	return NewIncidentEvent(
		SourceHookStopFail,
		SeverityError,
		"stop_failure",
		msg,
		Context{SessionID: sessionID},
		nil,
	)
}
