package proxy

// Tier 1 — unit edge cases for the PageTracker actor. These pin EXPECTED
// behavior validated against a real browser in the e2e harness
// (currentpage_e2e_browser_test.go): marker-stripped session URLs,
// shell+content coalescing, FIFO history caps, resolution precedence, eviction
// map cleanup, and no-op tracking on unresolved sessions.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docEntrySID builds an HTML document request for url carrying a __devtool_sid
// cookie with browserSessionID (the tab-identity signal TrackHTTPRequest reads).
// Complements docEntry(url) in pagetracker_frame_test.go, which omits the cookie.
func docEntrySID(url, browserSessionID string) HTTPLogEntry {
	e := docEntry(url)
	if browserSessionID != "" {
		e.RequestHeaders = map[string]string{"Cookie": "__devtool_sid=" + browserSessionID}
	}
	return e
}

// newTrackerWithDoc returns a started tracker holding one freshly-created
// session for the given doc, plus the resolved session id.
func newTrackerWithDoc(t *testing.T, maxSessions int, url, bsid string) (*PageTracker, string) {
	t.Helper()
	pt := NewPageTracker(maxSessions, time.Minute)
	t.Cleanup(pt.Stop)
	pt.TrackHTTPRequest(docEntrySID(url, bsid))
	var id string
	require.Eventually(t, func() bool {
		s := pt.GetActiveSessions()
		if len(s) == 1 {
			id = s[0].ID
			return true
		}
		return false
	}, time.Second, 5*time.Millisecond, "doc request must create exactly one session")
	return pt, id
}

func TestAppendBounded_FIFOCapAndGrowth(t *testing.T) {
	// Under capacity: plain append, order preserved.
	s := []int{}
	for i := 0; i < 3; i++ {
		s = appendBounded(s, i, 4)
	}
	assert.Equal(t, []int{0, 1, 2}, s, "below cap appends in order")

	// At/over capacity: oldest shifted out, newest retained, len pinned to cap.
	for i := 3; i < 8; i++ {
		s = appendBounded(s, i, 4)
	}
	assert.Len(t, s, 4, "length pinned to cap")
	assert.Equal(t, []int{4, 5, 6, 7}, s, "FIFO: oldest dropped, newest kept, order preserved")
}

