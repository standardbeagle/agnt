package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScopeResolve_MCPPath_ReturnsGlobal is the regression guard for the
// "expected SCOPE RESOLVE" report. RESOLVE is registered as a sub-verb, so the
// wire parser lifts it into cmd.SubVerb and leaves cmd.Args empty; the handler
// used to read cmd.Args[0] and therefore rejected every real SCOPE RESOLVE call.
// This drives the full client -> daemon -> result path (the exact seam a
// hub-verb unit test constructing cmd.Args by hand would have missed).
func TestScopeResolve_MCPPath_ReturnsGlobal(t *testing.T) {
	_, c, _ := newBootedDaemonWithClient(t)

	global, err := c.ResolveQueryScope(protocol.DirectoryFilter{Global: true})
	require.NoError(t, err, "SCOPE RESOLVE must succeed over the real wire, not reject its own registered sub-verb")
	assert.True(t, global, "global:true must resolve to a global scope")
}

// TestIncidentsQuery_SessionLess_ReturnsCandidatesNotError pins the headline
// fix: an MCP caller with no attached session (the daemon connection is never
// session-bound) gets the sessions to pick from, in ONE call, rather than a
// bare "no session attached" error.
func TestIncidentsQuery_SessionLess_ReturnsCandidatesNotError(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	activeSession(t, d.sessionRegistry, "sess-a", shortTempDir(t), "http://127.0.0.1:19191")
	activeSession(t, d.sessionRegistry, "sess-b", shortTempDir(t), "http://127.0.0.1:29292")
	d.addIncidentSession("sess-a")
	d.addIncidentSession("sess-b")

	result, err := c.IncidentQuery(protocol.IncidentQueryFilter{})
	require.NoError(t, err, "a session-less query must not error when candidates exist")
	require.NotNil(t, result)
	assert.True(t, result.ScopeAmbiguous, "no inbox resolved -> ambiguous")
	assert.Empty(t, result.Incidents, "no inbox resolved -> no incidents")
	require.Len(t, result.ScopeCandidates, 2, "both active sessions must be offered")

	codes := map[string]bool{}
	for _, cand := range result.ScopeCandidates {
		codes[cand.SessionCode] = true
	}
	assert.True(t, codes["sess-a"] && codes["sess-b"], "candidate list must name both sessions, got %v", codes)
}

// TestIncidentsQuery_ExplicitSession_ReadsThatInbox verifies the caller can pick
// a candidate and read exactly that session's inbox in the follow-up call.
func TestIncidentsQuery_ExplicitSession_ReadsThatInbox(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	activeSession(t, d.sessionRegistry, "sess-a", shortTempDir(t), "http://127.0.0.1:19191")
	d.addIncidentSession("sess-a")
	publishTestIncident(t, d, "sess-a", "fp-a", "boom in session a")

	result, err := c.IncidentQuery(protocol.IncidentQueryFilter{SessionCode: "sess-a"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.ScopeAmbiguous, "an explicit, valid session resolves an inbox")
	require.Len(t, result.Incidents, 1)
	assert.Contains(t, result.Incidents[0].Summary, "boom in session a")
}

// TestIncidentsQuery_CandidateList_NoContentLeak is the isolation guard: the
// candidate list is METADATA ONLY (numbered contract 1). Session B holding a
// distinctively-worded incident must not have that content appear anywhere in
// the session-less disambiguation response.
func TestIncidentsQuery_CandidateList_NoContentLeak(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	activeSession(t, d.sessionRegistry, "sess-a", shortTempDir(t), "http://127.0.0.1:19191")
	activeSession(t, d.sessionRegistry, "sess-b", shortTempDir(t), "http://127.0.0.1:29292")
	d.addIncidentSession("sess-a")
	d.addIncidentSession("sess-b")

	const secret = "SUPER_SECRET_INCIDENT_ONLY_IN_B"
	publishTestIncident(t, d, "sess-b", "fp-secret", secret)

	result, err := c.IncidentQuery(protocol.IncidentQueryFilter{})
	require.NoError(t, err)
	require.True(t, result.ScopeAmbiguous)

	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secret,
		"the candidate list must not leak another session's incident content")
	assert.Contains(t, string(raw), "sess-b", "candidate metadata (the code) is fine to disclose")
}

// TestIncidentsQuery_NoSessions_Errors keeps the genuine "no valid choice"
// answer: with no session at all, an error is still correct and says how to fix.
func TestIncidentsQuery_NoSessions_Errors(t *testing.T) {
	_, c, _ := newBootedDaemonWithClient(t)

	_, err := c.IncidentQuery(protocol.IncidentQueryFilter{})
	require.Error(t, err, "with zero sessions there is no candidate to offer, so an error is correct")
	assert.Contains(t, err.Error(), "no active sessions")
}

// publishTestIncident publishes one incident into a session inbox and waits for
// the async bus to deliver it, so the reading assertion is not racing the bus.
func publishTestIncident(t *testing.T, d *Daemon, sessionCode, fingerprint, summary string) {
	t.Helper()
	d.incidentBus.Publish(incident.IncidentEvent{
		ID:          fingerprint,
		Fingerprint: fingerprint,
		Type:        incident.MessageError,
		ReceivedAt:  time.Now(),
		Source:      incident.SourceBrowserJS,
		Severity:    incident.SeverityError,
		Category:    "TestError",
		Summary:     summary,
		Ctx:         incident.Context{SessionID: sessionCode},
	})
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
		for _, e := range entries {
			if e.Fingerprint == fingerprint {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "published incident never reached session %s inbox", sessionCode)
}
