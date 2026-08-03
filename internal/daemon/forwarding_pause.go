package daemon

// SetForwardingPaused records whether a session has paused agent-inbound push
// delivery. Paused sessions still accumulate incidents in their inbox (pullable
// via get_incidents); only the push sinks are gated. Idempotent.
func (d *Daemon) SetForwardingPaused(sessionCode string, paused bool) {
	if sessionCode == "" {
		return
	}
	if paused {
		d.forwardingPaused.Store(sessionCode, true)
	} else {
		d.forwardingPaused.Delete(sessionCode)
	}
}

// IsForwardingPaused reports whether the given session has paused push delivery.
func (d *Daemon) IsForwardingPaused(sessionCode string) bool {
	if sessionCode == "" {
		return false
	}
	v, ok := d.forwardingPaused.Load(sessionCode)
	return ok && v == true
}
