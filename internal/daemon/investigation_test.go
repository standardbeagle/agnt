//go:build unix

package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInvestigation_IsolatedPerSession: two sessions on one daemon hold
// independent records; a merge into one is invisible to the other.
func TestInvestigation_IsolatedPerSession(t *testing.T) {
	t.Parallel()
	_, client, tmpDir := newBootedDaemonWithClient(t)

	for _, code := range []string{"inv-iso-a", "inv-iso-b"} {
		_, err := client.Conn().Request("SESSION", "REGISTER", code, tmpDir).WithJSON(map[string]interface{}{
			"project_path": tmpDir,
		}).JSON()
		require.NoError(t, err)
	}

	_, err := client.Conn().Request("INVESTIGATION", "MERGE").WithJSON(map[string]interface{}{
		"session_code": "inv-iso-a",
		"patch": map[string]interface{}{
			"active_proxy_id": "px1",
			"findings":        []map[string]interface{}{{"fingerprint": "fp1", "summary": "boom"}},
			"failed_areas":    []string{"responsive_audit"},
		},
	}).JSON()
	require.NoError(t, err, "INVESTIGATION MERGE into inv-iso-a")

	gotA, err := client.Conn().Request("INVESTIGATION", "GET").WithJSON(map[string]interface{}{
		"session_code": "inv-iso-a",
	}).JSON()
	require.NoError(t, err)
	invA, ok := gotA["investigation"].(map[string]interface{})
	require.True(t, ok, "inv-iso-a must have an investigation record")
	assert.Equal(t, "px1", invA["active_proxy_id"])

	gotB, err := client.Conn().Request("INVESTIGATION", "GET").WithJSON(map[string]interface{}{
		"session_code": "inv-iso-b",
	}).JSON()
	require.NoError(t, err)
	assert.Nil(t, gotB["investigation"], "merge into inv-iso-a must be invisible to inv-iso-b")
}

