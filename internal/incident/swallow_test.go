package incident

import (
	"testing"
	"time"
)

// base is a fixed clock origin; tests advance from it deterministically.
var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestSwallowDetector_FaultWithNoAppErrorIsSwallowed(t *testing.T) {
	d := NewSwallowDetector(time.Second)
	d.RecordFault("px1", "http://app/api/x", 500, base)

	// Before the window elapses, nothing is swallowed yet.
	if evs := d.Sweep(base.Add(900 * time.Millisecond)); len(evs) != 0 {
		t.Fatalf("premature sweep emitted %d incidents, want 0", len(evs))
	}

	// After the window, the unhandled fault becomes one incident.
	evs := d.Sweep(base.Add(1100 * time.Millisecond))
	if len(evs) != 1 {
		t.Fatalf("expected 1 swallowed incident, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Source != SourceChaosSwallow || ev.Severity != SeverityWarning || ev.Ctx.ProxyID != "px1" {
		t.Fatalf("incident wrong: source=%s sev=%s proxy=%s", ev.Source, ev.Severity, ev.Ctx.ProxyID)
	}

	// A swept fault is consumed — a second sweep yields nothing.
	if evs := d.Sweep(base.Add(2 * time.Second)); len(evs) != 0 {
		t.Fatalf("fault re-emitted after sweep: %d", len(evs))
	}
}

func TestSwallowDetector_AppErrorClearsFault(t *testing.T) {
	d := NewSwallowDetector(time.Second)
	d.RecordFault("px1", "http://app/api/x", 500, base)
	// App surfaced an error 300ms later — within the window → handled.
	d.RecordAppError("px1", base.Add(300*time.Millisecond))

	if evs := d.Sweep(base.Add(2 * time.Second)); len(evs) != 0 {
		t.Fatalf("handled fault still swallowed: %d incidents", len(evs))
	}
}

func TestSwallowDetector_AppErrorOnDifferentProxyDoesNotClear(t *testing.T) {
	d := NewSwallowDetector(time.Second)
	d.RecordFault("px1", "http://app/api/x", 500, base)
	// Error on a different proxy must not absolve px1.
	d.RecordAppError("px2", base.Add(200*time.Millisecond))

	if evs := d.Sweep(base.Add(1100 * time.Millisecond)); len(evs) != 1 {
		t.Fatalf("cross-proxy clear leaked: got %d incidents, want 1", len(evs))
	}
}

func TestSwallowDetector_LateAppErrorDoesNotClear(t *testing.T) {
	d := NewSwallowDetector(time.Second)
	d.RecordFault("px1", "http://app/api/x", 500, base)
	// App error arrives after the window — too late to count as handling.
	d.RecordAppError("px1", base.Add(1500*time.Millisecond))

	if evs := d.Sweep(base.Add(1600 * time.Millisecond)); len(evs) != 1 {
		t.Fatalf("late app error wrongly cleared fault: got %d, want 1", len(evs))
	}
}

func TestSwallowDetector_StormCollapsesToOneFingerprint(t *testing.T) {
	d := NewSwallowDetector(time.Second)
	for i := 0; i < 5; i++ {
		d.RecordFault("px1", "http://app/api/x", 500, base.Add(time.Duration(i)*time.Millisecond))
	}
	evs := d.Sweep(base.Add(2 * time.Second))
	if len(evs) != 5 {
		t.Fatalf("expected 5 raw incidents, got %d", len(evs))
	}
	// All share one storm fingerprint so the inbox dedupes them downstream.
	fp := evs[0].Fingerprint
	for _, ev := range evs {
		if ev.Fingerprint != fp {
			t.Fatalf("fingerprints diverged: %q vs %q", ev.Fingerprint, fp)
		}
	}
}

func TestSwallowDetector_NilSafe(t *testing.T) {
	var d *SwallowDetector
	d.RecordFault("px", "u", 500, base)   // must not panic
	d.RecordAppError("px", base)          // must not panic
	if evs := d.Sweep(base); evs != nil { // nil receiver → nil result
		t.Fatalf("nil detector swept %d", len(evs))
	}
}
