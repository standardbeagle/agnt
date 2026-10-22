package incident

import "testing"

func TestBusCountsEventsWithNoLivePipeline(t *testing.T) {
	b := &MPSCBus{perSession: map[string]*sessionPipeline{}}
	b.deliver(&IncidentEvent{Source: "proc", Ctx: Context{SessionID: "gone"}})
	b.deliver(&IncidentEvent{Source: "proc"})
	if got := b.Unrouted(); got != 2 {
		t.Fatalf("Unrouted() = %d, want 2", got)
	}
}
