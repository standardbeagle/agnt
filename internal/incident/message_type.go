package incident

// MessageType partitions the agent-inbound queue into per-type lanes. The error
// lane is severity-banded; drawing/comment lanes are FIFO. New types are added
// here plus a lane config — the gate/digest machinery is type-agnostic.
type MessageType string

const (
	// MessageError is the lane for diagnostics, HTTP errors, crashes, etc.
	MessageError MessageType = "error"
	// MessageDrawing is the lane for sketch-mode wireframes.
	MessageDrawing MessageType = "drawing"
	// MessageComment is the lane for floating-panel user messages.
	MessageComment MessageType = "comment"
)
