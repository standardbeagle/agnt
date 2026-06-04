package overlay

// Action represents an action triggered from the overlay and dispatched to the
// host process via the OnAction callback.
type Action int

const (
	// ActionNone is the zero value — no action.
	ActionNone Action = iota
	// ActionRefreshStatus asks the host to refresh daemon-backed status.
	ActionRefreshStatus
)
