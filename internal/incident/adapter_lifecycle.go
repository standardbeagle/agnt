package incident

// FromShutdown creates an IncidentEvent signalling daemon/session shutdown.
func FromShutdown(reason string) IncidentEvent {
	if reason == "" {
		reason = "daemon shutdown"
	}
	return NewIncidentEvent(
		SourceShutdown,
		SeverityInfo,
		"shutdown",
		reason,
		Context{},
		nil,
	)
}
