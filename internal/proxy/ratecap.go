package proxy

import (
	"fmt"
	"net/http"
	"time"

	"github.com/standardbeagle/agnt/internal/publish"
)

// Dev-proxy request-rate cap, per client IP. The proxy is potentially public
// (a tunnel can expose it), so its request rate is bounded in every posture.
// The numbers are sized for development traffic, not for a public site: an
// unbundled dev server (Vite) serves a few thousand module requests on one page
// load, which must fit in the burst. Behind a tunnel every viewer arrives from
// the tunnel client's address and shares one bucket, which makes this a cap on
// the whole exposed proxy.
const (
	proxyRequestRatePerMinute = 12000 // 200 req/s sustained
	proxyRequestBurst         = 6000
	// proxyRateDiagnosticEvery spaces out the "cap hit" diagnostic so a flood
	// reports itself without flooding the traffic log.
	proxyRateDiagnosticEvery = 10 * time.Second
)

// rateLimited wraps the proxy's whole handler in the per-client request-rate
// cap. Over the cap a request gets 429 and never reaches the backend; the first
// refusal in each proxyRateDiagnosticEvery window is reported as a diagnostic so
// a throttled page is not a silent failure.
func (ps *ProxyServer) rateLimited(next http.Handler) http.Handler {
	if ps.requestLimiter == nil {
		ps.requestLimiter = publish.NewRateLimiter(proxyRequestRatePerMinute, proxyRequestBurst, nil)
	}
	limiter := ps.requestLimiter
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter.Allow(publish.ShareIPKey("", r.RemoteAddr)) {
			next.ServeHTTP(w, r)
			return
		}
		now := time.Now()
		last := ps.rateDiagnosticAt.Load()
		if now.UnixNano()-last >= int64(proxyRateDiagnosticEvery) && ps.rateDiagnosticAt.CompareAndSwap(last, now.UnixNano()) {
			ps.logger.LogDiagnostic(ProxyDiagnostic{
				Timestamp: now,
				Level:     DiagnosticWarning,
				Category:  "proxy",
				Event:     "request_rate_capped",
				Message:   fmt.Sprintf("proxy %s is refusing requests from %s with 429: the per-client request-rate cap was exceeded", ps.ID, r.RemoteAddr),
				Method:    r.Method,
				Data:      map[string]any{"proxy_id": ps.ID, "status_code": http.StatusTooManyRequests},
			})
		}
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	})
}
