//go:build unix

package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_CurrentPage_EndToEnd(t *testing.T) {
	t.Parallel()
	_, client, _ := newBootedDaemonWithClient(t)

	// Create a backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><head></head><body>Test Page</body></html>"))
	}))
	defer backend.Close()

	// Start a proxy
	result, err := client.ProxyStart("test-proxy", backend.URL, 0, 100, ".")
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	listenAddr := result["listen_addr"].(string)
	t.Logf("Proxy listening on: %s", listenAddr)

	// Use ListenAddr directly since it now includes the bind address
	proxyURL := fmt.Sprintf("http://%s", listenAddr)

	// Make a request through the proxy
	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatalf("Failed to request through proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	t.Logf("Response status: %d, body length: %d", resp.StatusCode, len(body))

	// Small delay to ensure tracking is complete
	time.Sleep(100 * time.Millisecond)

	// Check current page sessions
	sessionsResult, err := client.CurrentPageList("test-proxy")
	if err != nil {
		t.Fatalf("Failed to get current page list: %v", err)
	}

	t.Logf("CurrentPageList result: %+v", sessionsResult)

	count, ok := sessionsResult["count"].(float64)
	if !ok {
		t.Fatalf("Expected count field, got: %+v", sessionsResult)
	}

	if count < 1 {
		t.Errorf("Expected at least 1 page session, got %v", count)

		// Debug: check proxy logs
		logResult, err := client.ProxyLogQuery("test-proxy", protocol.LogQueryFilter{
			Types: []string{"http"},
			Limit: 10,
		})
		if err != nil {
			t.Logf("Failed to query logs: %v", err)
		} else {
			t.Logf("Proxy logs: %+v", logResult)
		}
	}

	sessions, ok := sessionsResult["sessions"].([]interface{})
	if ok && len(sessions) > 0 {
		firstSession := sessions[0].(map[string]interface{})
		t.Logf("First session URL: %s", firstSession["url"])
		t.Logf("First session active: %v", firstSession["active"])
	}

	// Clean up
	if err := client.ProxyStop("test-proxy"); err != nil {
		t.Logf("Failed to stop proxy: %v", err)
	}
}