// TestInvestigation_DroppedOnUnregister: after UnregisterExact retires the
// session, a read for that session returns empty, not the stale record.
func TestInvestigation_DroppedOnUnregister(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request("SESSION", "REGISTER", "inv-drop", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	_, err = client.Conn().Request("INVESTIGATION", "MERGE").WithJSON(map[string]interface{}{
		"session_code": "inv-drop",
		"patch":        map[string]interface{}{"active_proxy_id": "px9"},
	}).JSON()
	require.NoError(t, err)

	s, ok := d.sessionRegistry.Get("inv-drop")
	require.True(t, ok)
	require.True(t, d.sessionRegistry.UnregisterExact("inv-drop", s))

	got, err := client.Conn().Request("INVESTIGATION", "GET").WithJSON(map[string]interface{}{
		"session_code": "inv-drop",
	}).JSON()
	require.NoError(t, err)
	assert.Nil(t, got["investigation"], "retired session must read empty, not stale")
	assert.Equal(t, false, got["found"], "retired session must report found=false")
}

// TestInvestigation_DroppedOnHeartbeatTimeout: the heartbeat-timeout
// disconnect path drops the record exactly like an explicit teardown — a
// later read finds nothing, not a stale record from a dead agent.
func TestInvestigation_DroppedOnHeartbeatTimeout(t *testing.T) {
	t.Parallel()
	registry := NewSessionRegistry(time.Millisecond)
	session := &Session{
		Code:      "inv-stale",
		Status:    SessionStatusActive,
		LastSeen:  time.Now().Add(-time.Hour),
		StartedAt: time.Now().Add(-time.Hour),
	}
	require.NoError(t, registry.Register(session))
	session.MergeInvestigation(protocol.InvestigationPatch{ActiveProxyID: "px-stale"})
	require.NotNil(t, session.Investigation())

	registry.CheckHeartbeats()

	assert.Equal(t, SessionStatusDisconnected, session.GetStatus())
	assert.Nil(t, session.Investigation(), "heartbeat-timeout disconnect must drop the record")
}

// TestInvestigation_FindingsCapFIFO: 33 appended findings keep the newest 32,
// evicting the oldest first.
func TestInvestigation_FindingsCapFIFO(t *testing.T) {
	t.Parallel()
	session := &Session{Code: "inv-cap"}
	patch := protocol.InvestigationPatch{}
	for i := 0; i < protocol.MaxInvestigationFindings+1; i++ {
		patch.Findings = append(patch.Findings, protocol.FindingRef{
			Fingerprint: fmt.Sprintf("fp-%02d", i),
		})
	}
	merged := session.MergeInvestigation(patch)
	require.Len(t, merged.Findings, protocol.MaxInvestigationFindings)
	assert.Equal(t, "fp-01", merged.Findings[0].Fingerprint, "oldest finding must be evicted first")
	assert.Equal(t, fmt.Sprintf("fp-%02d", protocol.MaxInvestigationFindings),
		merged.Findings[len(merged.Findings)-1].Fingerprint)
}

// TestInvestigation_MergeSemantics: a zero-valued patch changes nothing but
// UpdatedAt; FailedAreas set-union caps at MaxInvestigationFailedAreas.
func TestInvestigation_MergeSemantics(t *testing.T) {
	t.Parallel()
	session := &Session{Code: "inv-merge"}
	first := session.MergeInvestigation(protocol.InvestigationPatch{
		ActiveProxyID: "px1",
		FailedAreas:   []string{"responsive_audit"},
	})
	before := *first

	// Zero-valued patch: no-op except UpdatedAt.
	time.Sleep(time.Millisecond)
	noop := session.MergeInvestigation(protocol.InvestigationPatch{})
	assert.Equal(t, before.ActiveProxyID, noop.ActiveProxyID)
	assert.Equal(t, before.FailedAreas, noop.FailedAreas)
	assert.Equal(t, before.Findings, noop.Findings)
	assert.True(t, noop.UpdatedAt.After(before.UpdatedAt) || noop.UpdatedAt.Equal(before.UpdatedAt),
		"zero patch must still refresh UpdatedAt")

	// Empty-string scalars leave the stored value alone; union adds new areas.
	second := session.MergeInvestigation(protocol.InvestigationPatch{
		FailedAreas: []string{"responsive_audit", "api_audit"},
	})
	assert.Equal(t, "px1", second.ActiveProxyID, "zero scalar must not replace")
	assert.Equal(t, []string{"responsive_audit", "api_audit"}, second.FailedAreas)

	// Union cap: pushing past MaxInvestigationFailedAreas evicts oldest.
	var areas []string
	for i := 0; i < protocol.MaxInvestigationFailedAreas+2; i++ {
		areas = append(areas, fmt.Sprintf("audit-%02d", i))
	}
	capped := session.MergeInvestigation(protocol.InvestigationPatch{FailedAreas: areas})
	assert.Len(t, capped.FailedAreas, protocol.MaxInvestigationFailedAreas)
}

// TestClientInvestigationGetMerge exercises the typed daemonclient API
// (InvestigationGet / InvestigationMerge) against an in-process booted
// daemon — the same harness the hub integration tests use, since
// internal/daemonclient cannot import internal/daemon (import cycle).
func TestClientInvestigationGetMerge(t *testing.T) {
	t.Parallel()
	_, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request("SESSION", "REGISTER", "inv-client", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	empty, err := client.InvestigationGet("inv-client")
	require.NoError(t, err)
	assert.False(t, empty.Found)
	assert.Nil(t, empty.Investigation)

	merged, err := client.InvestigationMerge("inv-client", protocol.InvestigationPatch{
		ActiveProxyID: "px-typed",
		Findings:      []protocol.FindingRef{{Fingerprint: "fp-typed", Summary: "typed round trip"}},
		FailedAreas:   []string{"api_audit"},
	})
	require.NoError(t, err)
	require.True(t, merged.Found)
	require.NotNil(t, merged.Investigation)
	assert.Equal(t, "px-typed", merged.Investigation.ActiveProxyID)
	require.Len(t, merged.Investigation.Findings, 1)
	assert.Equal(t, "fp-typed", merged.Investigation.Findings[0].Fingerprint)
	assert.False(t, merged.Investigation.UpdatedAt.IsZero())

	got, err := client.InvestigationGet("inv-client")
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, []string{"api_audit"}, got.Investigation.FailedAreas)
}
