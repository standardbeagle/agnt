//go:build unix

package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// firstIndexOf returns the index of the first event matching phase/script/dep,
// or -1 if none. dep == "" matches any dependency (for non-dependency phases).
func firstIndexOf(events []AutostartProgress, phase AutostartPhase, script, dep string) int {
	for i, ev := range events {
		if ev.Phase != phase || ev.Script != script {
			continue
		}
		if dep != "" && ev.Dependency != dep {
			continue
		}
		return i
	}
	return -1
}

// hasReadyEvent reports whether a PhaseDependencyReady event exists for the
// given script->dep pair. Its ABSENCE is the load-invariant signal that the
// wait ended by timeout/cancel rather than by the dependency signalling ready.
func hasReadyEvent(events []AutostartProgress, script, dep string) bool {
	return firstIndexOf(events, PhaseDependencyReady, script, dep) >= 0
}

// TestDepsIntegration verifies dependency-wait behaviour end to end: a real
// daemon (its socket reachable via daemonclient) runs autostart over a
// multi-script config and the emitted progress events are asserted for
// dependency ORDERING, not wall-clock duration.
//
// Why ordering, not timing. The previous version drove New()+Start() and
// asserted `elapsed >= 0.8s` / `gap >= 800ms`. Those bounds were satisfied
// purely by monitorStartupFailure blocking each successful start for the full
// StartupMonitorTimeout (3s x 2 scripts x 2 subtests ~= 12s) — NOT by
// dependency sequencing. Proof: the test still PASSED with the dependency wait
// entirely removed. Two coupled defects made it vacuous:
//
//  1. monitorStartupFailure blocks its caller for the whole window on a
//     SUCCESSFUL start, so script starts serialized behind an unrelated timer
//     and any `elapsed >=` bound was trivially met. Fixed here by routing
//     through newDaemon (NewForTest sets a 200ms window; see test_helpers.go)
//     instead of New()+Start().
//  2. setupReadinessSignal (daemon_autostart.go) signals ready IMMEDIATELY for
//     a script with no declared `ports`, winning the readiness race before the
//     dependency has actually done anything — so the gate never gated. Fixed
//     here by declaring `ports` on the depended-on script, which swaps the
//     immediate signal for a port probe; readiness is then driven only by a
//     real event (URL detection for the ready path, or the declared timeout for
//     the timeout path).
//
// With both fixed, the assertions below key off PhaseDependencyWaitStart /
// PhaseDependencyReady, which are emitted ONLY by the dependency-wait code
// path (waitForSingleDependency). Disabling the wait removes those events and
// fails the test — the property the old version could not detect.
func TestDepsIntegration(t *testing.T) {
	clusterDir := t.TempDir()
	sockPath := filepath.Join(clusterDir, "test.sock")
	// newDaemon routes through NewForTest: skips host-global startup ops and,
	// crucially here, sets StartupMonitorTimeout to 200ms so a successful start
	// is not serialized behind a 3s crash-watch window.
	d := newDaemon(t, clusterDir)

	// client_starts_after_server_url: "server" prints a URL after ~1s (URL
	// detection is the readiness signal), then stays alive; "client" depends on
	// "server". Because "server" declares `ports`, its readiness is NOT signalled
	// immediately — the only thing that can release the client's wait is the URL
	// detection. The client must therefore start strictly AFTER the dependency is
	// reported ready.
	t.Run("client_starts_after_server_url", func(t *testing.T) {
		dir := t.TempDir()
		serverPort := ephemeralPort(t)
		writeConfig(t, dir, fmt.Sprintf(`
scripts {
    server {
        run "sleep 1 && echo Listening on http://localhost:%d && sleep 60"
        autostart true
        ports %d
    }
    client {
        run "echo client-started && sleep 60"
        autostart true
        depends-on "server" timeout=10
    }
}
`, serverPort, serverPort))

		client := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
		require.NoError(t, client.Connect())
		defer client.Close()

		progress := make(chan AutostartProgress, 200)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		start := time.Now()
		result := d.RunAutostartAsync(ctx, dir, progress)
		elapsed := time.Since(start)
		close(progress)
		events := drainProgress(progress)

		t.Logf("RunAutostart completed in %v", elapsed)
		t.Logf("RunAutostart result: scripts=%v errors=%v", result.Scripts, result.Errors)

		require.NotNil(t, result, "result must not be nil")
		assert.Len(t, result.Scripts, 2, "expected 2 scripts started")
		assert.Empty(t, result.Errors, "expected no errors")

		// Primary invariant: dependency ORDERING via progress events.
		//
		// waitForSingleDependency emits PhaseDependencyWaitStart before the wait
		// and PhaseDependencyReady ONLY when the dependency signals ready (URL
		// detected). startOneScript emits the client's PhaseScriptStarted AFTER
		// the wait returns. So a correct gate produces, in order:
		//     server started -> client dep-wait start -> client dep ready -> client started
		serverStarted := firstIndexOf(events, PhaseScriptStarted, "server", "")
		depWaitStart := firstIndexOf(events, PhaseDependencyWaitStart, "client", "server")
		depReady := firstIndexOf(events, PhaseDependencyReady, "client", "server")
		clientStarted := firstIndexOf(events, PhaseScriptStarted, "client", "")

		// These four requires are what give the test teeth: with the dependency
		// wait disabled, PhaseDependencyWaitStart and PhaseDependencyReady are
		// never emitted, so depWaitStart/depReady are -1 and the test FAILS.
		require.GreaterOrEqual(t, serverStarted, 0, "missing PhaseScriptStarted for server")
		require.GreaterOrEqual(t, depWaitStart, 0,
			"missing PhaseDependencyWaitStart for client->server — dependency wait did not run")
		require.GreaterOrEqual(t, depReady, 0,
			"missing PhaseDependencyReady for client->server — dependency never signalled ready "+
				"(the client's wait was released by something other than the server's URL detection)")
		require.GreaterOrEqual(t, clientStarted, 0, "missing PhaseScriptStarted for client")

		// The gap the old test asserted in wall-clock is asserted here as event
		// sequence, and it is attributable to the dependency wait alone:
		// PhaseDependencyReady is emitted by waitForSingleDependency, never by
		// monitorStartupFailure.
		assert.Less(t, serverStarted, depWaitStart,
			"server (layer 0) must start before the client (layer 1) begins its dependency wait")
		assert.Less(t, depWaitStart, depReady,
			"the client must observe its dependency become ready while it is waiting")
		assert.Less(t, depReady, clientStarted,
			"the client must start only AFTER its dependency is reported ready")

		// Generous liveness ceiling, not a latency budget: catches a regression
		// that reinstates a multi-second monitor block on every start, which is
		// far from this value. The 200ms monitor window + ~1s URL delay lands
		// well under a second-and-change in practice.
		assert.Less(t, elapsed.Seconds(), 10.0,
			"autostart should complete promptly once semantics are correct, took %v", elapsed)

		// Integration check: both processes are actually registered and running,
		// observable over the daemon socket (not just via in-process events).
		serverID := makeProcessID(dir, "server")
		clientID := makeProcessID(dir, "client")
		waitForProcessState(t, client, serverID, "running", 3*time.Second)
		waitForProcessState(t, client, clientID, "running", 3*time.Second)

		_, _ = client.ProcStop(serverID, false)
		_, _ = client.ProcStop(clientID, false)
	})

	// timeout_dependency_never_ready: "server" declares `ports` nothing binds and
	// only sleeps, so it never signals ready — neither immediately (ports swap the
	// immediate signal for a port probe) nor via URL detection (no URL printed).
	// The client's wait can therefore end ONLY via its declared timeout=1, and it
	// must start anyway (timeout is a warning, not a hard error). The distinguishing
	// assertion is the ABSENCE of PhaseDependencyReady: the wait ran and ended on
	// the timeout path, attributable to the dependency wait rather than to any
	// startup monitor.
	t.Run("timeout_dependency_never_ready", func(t *testing.T) {
		dir := t.TempDir()
		serverPort := ephemeralPort(t)
		writeConfig(t, dir, fmt.Sprintf(`
scripts {
    server {
        run "sleep 60"
        autostart true
        ports %d
    }
    client {
        run "echo client-started && sleep 60"
        autostart true
        depends-on "server" timeout=1
    }
}
`, serverPort))

		client := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
		require.NoError(t, client.Connect())
		defer client.Close()

		progress := make(chan AutostartProgress, 200)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		start := time.Now()
		result := d.RunAutostartAsync(ctx, dir, progress)
		elapsed := time.Since(start)
		close(progress)
		events := drainProgress(progress)

		t.Logf("RunAutostart completed in %v", elapsed)
		t.Logf("RunAutostart result: scripts=%v errors=%v", result.Scripts, result.Errors)

		require.NotNil(t, result, "result must not be nil")
		assert.Len(t, result.Scripts, 2, "expected 2 scripts started")
		assert.Empty(t, result.Errors, "expected no hard errors (timeout is a warning)")

		// Primary invariant: the dependency wait ran (teeth: gone when the wait is
		// disabled) and ended on the TIMEOUT path (no ready event).
		depWaitStart := firstIndexOf(events, PhaseDependencyWaitStart, "client", "server")
		require.GreaterOrEqual(t, depWaitStart, 0,
			"missing PhaseDependencyWaitStart for client->server — dependency wait did not run")
		assert.False(t, hasReadyEvent(events, "client", "server"),
			"the server never binds its port and prints no URL, so it must never be reported "+
				"ready — the wait must end on the declared timeout, not via a readiness signal")

		// The client starts despite the unmet dependency (timeout is a warning).
		clientStarted := firstIndexOf(events, PhaseScriptStarted, "client", "")
		assert.GreaterOrEqual(t, clientStarted, 0,
			"client must start after the dependency timeout expires")

		// Load-invariant lower bound (a timer cannot fire EARLY regardless of host
		// load): if the wait returns in well under the declared timeout=1, the dep
		// was released by something other than the timeout — the exact vacuity this
		// redesign closes. Not the primary invariant, but a real one. 1s is the
		// config floor: depends-on timeout=N truncates to whole seconds, so
		// sub-second is not expressible.
		assert.GreaterOrEqual(t, elapsed.Seconds(), 0.9,
			"the declared timeout=1 must bound the wait; returning early means the dep was "+
				"released by something else, took %v", elapsed)
		// Generous liveness ceiling: catches a regression to the old 120s implicit
		// fallback, 60x away from this value.
		assert.Less(t, elapsed.Seconds(), 10.0,
			"expected RunAutostart to complete well before the 60s sleep finishes, took %v", elapsed)

		serverID := makeProcessID(dir, "server")
		clientID := makeProcessID(dir, "client")
		waitForProcessState(t, client, serverID, "running", 3*time.Second)
		waitForProcessState(t, client, clientID, "running", 3*time.Second)

		_, _ = client.ProcStop(serverID, false)
		_, _ = client.ProcStop(clientID, false)
	})
}
