package incident

// Bus is the central sink for all incident events. L8 (stream wiring) wires
// this to the real fan-out implementation. Until then, callers may pass a
// NopBus to satisfy the interface without side effects.
type Bus interface {
	Publish(IncidentEvent)
}

// NopBus discards every event published to it.
type NopBus struct{}

func (NopBus) Publish(IncidentEvent) {}
