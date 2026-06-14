package incident

import (
	"fmt"
	"sync"
	"time"
)

// SwallowDetector implements the chaos "swallowed error" heuristic: when the
// chaos engine injects an error-status fault into a proxied response and the
// application produces NO app-side error signal (a frontend JS error on the
// same proxy) within a window, the fault was silently eaten — a real defect in
// the app's error handling that the chaos run exists to surface.
//
// The detector is a pure correlation state machine driven by three calls:
//
//   - RecordFault   — an injected fault was logged (pending until proven handled)
//   - RecordAppError — an app-side error arrived; clears recent pending faults
//     on that proxy (the app surfaced something, so it did not swallow)
//   - Sweep         — pending faults older than the window with no matching app
//     error become swallowed-error incidents
//
// Time is passed in explicitly (no internal clock) so the daemon can drive it
// with a ticker and tests can drive it deterministically.
//
// Correlation is per-proxy and time-windowed, not per-URL: a frontend JS error
// rarely names the request that triggered it, so any app error within the
// window after an injected fault counts as "handled". This is a heuristic by
// construction — it trades precision for catching the silent-failure case that
// nothing else can observe.
type SwallowDetector struct {
	window time.Duration

	mu      sync.Mutex
	pending []pendingFault
}

type pendingFault struct {
	proxyID string
	url     string
	status  int
	at      time.Time
}

// NewSwallowDetector returns a detector with the given correlation window. A
// non-positive window falls back to two seconds.
func NewSwallowDetector(window time.Duration) *SwallowDetector {
	if window <= 0 {
		window = 2 * time.Second
	}
	return &SwallowDetector{window: window}
}

// RecordFault registers an injected error-status fault as pending. It becomes a
// swallowed-error incident on a later Sweep unless RecordAppError clears it
// first.
func (d *SwallowDetector) RecordFault(proxyID, url string, status int, now time.Time) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.pending = append(d.pending, pendingFault{proxyID: proxyID, url: url, status: status, at: now})
	d.mu.Unlock()
}

// RecordAppError records that the app produced an error on proxyID. It clears
// every pending fault on that proxy whose injection time is within the window
// ending at now — the app surfaced an error in response, so those faults were
// handled, not swallowed.
func (d *SwallowDetector) RecordAppError(proxyID string, now time.Time) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	kept := d.pending[:0]
	for _, p := range d.pending {
		// Clear a pending fault on this proxy if the app error arrived within
		// the window after it was injected. Faults on other proxies, or older
		// than the window, are left for Sweep to resolve.
		if p.proxyID == proxyID && !now.Before(p.at) && now.Sub(p.at) <= d.window {
			continue
		}
		kept = append(kept, p)
	}
	d.pending = kept
}

// Sweep returns swallowed-error incidents for every pending fault older than
// the window (no app error cleared it in time) and drops them from the pending
// set. Faults still inside the window are retained for a future sweep.
func (d *SwallowDetector) Sweep(now time.Time) []IncidentEvent {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []IncidentEvent
	kept := d.pending[:0]
	for _, p := range d.pending {
		if now.Sub(p.at) >= d.window {
			out = append(out, swallowedIncident(p, d.window))
			continue
		}
		kept = append(kept, p)
	}
	d.pending = kept
	return out
}

// swallowedIncident builds the incident raised for a fault the app swallowed.
func swallowedIncident(p pendingFault, window time.Duration) IncidentEvent {
	msg := fmt.Sprintf(
		"chaos injected %d on %s but the app produced no error within %s — possible swallowed error / missing error handling",
		p.status, p.url, window,
	)
	ev := NewIncidentEvent(
		SourceChaosSwallow, SeverityWarning, "chaos",
		msg,
		Context{ProxyID: p.proxyID, URL: p.url},
		nil,
	)
	// Collapse repeated swallows on the same proxy into one fingerprint so a
	// burst of injected faults does not flood the inbox.
	ev.Fingerprint = computeStormFingerprint(string(SourceChaosSwallow), "swallowed", p.proxyID)
	return ev
}
