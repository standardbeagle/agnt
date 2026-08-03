package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// testContext returns a context that is cancelled when the test ends.
// Kept local to this file so it does not collide with other test
// helpers in the package.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestUnifiedErrors_SuppressProxyReadinessSentinel is the end-to-end
// repro for the 17:43 startup race described in the Dart task. A
// real proxy is created with a `wait-for` dependency closed; the
// test hits the proxy several times to generate 503 sentinel logs,
// then asserts that convertHTTPErrorDirect filters all of them out
// — mimicking what the snapshot collectors see during the gating window.
//
// The same proxy is then re-tested with a genuine 500 response body
// (no sentinel) to verify the filter does NOT suppress real upstream
// errors by status code alone.
func TestUnifiedErrors_SuppressProxyReadinessSentinel(t *testing.T) {
	// Backend is never reached because the gate is closed, but we
	// still need a valid target URL for ProxyServer construction.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	ps, err := proxy.NewProxyServer(proxy.ProxyConfig{
		ID:         "gated-repro",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	})
	require.NoError(t, err)

	// Close the gate — proxy is now waiting on dev-backend.
	ps.SetDependencies([]string{"dev-backend"})

	// Start the proxy so it binds a port we can hit.
	require.NoError(t, ps.Start(testContext(t)))
	defer ps.Stop(testContext(t))
	<-ps.Ready()

	// Fire several requests while the gate is closed. Each returns
	// 503 with the sentinel body.
	for i := 0; i < 5; i++ {
		resp, err := http.Get("http://" + ps.ListenAddr + "/api/data")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	}

	// Pull the log entries and run them through the unified-error
	// converter. All sentinel-bearing 503s must be dropped — that's
	// the contract the AI agent depends on.
	entries := ps.Logger().Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypeHTTP}, Limit: 10})
	require.Len(t, entries, 5, "expected one logged 503 per request")

	var surfaced []unifiedError
	for _, e := range entries {
		surfaced = append(surfaced, convertHTTPErrorDirect("gated-repro", e.HTTP)...)
	}
	assert.Empty(t, surfaced,
		"readiness 503s should be filtered from the unified-error view even though they are logged; got %v",
		surfaced)

	// Now flip the gate and synthesize a real 500 response. The
	// filter must not drop it — we only suppress 503s carrying the
	// sentinel, not every 5xx.
	ps.MarkDependencyReady("dev-backend")
	assert.True(t, ps.IsReadyForForwarding())

	realErr := &proxy.HTTPLogEntry{
		ID:           "real-err",
		Timestamp:    time.Now(),
		Method:       "POST",
		URL:          "/api/users",
		StatusCode:   500,
		ResponseBody: `{"error":"database connection timeout"}`,
	}
	realUnified := convertHTTPErrorDirect("gated-repro", realErr)
	require.Len(t, realUnified, 1, "real 500 must surface in the unified-error view")
	assert.Equal(t, "error", realUnified[0].Severity)
	assert.Contains(t, realUnified[0].Message, "database connection timeout")
}
