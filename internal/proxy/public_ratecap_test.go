package proxy

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/publish"
)

// manualClock is a deterministic, injectable clock for the rate-cap tests — no
// wall-clock sleeps: the tests advance time explicitly and assert token
// accounting, never elapsed time (the binding message-passing test preference).
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock { return &manualClock{t: time.Unix(1_700_000_000, 0)} }

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestArtifactRoute_RateCapReturns429AtLimit is the inbound-rate teeth test.
// Exactly `burst` artifact GETs succeed; the next returns 429; refill against the
// INJECTED clock grants exactly one more. Token accounting is asserted, not time.
//
// MUTATION PROOF: remove the cap (drop the WithRateLimits call, or make the
// limiter allow-all) and this test FAILS — the (burst+1)th GET returns 200
// instead of 429.
func TestArtifactRoute_RateCapReturns429AtLimit(t *testing.T) {
	const burst = 3
	clk := newManualClock()
	h := NewPublicHandler(&fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: "share-1"}, nil, 0).
		WithRateLimits(publish.NewRateLimiter(60, burst, clk.now), nil) // 1 token/sec

	target := sharePrefix + validToken
	for i := 0; i < burst; i++ {
		if w := do(h, http.MethodGet, target, "", nil); w.Code != http.StatusOK {
			t.Fatalf("GET %d within burst: got %d, want 200", i+1, w.Code)
		}
	}
	// (burst+1)th: refused.
	w := do(h, http.MethodGet, target, "", nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("GET %d over burst %d: got %d, want 429", burst+1, burst, w.Code)
	}
	// 429 must not leak the share id or token (INV-9 / Secret Hygiene).
	if body := w.Body.String(); strings.Contains(body, "share-1") || strings.Contains(body, validToken) {
		t.Fatalf("429 body leaked an identifier: %q", body)
	}

	// No clock advance: still refused (refill is a function of the clock, not the
	// call count).
	if w := do(h, http.MethodGet, target, "", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("bucket refilled with no clock advance: got %d, want 429", w.Code)
	}

	// Advance exactly one refill period → exactly one more GET.
	clk.advance(time.Second)
	if w := do(h, http.MethodGet, target, "", nil); w.Code != http.StatusOK {
		t.Fatalf("one token should have refilled after 1s: got %d, want 200", w.Code)
	}
	if w := do(h, http.MethodGet, target, "", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("only one token should have refilled; a second GET was served: got %d, want 429", w.Code)
	}
}

// TestArtifactRoute_OutboundPerOriginCapRefusesBeforeFetch is the outbound
// amplification teeth test. The per-origin cap must refuse the (burst+1)th
// upstream-bearing request BEFORE the guarded fetch opens a socket — asserting
// the dangerous side effect (the outbound fetch) is never reached, not merely
// that the response was non-200 (publish-security-review-lessons §8).
//
// MUTATION PROOF: remove the outbound cap and this test FAILS — the fetcher is
// called a (burst+1)th time, so the calls count and the 200 assertion both trip.
func TestArtifactRoute_OutboundPerOriginCapRefusesBeforeFetch(t *testing.T) {
	const burst = 2
	clk := newManualClock()
	fetcher := &countingFetcher{body: []byte(upstreamDoc)}
	h := upstreamHandler(t, upstreamRevision("https://app.example.com/live"), fetcher)
	// Generous inbound cap so the artifact limiter never fires first; tight
	// outbound cap so this test exercises the per-origin bound.
	h.WithRateLimits(publish.NewRateLimiter(6000, 1000, clk.now), publish.NewRateLimiter(60, burst, clk.now))

	target := sharePrefix + validToken
	for i := 0; i < burst; i++ {
		if w := do(h, http.MethodGet, target, "", nil); w.Code != http.StatusOK {
			t.Fatalf("proxied GET %d within outbound burst: got %d, want 200", i+1, w.Code)
		}
	}
	if fetcher.calls != burst {
		t.Fatalf("expected exactly %d guarded fetches, got %d", burst, fetcher.calls)
	}

	// (burst+1)th: the outbound bucket is empty → refused, and crucially the
	// fetcher is NOT invoked (no outbound socket).
	w := do(h, http.MethodGet, target, "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap proxied GET: got %d, want 503", w.Code)
	}
	if fetcher.calls != burst {
		t.Fatalf("outbound cap did not fire BEFORE the fetch: fetcher called %d times, want %d "+
			"(this is where the test fails with the cap removed)", fetcher.calls, burst)
	}
	if body := w.Body.String(); strings.Contains(body, "share-1") || strings.Contains(body, validToken) {
		t.Fatalf("503 body leaked an identifier: %q", body)
	}

	// Refill one token → exactly one more guarded fetch reaches the origin.
	clk.advance(time.Second)
	if w := do(h, http.MethodGet, target, "", nil); w.Code != http.StatusOK {
		t.Fatalf("outbound token should have refilled after 1s: got %d, want 200", w.Code)
	}
	if fetcher.calls != burst+1 {
		t.Fatalf("refilled outbound token did not admit exactly one fetch: calls=%d, want %d", fetcher.calls, burst+1)
	}
}

// TestUpstreamOriginKey_CollapsesByOrigin pins that the outbound bucket is keyed
// by origin (scheme+host, case-insensitive), so many shares at one origin share
// a bucket while distinct origins do not.
func TestUpstreamOriginKey_CollapsesByOrigin(t *testing.T) {
	same := []string{"https://App.Example.com/a", "https://app.example.com/b?q=1"}
	if upstreamOriginKey(same[0]) != upstreamOriginKey(same[1]) {
		t.Fatalf("same origin, different path/case must share a bucket: %q vs %q",
			upstreamOriginKey(same[0]), upstreamOriginKey(same[1]))
	}
	if upstreamOriginKey("https://app.example.com/a") == upstreamOriginKey("https://other.example.com/a") {
		t.Fatalf("distinct origins must not share a bucket")
	}
	if upstreamOriginKey("https://app.example.com/a") == upstreamOriginKey("https://app.example.com:8443/a") {
		t.Fatalf("distinct ports are distinct origins")
	}
}
