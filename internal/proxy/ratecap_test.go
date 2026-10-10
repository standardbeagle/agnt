package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxyRequestRateCapReturns429 pins the dev proxy's request-rate cap: past
// the burst a client gets 429 without the backend being touched, the refusal is
// reported as a diagnostic, and the budget comes back as the clock advances.
func TestProxyRequestRateCapReturns429(t *testing.T) {
	var backendHits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHits.Add(1)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{ID: "ratecap", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 100})
	require.NoError(t, err)
	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	const burst = 3
	ps.requestLimiter = publish.NewRateLimiter(60, burst, func() time.Time { return time.Unix(0, nowNanos.Load()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, ps.Start(ctx))
	t.Cleanup(func() { ps.Stop(ctx) })
	<-ps.Ready()

	get := func() int {
		resp, err := http.Get("http://" + ps.ListenAddr + "/api/x")
		require.NoError(t, err)
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	for i := 0; i < burst; i++ {
		require.Equal(t, http.StatusOK, get(), "request %d is within the burst", i)
	}
	assert.Equal(t, http.StatusTooManyRequests, get(), "request past the burst must be refused")
	assert.Equal(t, http.StatusTooManyRequests, get())
	assert.EqualValues(t, burst, backendHits.Load(), "a refused request must not reach the backend")

	capped := 0
	for _, e := range ps.logger.Query(LogFilter{Types: []LogEntryType{LogTypeDiagnostic}}) {
		if e.Diagnostic != nil && e.Diagnostic.Event == "request_rate_capped" {
			capped++
		}
	}
	assert.Equal(t, 1, capped, "the cap reports itself once per window, not per refusal")

	nowNanos.Add(int64(2 * time.Second)) // 1 token/sec → two tokens back
	assert.Equal(t, http.StatusOK, get(), "budget must refill with time")
}
