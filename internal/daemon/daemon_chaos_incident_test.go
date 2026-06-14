package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

// chaosHTTPEntry builds a proxied HTTP log entry. chaos marks it synthetic
// (suppressed from the inbox); watch additionally arms the swallow heuristic
// (set at injection only when the proxy panel toggle is on).
func chaosHTTPEntry(status int, chaos, watch bool) proxy.LogEntry {
	return proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:            "GET",
			URL:               "http://app/api/x",
			StatusCode:        status,
			Chaos:             chaos,
			ChaosSwallowWatch: watch,
		},
	}
}

// TestChaosInjectedFault_SuppressedFromIncidentInbox pins the marker fix: a
// real backend 500 reaches the incident inbox, but a chaos-injected 500 (same
// status) is kept out — it is synthetic, not a bug to chase.
func TestChaosInjectedFault_SuppressedFromIncidentInbox(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	require.NotNil(t, d.incidentBus)
	d.addIncidentSession("sess-chaos")

	// Control: a genuine backend 500 surfaces.
	d.fireToIncidentBus(chaosHTTPEntry(500, false, false), "px-real")
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("sess-chaos", incident.QueryFilter{})
		for _, e := range entries {
			if e.Sample != nil && e.Sample.Source == incident.SourceHTTP5xx {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "real backend 500 should reach the inbox")

	// Chaos-injected 500 must NOT add any chaos-origin HTTP incident.
	d.fireToIncidentBus(chaosHTTPEntry(500, true, false), "px-chaos")
	// Give the async bus a moment, then assert the injected proxy never shows.
	time.Sleep(200 * time.Millisecond)
	entries, _ := d.incidentBus.QuerySession("sess-chaos", incident.QueryFilter{})
	for _, e := range entries {
		if e.Sample != nil && e.Sample.Ctx.ProxyID == "px-chaos" {
			t.Fatalf("chaos-injected fault leaked into incident inbox: %+v", e.Sample)
		}
	}
}

// TestSwallowHeuristic_RaisesIncidentWhenAppEatsFault drives the full enabled
// path: ApplyChaosConfig turns the detector on, an injected fault with no
// following app error is swept into a swallowed-error incident on the bus.
func TestSwallowHeuristic_RaisesIncidentWhenAppEatsFault(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	d.addIncidentSession("sess-swallow")
	// Use a short window so the sweeper raises the incident quickly.
	d.swallowDetector = incident.NewSwallowDetector(200 * time.Millisecond)

	// Watched injected fault, no app error follows → swallowed.
	d.fireToIncidentBus(chaosHTTPEntry(500, true, true), "px1")

	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("sess-swallow", incident.QueryFilter{})
		for _, e := range entries {
			if e.Sample != nil && e.Sample.Source == incident.SourceChaosSwallow {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "swallowed fault should raise a chaos_swallowed_error incident")
}

// TestSwallowHeuristic_AppErrorSuppressesIncident confirms the inverse: when an
// app-side error follows the injected fault, no swallowed-error incident fires.
func TestSwallowHeuristic_AppErrorSuppressesIncident(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	d.addIncidentSession("sess-handled")
	d.swallowDetector = incident.NewSwallowDetector(200 * time.Millisecond)

	d.fireToIncidentBus(chaosHTTPEntry(500, true, true), "px1")
	// App reports a JS error promptly → handled.
	d.fireToIncidentBus(proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "fetch failed", URL: "http://app"},
	}, "px1")

	// Wait past the window + a sweep tick; assert no swallowed incident.
	time.Sleep(900 * time.Millisecond)
	entries, _ := d.incidentBus.QuerySession("sess-handled", incident.QueryFilter{})
	for _, e := range entries {
		if e.Sample != nil && e.Sample.Source == incident.SourceChaosSwallow {
			t.Fatalf("handled fault wrongly raised a swallowed-error incident")
		}
	}
}