func TestTracker_InteractionHistory_CapsButCountsAll(t *testing.T) {
	pt, _ := newTrackerWithDoc(t, 10, "/p", "b1")

	const n = MaxInteractionsPerSession + 50
	for i := 0; i < n; i++ {
		pt.TrackInteraction(InteractionEvent{
			EventType: "click",
			URL:       "/p",
			Value:     itoa(i), // tag each event with its index
		}, "b1")
	}

	var got *PageSession
	require.Eventually(t, func() bool {
		s := pt.GetActiveSessions()
		if len(s) == 1 && s[0].InteractionCount == n {
			got = s[0]
			return true
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	assert.Equal(t, n, got.InteractionCount, "count reflects ALL interactions, not slice length")
	assert.Len(t, got.Interactions, MaxInteractionsPerSession, "slice capped to max")
	// Newest retained, oldest dropped: last element is index n-1; first is n-cap.
	assert.Equal(t, itoa(n-1), got.Interactions[len(got.Interactions)-1].Value, "newest retained")
	assert.Equal(t, itoa(n-MaxInteractionsPerSession), got.Interactions[0].Value, "oldest beyond cap dropped")
}

func TestTracker_MutationHistory_CapsButCountsAll(t *testing.T) {
	pt, _ := newTrackerWithDoc(t, 10, "/p", "b1")

	const n = MaxMutationsPerSession + 25
	for i := 0; i < n; i++ {
		pt.TrackMutation(MutationEvent{MutationType: "added", URL: "/p"}, "b1")
	}

	require.Eventually(t, func() bool {
		s := pt.GetActiveSessions()
		return len(s) == 1 && s[0].MutationCount == n
	}, 2*time.Second, 5*time.Millisecond)

	s := pt.GetActiveSessions()[0]
	assert.Equal(t, n, s.MutationCount)
	assert.Len(t, s.Mutations, MaxMutationsPerSession, "mutation slice capped independently of interactions")
}

func TestTracker_ResolutionPrecedence_FrameOverBrowserOverURL(t *testing.T) {
	pt := NewPageTracker(10, time.Minute)
	t.Cleanup(pt.Stop)

	// S1: /a, browser b1, content-frame f1.
	pt.TrackHTTPRequest(docEntrySID("/a?"+frameMarkerParam+"=f1", "b1"))
	// S2: /b, browser b2, content-frame f2.
	pt.TrackHTTPRequest(docEntrySID("/b?"+frameMarkerParam+"=f2", "b2"))

	var s1, s2 string
	require.Eventually(t, func() bool {
		sessions := pt.GetActiveSessions()
		if len(sessions) != 2 {
			return false
		}
		for _, s := range sessions {
			switch s.URL {
			case "/a":
				s1 = s.ID
			case "/b":
				s2 = s.ID
			}
		}
		return s1 != "" && s2 != ""
	}, time.Second, 5*time.Millisecond)

	// Frame id wins even when browser/url point elsewhere.
	assert.Equal(t, s1, pt.ResolveSession("b2", "/b", "f1"), "content frame id has top precedence")
	// Browser id wins over URL when no frame supplied.
	assert.Equal(t, s1, pt.ResolveSession("b1", "/b", ""), "browser session beats URL")
	// URL is the last resort.
	assert.Equal(t, s2, pt.ResolveSession("", "/b", ""), "URL resolves when no frame/browser")
	// Total miss resolves to empty.
	assert.Equal(t, "", pt.ResolveSession("nope", "/zzz", "nope"), "unknown keys resolve to empty")
}

func TestTracker_Eviction_DropsOldestAndItsMappings(t *testing.T) {
	pt := NewPageTracker(2, time.Minute) // cap of 2
	t.Cleanup(pt.Stop)

	// Three distinct tabs/pages; oldest is /a (b1/f1).
	pt.TrackHTTPRequest(docEntrySID("/a?"+frameMarkerParam+"=f1", "b1"))
	time.Sleep(2 * time.Millisecond) // ensure distinct StartTime ordering
	pt.TrackHTTPRequest(docEntrySID("/b?"+frameMarkerParam+"=f2", "b2"))
	time.Sleep(2 * time.Millisecond)
	pt.TrackHTTPRequest(docEntrySID("/c?"+frameMarkerParam+"=f3", "b3"))

	require.Eventually(t, func() bool {
		return len(pt.GetActiveSessions()) == 2
	}, time.Second, 5*time.Millisecond, "cap enforced: exactly 2 sessions survive")

	// Oldest (/a) evicted: all three of its lookup keys must be gone.
	assert.Equal(t, "", pt.ResolveSession("b1", "", ""), "evicted browser-session mapping cleared")
	assert.Equal(t, "", pt.ResolveSession("", "/a", ""), "evicted url mapping cleared")
	assert.Equal(t, "", pt.ResolveSession("", "", "f1"), "evicted frame mapping cleared")

	// Survivors still resolve by every key.
	assert.NotEqual(t, "", pt.ResolveSession("", "", "f2"), "survivor /b still resolves by frame")
	assert.NotEqual(t, "", pt.ResolveSession("b3", "", ""), "survivor /c still resolves by browser")
}

func TestTracker_TrackOnUnresolvedSession_IsNoOp(t *testing.T) {
	pt := NewPageTracker(10, time.Minute)
	t.Cleanup(pt.Stop)

	// No document ever created — every Track* must silently no-op, never panic,
	// never fabricate a session.
	pt.TrackInteraction(InteractionEvent{EventType: "click", URL: "/ghost"}, "ghost")
	pt.TrackMutation(MutationEvent{MutationType: "added", URL: "/ghost"}, "ghost")
	pt.TrackError(FrontendError{Message: "boom", URL: "/ghost"}, "ghost")
	pt.TrackPerformance(PerformanceMetric{URL: "/ghost"}, "ghost")

	// Drain: a resolvable query after the ops confirms they were processed.
	assert.Empty(t, pt.GetActiveSessions(), "tracking against a non-existent session creates nothing")
}

func TestTracker_StopSemantics(t *testing.T) {
	pt, _ := newTrackerWithDoc(t, 10, "/p", "b1")
	pt.Stop()

	// Idempotent.
	assert.NotPanics(t, pt.Stop, "Stop is idempotent")
	// Post-stop queries return zero values; tracks are dropped.
	assert.Nil(t, pt.GetActiveSessions(), "queries return zero after Stop")
	pt.TrackInteraction(InteractionEvent{EventType: "click", URL: "/p"}, "b1") // must not panic
	_, ok := pt.GetSession("page-1")
	assert.False(t, ok, "GetSession returns not-found after Stop")
}

// TestTracker_HTMLOnResourcePath_DoesNotSpawnSession pins the tightened
// classifier: an SPA dev server with an index.html fallback returns text/html
// for asset paths (favicon.ico, deep-linked images). Because the request PATH
// ends in a resource extension, that mis-served HTML is treated as a resource —
// it does NOT spawn its own page session and instead attaches to the live page.
func TestTracker_HTMLOnResourcePath_DoesNotSpawnSession(t *testing.T) {
	pt, _ := newTrackerWithDoc(t, 10, "/", "b1")

	// favicon.ico served as HTML (SPA fallback). A distinct browser session would
	// previously have forced a second tab session; the path-suffix rule prevents it.
	pt.TrackHTTPRequest(docEntrySID("/favicon.ico", "b2"))

	// Must stay at exactly one session — and never momentarily create a second.
	assert.Never(t, func() bool {
		return len(pt.GetActiveSessions()) != 1
	}, 300*time.Millisecond, 30*time.Millisecond, "HTML on a .ico path must not spawn a page session")

	sessions := pt.GetActiveSessions()
	require.Len(t, sessions, 1)
	assert.Equal(t, "/", sessions[0].URL, "the only session is the real page")

	res := map[string]bool{}
	for _, r := range sessions[0].Resources {
		res[r.URL] = true
	}
	assert.True(t, res["/favicon.ico"], "the mis-served favicon is recorded as a resource of the page")
}
