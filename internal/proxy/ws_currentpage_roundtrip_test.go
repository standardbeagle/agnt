package proxy

// Tier 3 — WebSocket roundtrip seam. Closes the gap between parse-only unit
// tests (ws_parse_test.go) and storage-only unit tests (pagetracker_*_test.go):
// here a REAL gorilla WebSocket client dials the live proxy's
// /__devtool_metrics endpoint and pushes the exact envelopes the injected JS
// emits ({type,data,url,session_id,frame_id}), then asserts every telemetry
// kind lands in the PageTracker via the production handleWebSocket → parse →
// Track* wire. This is the path no test previously exercised end to end.

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dialMetrics(t *testing.T, listenAddr string) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: listenAddr, Path: "/__devtool_metrics"}
	var conn *websocket.Conn
	var err error
	header := http.Header{"Origin": {"http://" + listenAddr}}
	require.Eventually(t, func() bool {
		conn, _, err = websocket.DefaultDialer.Dial(u.String(), header)
		return err == nil
	}, 3*time.Second, 20*time.Millisecond, "metrics websocket must accept a client")
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// createContentSession drives a real content-frame document request through the
// proxy so the tracker holds a session keyed by frame id f1 on the clean URL.
func createContentSession(t *testing.T, ps *ProxyServer, proxyURL string) string {
	t.Helper()
	_ = getWrap(t, proxyURL+"/app?"+frameMarkerParam+"=f1", "iframe")
	var id string
	require.Eventually(t, func() bool {
		for _, s := range ps.PageTracker().GetActiveSessions() {
			if s.URL == "/app" || s.URL == proxyURL+"/app" {
				id = s.ID
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "content-frame request must create a tracked session")
	return id
}

func TestWS_CurrentPage_RoundtripAllTelemetryKinds(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)
	proxyURL := "http://" + ps.ListenAddr

	sid := createContentSession(t, ps, proxyURL)
	conn := dialMetrics(t, ps.ListenAddr)

	// Helper to write one envelope addressed to the content frame f1.
	send := func(typ string, data map[string]interface{}) {
		require.NoError(t, conn.WriteJSON(map[string]interface{}{
			"type":       typ,
			"data":       data,
			"url":        "/app",
			"session_id": "", // resolve by frame id, the always-wrap path
			"frame_id":   "f1",
		}))
	}

	// Two interactions (batched, as the client sends them).
	send("interactions", map[string]interface{}{
		"events": []interface{}{
			map[string]interface{}{"event_type": "click", "target": map[string]interface{}{"selector": "#go"}},
			map[string]interface{}{"event_type": "keydown", "key": map[string]interface{}{"key": "a"}},
		},
	})
	// One mutation.
	send("mutations", map[string]interface{}{
		"events": []interface{}{
			map[string]interface{}{"mutation_type": "added"},
		},
	})
	// One error.
	send("error", map[string]interface{}{
		"message": "BoomError",
		"error":   "ReferenceError: boom",
		"stack":   "ReferenceError: boom\n  at app.js:1",
	})
	// Performance with a title to promote.
	send("performance", map[string]interface{}{
		"page_title":     "WS Promoted Title",
		"load_event_end": float64(1234),
		"page_width":     float64(1280),
		"viewport_width": float64(1024),
	})

	// Everything must land on the one session, resolved purely by frame id.
	var got *PageSession
	require.Eventually(t, func() bool {
		s, ok := ps.PageTracker().GetSession(sid)
		if !ok {
			return false
		}
		if s.InteractionCount == 2 && s.MutationCount == 1 &&
			len(s.Errors) == 1 && s.Performance != nil {
			got = s
			return true
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "all telemetry kinds must arrive over the live WS")

	assert.Equal(t, 2, got.InteractionCount, "both batched interactions tracked")
	assert.Equal(t, "click", got.Interactions[0].EventType)
	assert.Equal(t, "#go", got.Interactions[0].Target.Selector, "interaction target parsed end to end")
	assert.Equal(t, "keydown", got.Interactions[1].EventType)

	assert.Equal(t, 1, got.MutationCount)
	assert.Equal(t, "added", got.Mutations[0].MutationType)

	require.Len(t, got.Errors, 1)
	assert.Equal(t, "BoomError", got.Errors[0].Message, "error message survives the wire")

	require.NotNil(t, got.Performance)
	assert.Equal(t, int64(1234), got.Performance.LoadEventEnd)
	assert.Equal(t, "WS Promoted Title", got.PageTitle, "performance title promoted to the session")
}

// TestWS_CurrentPage_UnknownFrameIsDropped verifies the resolution boundary:
// telemetry addressed to a frame/url with no session is silently discarded —
// no panic, no fabricated session.
func TestWS_CurrentPage_UnknownFrameIsDropped(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)

	conn := dialMetrics(t, ps.ListenAddr)
	require.NoError(t, conn.WriteJSON(map[string]interface{}{
		"type":     "interactions",
		"data":     map[string]interface{}{"events": []interface{}{map[string]interface{}{"event_type": "click"}}},
		"url":      "/nowhere",
		"frame_id": "ghost",
	}))

	// Give the server time to process, then assert nothing was created.
	assert.Never(t, func() bool {
		return len(ps.PageTracker().GetActiveSessions()) > 0
	}, 500*time.Millisecond, 50*time.Millisecond, "telemetry for an unknown session creates nothing")
}
