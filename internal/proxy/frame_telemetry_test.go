package proxy

import "testing"

// TestLogEntry_FrameIDStamped: the browser-telemetry Log* methods stamp the
// envelope FrameID from their optional variadic, and leave it empty when none
// is supplied (legacy/server callers).
func TestLogEntry_FrameIDStamped(t *testing.T) {
	tl := NewTrafficLogger(50)

	tl.LogError(FrontendError{ID: "e1", Message: "boom"}, "frameA")
	tl.LogError(FrontendError{ID: "e2", Message: "boom"}) // no frame id
	tl.LogInteraction(InteractionEvent{ID: "i1", EventType: "click"}, "frameB")
	tl.LogMutation(MutationEvent{ID: "m1"}, "frameA")
	tl.LogPerformance(PerformanceMetric{ID: "p1"}, "frameA")
	tl.LogCustom(CustomLog{ID: "c1", Message: "x"}, "frameB")

	all := tl.Query(LogFilter{})
	byID := map[string]LogEntry{}
	for _, e := range all {
		switch {
		case e.Error != nil:
			byID[e.Error.ID] = e
		case e.Interaction != nil:
			byID[e.Interaction.ID] = e
		case e.Mutation != nil:
			byID[e.Mutation.ID] = e
		case e.Performance != nil:
			byID[e.Performance.ID] = e
		case e.Custom != nil:
			byID[e.Custom.ID] = e
		}
	}

	checks := map[string]string{"e1": "frameA", "e2": "", "i1": "frameB", "m1": "frameA", "p1": "frameA", "c1": "frameB"}
	for id, want := range checks {
		e, ok := byID[id]
		if !ok {
			t.Fatalf("entry %q not logged", id)
		}
		if e.FrameID != want {
			t.Errorf("entry %q FrameID = %q, want %q", id, e.FrameID, want)
		}
	}
}

// TestLogFilter_Frames: the Frames filter selects only entries from the named
// content frames.
func TestLogFilter_Frames(t *testing.T) {
	tl := NewTrafficLogger(50)
	tl.LogError(FrontendError{ID: "a", Message: "x"}, "frameA")
	tl.LogError(FrontendError{ID: "b", Message: "x"}, "frameB")
	tl.LogError(FrontendError{ID: "c", Message: "x"}) // unframed

	onlyA := tl.Query(LogFilter{Frames: []string{"frameA"}})
	if len(onlyA) != 1 || onlyA[0].Error.ID != "a" {
		t.Fatalf("Frames=[frameA] must select exactly entry a, got %d", len(onlyA))
	}

	ab := tl.Query(LogFilter{Frames: []string{"frameA", "frameB"}})
	if len(ab) != 2 {
		t.Errorf("Frames=[frameA,frameB] must select 2 entries, got %d", len(ab))
	}

	// No frame filter returns everything (including unframed).
	if all := tl.Query(LogFilter{}); len(all) != 3 {
		t.Errorf("no frame filter must return all 3, got %d", len(all))
	}
}
