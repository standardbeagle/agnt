//go:build unix

package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// TestHubHandlePortsCleanOrphans_ScopeGatesForeignPGID is the handler-level
// regression for the review-advisory follow-up (01KXM3MDM4CJMAKKMTW0R29ZM8):
// hubHandlePortsCleanOrphans must resolve the caller's session project scope
// (resolveProjectScope) and pass that EXACT project path into the ownership
// gate (pgidOwnershipCheck, via knownProjectPaths) BEFORE reaping any
// candidate pgid — a shared uid alone must never be sufficient evidence.
//
// The real /proc scan, ancestor-chain walk, and pgid signal are swapped for
// injected fakes (scanOrphanedPGIDsFn / walkParentsFn / killSessionPGIDFn,
// mirroring the portProbe seam in port_preflight.go) so the test drives the
// actual PORTS CLEAN-ORPHANS command handler over the real daemon socket —
// exercising resolveProjectScope and reapOrphans exactly as production does
// — without a genuine dead-leader process tree or a real kill(2).
func TestHubHandlePortsCleanOrphans_ScopeGatesForeignPGID(t *testing.T) {
	// No t.Parallel(): overrides package-level fake-injection vars shared
	// across the daemon package.
	_, sockPath := newBootedDaemon(t)

	ownedProject := normalizePath(t.TempDir())
	foreignProject := normalizePath(t.TempDir()) // never registered with any session

	const ownedPGID = 900001
	const foreignPGID = 900002

	origScan, origWalk, origKill := scanOrphanedPGIDsFn, walkParentsFn, killSessionPGIDFn
	t.Cleanup(func() {
		scanOrphanedPGIDsFn, walkParentsFn, killSessionPGIDFn = origScan, origWalk, origKill
	})

	scanOrphanedPGIDsFn = func(_ int, _ map[int]bool) []platform.OrphanPGID {
		return []platform.OrphanPGID{
			{PGID: ownedPGID, Members: []int{ownedPGID}},
			{PGID: foreignPGID, Members: []int{foreignPGID}},
		}
	}
	walkParentsFn = func(pid int) []platform.AncestorInfo {
		switch pid {
		case ownedPGID:
			// Both cmdline and cwd evidence match the resolved project.
			return []platform.AncestorInfo{{PID: pid, Cmdline: "agnt run child", Cwd: ownedProject}}
		case foreignPGID:
			// cmdline looks like agnt, but the cwd is NOT inside any
			// project this daemon currently knows about — must be rejected.
			return []platform.AncestorInfo{{PID: pid, Cmdline: "agnt run child", Cwd: foreignProject}}
		default:
			return nil
		}
	}

	killedPGIDs := make(map[int]bool)
	killSessionPGIDFn = func(pgid int, _ int, _ time.Duration, _ bool) error {
		killedPGIDs[pgid] = true
		return nil
	}

	client := registerSessionClient(t, sockPath, "sess-owned", ownedProject)

	raw, err := client.Conn().Request(protocol.VerbPorts, protocol.SubVerbCleanOrphans).
		WithJSON(protocol.DirectoryFilter{}).JSON()
	require.NoError(t, err)

	reapedPIDs := toIntSlice(t, raw["reaped_pgids"])

	require.Contains(t, reapedPIDs, ownedPGID, "candidate matching the resolved project scope must be reaped")
	require.NotContains(t, reapedPIDs, foreignPGID, "candidate outside the resolved project scope must be rejected, not reaped")

	require.True(t, killedPGIDs[ownedPGID], "owned pgid must have been signaled")
	require.False(t, killedPGIDs[foreignPGID], "foreign pgid must never reach the kill call")
}

// TestHubHandlePortsCleanOrphans_ScopeSwitchesEligibility proves the gate
// decision is driven by the EXACT resolved scope, not a static allowlist:
// the same two candidate pgids flip which one is eligible depending on
// which project the calling session resolves to.
func TestHubHandlePortsCleanOrphans_ScopeSwitchesEligibility(t *testing.T) {
	// No t.Parallel(): overrides package-level fake-injection vars shared
	// across the daemon package.
	_, sockPath := newBootedDaemon(t)

	projA := normalizePath(t.TempDir())
	projB := normalizePath(t.TempDir())

	const pgidA = 900011
	const pgidB = 900012

	origScan, origWalk, origKill := scanOrphanedPGIDsFn, walkParentsFn, killSessionPGIDFn
	t.Cleanup(func() {
		scanOrphanedPGIDsFn, walkParentsFn, killSessionPGIDFn = origScan, origWalk, origKill
	})

	scanOrphanedPGIDsFn = func(_ int, _ map[int]bool) []platform.OrphanPGID {
		return []platform.OrphanPGID{
			{PGID: pgidA, Members: []int{pgidA}},
			{PGID: pgidB, Members: []int{pgidB}},
		}
	}
	walkParentsFn = func(pid int) []platform.AncestorInfo {
		switch pid {
		case pgidA:
			return []platform.AncestorInfo{{PID: pid, Cmdline: "agnt run child", Cwd: projA}}
		case pgidB:
			return []platform.AncestorInfo{{PID: pid, Cmdline: "agnt run child", Cwd: projB}}
		default:
			return nil
		}
	}
	killSessionPGIDFn = func(pgid int, _ int, _ time.Duration, _ bool) error { return nil }

	// A session scoped to project A only reaps pgidA, never pgidB — even
	// though pgidB carries the exact same cmdline evidence.
	clientA := registerSessionClient(t, sockPath, "sess-a-switch", projA)
	rawA, err := clientA.Conn().Request(protocol.VerbPorts, protocol.SubVerbCleanOrphans).
		WithJSON(protocol.DirectoryFilter{}).JSON()
	require.NoError(t, err)
	reapedA := toIntSlice(t, rawA["reaped_pgids"])
	require.Contains(t, reapedA, pgidA)
	require.NotContains(t, reapedA, pgidB)
}

// toIntSlice re-decodes a JSON-response numeric slice (unmarshaled into
// interface{} as float64) into []int for require.Contains comparisons.
func toIntSlice(t *testing.T, raw interface{}) []int {
	t.Helper()
	items, ok := raw.([]interface{})
	require.True(t, ok, "expected a JSON array, got %T", raw)
	out := make([]int, 0, len(items))
	for _, item := range items {
		f, ok := item.(float64)
		require.True(t, ok, "expected numeric array element, got %T", item)
		out = append(out, int(f))
	}
	return out
}