// TestClient_CurrentPage_GetIsLeanAndComplete locks the GET wire contract:
// the payload must NOT carry HTTP bodies/headers/navigations (the old
// full-PageSession marshal made a trivial page ~17 KB), and it MUST carry the
// per-kind counts and URL-string resources the tool-side converters need. This
// is the seam that previously had no end-to-end test, so the schema silently
// drifted (counts read as 0, resources dropped, by-type rollups all "unknown").
func TestClient_CurrentPage_GetIsLeanAndComplete(t *testing.T) {
	t.Parallel()
	d, client, _ := newBootedDaemonWithClient(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, "body{color:red}")
			return
		}
		if strings.HasSuffix(r.URL.Path, "broken.js") {
			w.WriteHeader(http.StatusInternalServerError) // a failing sub-resource
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// A chunky body — the old marshal embedded this verbatim per navigation.
		fmt.Fprint(w, `<html><head><title>Probe</title><link rel="stylesheet" href="/a.css"></head><body>`+
			strings.Repeat("x", 4000)+`</body></html>`)
	}))
	defer backend.Close()

	res, err := client.ProxyStart("lean", backend.URL, 0, 100, ".")
	require.NoError(t, err)
	addr := res["listen_addr"].(string)

	// Create the doc session + a css resource via the proxy.
	if r, err := http.Get("http://" + addr + "/"); err == nil {
		_, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}
	cssReq, _ := http.NewRequest("GET", "http://"+addr+"/a.css", nil)
	cssReq.Header.Set("Referer", "http://"+addr+"/")
	if r, err := http.DefaultClient.Do(cssReq); err == nil {
		r.Body.Close()
	}
	jsReq, _ := http.NewRequest("GET", "http://"+addr+"/broken.js", nil)
	jsReq.Header.Set("Referer", "http://"+addr+"/")
	if r, err := http.DefaultClient.Do(jsReq); err == nil {
		r.Body.Close()
	}

	// Inject telemetry directly into the tracker: 3 errors, 4 interactions, 2 mutations.
	ps, err := d.proxym.Get("lean")
	require.NoError(t, err)
	pt := ps.PageTracker()
	require.Eventually(t, func() bool { return len(pt.GetActiveSessions()) == 1 },
		3*time.Second, 20*time.Millisecond, "doc request must create a session")
	for i := 0; i < 3; i++ {
		pt.TrackError(proxy.FrontendError{Message: "Uncaught ReferenceError: x", Error: "ReferenceError: x is not defined", URL: "/"}, "")
	}
	pt.TrackInteraction(proxy.InteractionEvent{EventType: "click", URL: "/"}, "")
	pt.TrackInteraction(proxy.InteractionEvent{EventType: "click", URL: "/"}, "")
	pt.TrackInteraction(proxy.InteractionEvent{EventType: "keydown", URL: "/"}, "")
	pt.TrackInteraction(proxy.InteractionEvent{EventType: "scroll", URL: "/"}, "")
	pt.TrackMutation(proxy.MutationEvent{MutationType: "added", URL: "/"}, "")
	pt.TrackMutation(proxy.MutationEvent{MutationType: "removed", URL: "/"}, "")

	var sid string
	require.Eventually(t, func() bool {
		l, _ := client.CurrentPageList("lean")
		ss, _ := l["sessions"].([]interface{})
		if len(ss) == 1 {
			sm := ss[0].(map[string]interface{})
			if int(getFloat(sm, "error_count")) == 3 && int(getFloat(sm, "interaction_count")) == 4 {
				sid = sm["id"].(string)
				return true
			}
		}
		return false
	}, 3*time.Second, 30*time.Millisecond, "telemetry must reach the tracker")

	get, err := client.CurrentPageGet("lean", sid)
	require.NoError(t, err)
	raw, _ := json.Marshal(get)

	// --- Lean: no HTTP bodies/headers/navigations on the wire. ---
	assert.NotContains(t, string(raw), "response_body", "GET must not carry HTTP response bodies")
	assert.NotContains(t, string(raw), "request_headers", "GET must not carry request headers")
	assert.NotContains(t, string(raw), "navigations", "GET must not carry the navigation history")
	assert.NotContains(t, string(raw), strings.Repeat("x", 100), "GET must not embed the page body")
	assert.Less(t, len(raw), 3000, "GET payload stays small regardless of page body size (was ~17KB)")

	// --- Complete: counts present and correct, resources are URL strings. ---
	assert.Equal(t, 3, int(getFloat(get, "error_count")), "error_count populated")
	assert.Equal(t, 4, int(getFloat(get, "interaction_count")), "interaction_count populated")
	assert.Equal(t, 2, int(getFloat(get, "mutation_count")), "mutation_count populated")
	assert.Equal(t, 2, int(getFloat(get, "resource_count")), "resource_count populated")

	resources, ok := get["resources"].([]interface{})
	require.True(t, ok)
	require.Len(t, resources, 2)
	assert.IsType(t, "", resources[0], "resources are URL strings, not HTTPLogEntry objects")
	assert.Contains(t, resources, "/a.css", "resource URL preserved")

	// The 500 sub-resource surfaces in failed_resources for triage.
	failed, ok := get["failed_resources"].([]interface{})
	require.True(t, ok)
	require.Len(t, failed, 1, "only the broken resource is listed as failed")
	fr := failed[0].(map[string]interface{})
	assert.Equal(t, "/broken.js", fr["url"])
	assert.Equal(t, 500, int(getFloat(fr, "status")))

	// Errors carry a derived type for grouping.
	errs, ok := get["errors"].([]interface{})
	require.True(t, ok)
	require.Len(t, errs, 3)
	assert.Equal(t, "ReferenceError", errs[0].(map[string]interface{})["type"], "error type derived for SUMMARY grouping")
}

func getFloat(m map[string]interface{}, k string) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return 0
}
