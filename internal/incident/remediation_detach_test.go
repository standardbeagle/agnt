package incident

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBus_RemediationStatusCodes_DetachedFromSourceAndDedup pins the
// detachment property cloneIncidentEvent (dedup.go) guarantees: a
// []int status_codes value carried on an IncidentEvent's Remediation.PrimaryArgs
// through the real bus → dedup → inbox Sample path is deep-copied, not
// aliased. Structural-clone coverage elsewhere (TestDedup_FirstAndLastPreserved
// in dedup_test.go) only proves First/Last hold independent *primitive*
// fields (Summary); it never exercises a reference-typed value (slice) inside
// the event, which is exactly where Go's plain struct-value-copy semantics
// stop protecting you — copying an IncidentEvent copies the map header but
// not the slice backing array underneath it.
func TestBus_RemediationStatusCodes_DetachedFromSourceAndDedup(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("sess-detach", nil, nil, nil)

	sourceCodes := []int{500, 502, 503, 504}
	ev := IncidentEvent{
		ID:          newID(),
		Fingerprint: "detach-fp",
		ReceivedAt:  time.Now(),
		Source:      SourceHTTP5xx,
		Severity:    SeverityError,
		Category:    "5xx",
		Summary:     "GET /api/x → 500",
		Ctx:         Context{SessionID: "sess-detach"},
		Remediation: Remediation{
			PrimaryArgs: map[string]any{"status_codes": sourceCodes},
		},
	}

	bus.Publish(ev)

	var sampleCodes []int
	require.Eventually(t, func() bool {
		entries, _ := bus.QuerySession("sess-detach", QueryFilter{})
		if len(entries) != 1 || entries[0].Sample == nil {
			return false
		}
		raw, ok := entries[0].Sample.Remediation.PrimaryArgs["status_codes"]
		if !ok {
			return false
		}
		sampleCodes, ok = raw.([]int)
		return ok
	}, 2*time.Second, 5*time.Millisecond, "event must be delivered and queryable")

	require.Equal(t, []int{500, 502, 503, 504}, sampleCodes, "sampled slice starts equal to the source")

	// Mutating the ORIGINAL slice after delivery must not change the
	// delivered/sampled copy. The sampled copy is the same object the
	// Deduplicator holds internally as DedupEntry.Last (bus.go wires
	// Sample: &de.Last), so this single assertion covers detachment from
	// both the source event's backing array and the dedup-held backing array.
	sourceCodes[0] = 999
	assert.Equal(t, 500, sampleCodes[0],
		"mutating the source event's slice after delivery must not alter the delivered sample (no aliasing)")

	// And the reverse: mutating the delivered/sampled slice must not reach
	// back into the original event's slice.
	sampleCodes[1] = 111
	assert.Equal(t, 502, sourceCodes[1],
		"mutating the sampled slice must not alter the source event's slice (no aliasing)")
}

// TestBus_RemediationStatusCodes_DetachedAcrossSessions pins the sibling case:
// the same IncidentEvent fanned out to two sessions (bus.deliver's per-session
// loop in MPSCBus.deliver) must not let one session's dedup/inbox Sample alias
// another session's — Contract #1 (Cross-session isolation,
// .claude/rules/daemon-architecture.md § Incident Pipeline) applies to every
// field on the event, including reference-typed Remediation args, not just the
// top-level struct.
func TestBus_RemediationStatusCodes_DetachedAcrossSessions(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("sess-a", nil, nil, nil)
	bus.AddSession("sess-b", nil, nil, nil)

	ev := IncidentEvent{
		ID:          newID(),
		Fingerprint: "cross-session-fp",
		ReceivedAt:  time.Now(),
		Source:      SourceHTTP5xx,
		Severity:    SeverityError,
		Remediation: Remediation{
			PrimaryArgs: map[string]any{"status_codes": []int{400, 401}},
		},
	}
	evA := ev
	evA.Ctx.SessionID = "sess-a"
	evB := ev
	evB.Ctx.SessionID = "sess-b"
	bus.Publish(evA)
	bus.Publish(evB)

	var codesA, codesB []int
	require.Eventually(t, func() bool {
		entriesA, _ := bus.QuerySession("sess-a", QueryFilter{})
		entriesB, _ := bus.QuerySession("sess-b", QueryFilter{})
		if len(entriesA) != 1 || len(entriesB) != 1 {
			return false
		}
		if entriesA[0].Sample == nil || entriesB[0].Sample == nil {
			return false
		}
		var ok bool
		codesA, ok = entriesA[0].Sample.Remediation.PrimaryArgs["status_codes"].([]int)
		if !ok {
			return false
		}
		codesB, ok = entriesB[0].Sample.Remediation.PrimaryArgs["status_codes"].([]int)
		return ok
	}, 2*time.Second, 5*time.Millisecond, "event must be delivered to both sessions")

	codesA[0] = 777
	assert.Equal(t, 400, codesB[0], "session A's sample slice must not alias session B's")
}
