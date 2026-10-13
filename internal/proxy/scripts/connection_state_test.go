package scripts

import (
	"strings"
	"testing"
)

// countOccurrences is a small readability helper for the source-shape
// assertions below.
func countOccurrences(haystack, needle string) int {
	return strings.Count(haystack, needle)
}

// TestConnectionStateNotifiedOnEveryEdge guards the defect that made the
// indicator status dot render grey on a live connection: core.js used to
// install two WebSocket wirings (a plain `connect` and a diagnostics variant
// that replaced it at init), and only the dead one fanned out the connected
// edge. There is now exactly one wiring, so there is exactly one place for
// that to go wrong.
//
// Every open/close/error handler must call notifyConnectionState().
func TestConnectionStateNotifiedOnEveryEdge(t *testing.T) {
	if !strings.Contains(coreJS, "function notifyConnectionState()") {
		t.Fatal("core.js must define notifyConnectionState()")
	}

	if got := countOccurrences(coreJS, "function connect()"); got != 1 {
		t.Fatalf("core.js defines connect() %d times, want exactly 1 wiring", got)
	}
	if strings.Contains(coreJS, "connectWithDiagnostics") {
		t.Error("core.js must not reintroduce a second WebSocket wiring (connectWithDiagnostics)")
	}

	// One wiring x three edges (open, close, error) = three notify sites.
	const wantNotifies = 3
	if got := countOccurrences(coreJS, "notifyConnectionState();"); got != wantNotifies {
		t.Errorf("notifyConnectionState() called at %d sites, want %d (open/close/error in connect())", got, wantNotifies)
	}
}

// TestReconnectHasNoAttemptLimit is the always-on source guard for the
// reconnect contract; the behaviour (backoff schedule, stale-socket close
// ignored, online/visibility re-arm) is proven in the JS tier
// (jstest/core-transport.test.js).
func TestReconnectHasNoAttemptLimit(t *testing.T) {
	if strings.Contains(coreJS, "MAX_RECONNECT_ATTEMPTS") {
		t.Error("core.js must not cap reconnect attempts: an outage longer than the budget strands the page until reload")
	}
	for _, want := range []string{
		"RECONNECT_MAX_MS",
		"if (sock !== ws) return;",
		"addEventListener('online'",
		"addEventListener('visibilitychange'",
	} {
		if !strings.Contains(coreJS, want) {
			t.Errorf("core.js missing %q", want)
		}
	}
}

// TestTransportDiagnosticsHasItsOwnGlobal pins the split of the two
// diagnostics APIs: core.js (transport history) must not export onto the
// global diagnostics.js (CSS visual debugging) owns, or one overwrites the
// other wherever both modules load.
func TestTransportDiagnosticsHasItsOwnGlobal(t *testing.T) {
	if strings.Contains(coreJS, "window.__devtool_diagnostics = ") || strings.Contains(coreJS, "!window.__devtool_diagnostics") {
		t.Error("core.js must export transport diagnostics as window.__devtool_transport_diag, not window.__devtool_diagnostics")
	}
	if !strings.Contains(coreJS, "window.__devtool_transport_diag = {") {
		t.Error("core.js must export window.__devtool_transport_diag")
	}
	if !strings.Contains(diagnosticsJS, "window.__devtool_diagnostics = {") {
		t.Error("diagnostics.js is expected to own window.__devtool_diagnostics")
	}
}

// TestOnConnectedReplaysCurrentState guards the other half of the grey-dot
// race: core.js connects during its own init, so a subscriber that registers
// later (the indicator) misses the open edge entirely unless onConnected
// replays the current state synchronously at registration.
func TestOnConnectedReplaysCurrentState(t *testing.T) {
	idx := strings.Index(coreJS, "function onConnected(cb)")
	if idx < 0 {
		t.Fatal("onConnected not found in core.js")
	}
	body := coreJS[idx : idx+400]
	if !strings.Contains(body, "connectedCallbacks.push(cb)") {
		t.Fatal("onConnected must register the callback")
	}
	pushIdx := strings.Index(body, "connectedCallbacks.push(cb)")
	replayIdx := strings.Index(body, "cb(st === 'connected', st)")
	if replayIdx < 0 {
		t.Fatal("onConnected must replay the current connection state at registration")
	}
	if replayIdx < pushIdx {
		t.Error("onConnected must register before replaying so an edge during replay is not lost")
	}
}

// TestConnectionStatesHaveDistinctIntensity pins the accessibility contract for
// the status dot: hue alone is not a sufficient signal, so each of the three
// states must also differ in fill intensity (opacity band) and in shape
// (solid disc vs ring vs hollow ring, expressed through inset shadows).
func TestConnectionStatesHaveDistinctIntensity(t *testing.T) {
	idx := strings.Index(indicatorJS, "var CONN_STATES = {")
	if idx < 0 {
		t.Fatal("CONN_STATES not found in indicator.js")
	}
	end := strings.Index(indicatorJS[idx:], "\n  };")
	if end < 0 {
		t.Fatal("CONN_STATES block not terminated as expected")
	}
	block := indicatorJS[idx : idx+end]

	for _, state := range []string{"connected:", "connecting:", "disconnected:"} {
		if !strings.Contains(block, state) {
			t.Errorf("CONN_STATES missing %s", state)
		}
	}

	// Distinct opacity per state — the intensity ladder.
	opacities := map[string]bool{}
	for _, chunk := range strings.Split(block, "opacity: '")[1:] {
		if i := strings.Index(chunk, "'"); i > 0 {
			opacities[chunk[:i]] = true
		}
	}
	if len(opacities) != 3 {
		t.Errorf("expected 3 distinct opacity bands across connection states, got %d (%v)", len(opacities), opacities)
	}

	// Shape differentiation: only the non-connected states use inset rings,
	// so the dot is distinguishable without perceiving hue at all.
	if strings.Count(block, "inset 0 0 0") < 4 {
		t.Error("connecting/disconnected states must use inset ring shadows so they differ in shape, not just hue")
	}

	// Reduced motion must not collapse the states into one appearance.
	if !strings.Contains(indicatorJS, "@media (prefers-reduced-motion: reduce) {") {
		t.Error("indicator.js must pin static per-state intensity under prefers-reduced-motion")
	}
	if !strings.Contains(indicatorJS, "__devtool-dot-retry-pulse") {
		t.Error("reconnecting state must have its own pulse, distinct from the disconnected blink")
	}
}
