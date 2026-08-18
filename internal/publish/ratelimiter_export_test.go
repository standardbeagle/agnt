package publish

import (
	"testing"
	"time"
)

// This file reuses the deterministic, injectable fakeClock from
// feedback_store_test.go — no wall-clock sleeps: tests advance time explicitly
// and assert token accounting, never elapsed time.

// TestRateLimiter_TokenAccounting proves the exported RateLimiter spends exactly
// burst tokens before refusing, refills continuously against the INJECTED clock,
// and keys buckets independently — all with zero real sleeps.
func TestRateLimiter_TokenAccounting(t *testing.T) {
	clk := newFakeClock()
	const ratePerMin, burst = 60, 5 // 1 token/sec, capacity 5
	rl := NewRateLimiter(ratePerMin, burst, clk.now)

	// Burst is spent exactly: the first `burst` requests pass, the next fails.
	for i := 0; i < burst; i++ {
		if !rl.Allow("share-a") {
			t.Fatalf("request %d within burst was refused", i+1)
		}
	}
	if rl.Allow("share-a") {
		t.Fatalf("request %d exceeded burst %d but was allowed", burst+1, burst)
	}

	// No time passed: still refused (this is the ordering guarantee — refill is a
	// function of the clock, not of call count).
	if rl.Allow("share-a") {
		t.Fatalf("bucket refilled with no clock advance")
	}

	// Advance one refill period (1 token at 1/sec): exactly one more request.
	clk.advance(time.Second)
	if !rl.Allow("share-a") {
		t.Fatalf("one token should have refilled after 1s")
	}
	if rl.Allow("share-a") {
		t.Fatalf("only one token should have refilled; a second was granted")
	}

	// A different key is an independent full bucket.
	if !rl.Allow("share-b") {
		t.Fatalf("independent key should start full, was refused")
	}

	// Refill is capped at burst: a long idle does not accumulate beyond capacity.
	clk.advance(time.Hour)
	got := 0
	for rl.Allow("share-a") {
		got++
		if got > burst+1 {
			break
		}
	}
	if got != burst {
		t.Fatalf("refill exceeded burst cap: granted %d after long idle, want %d", got, burst)
	}
}

// TestShareIPKey_StripsPort pins that the artifact route and the feedback route
// key their buckets identically (share id + IP, port removed), so the two routes
// partition per (share, client) the same way.
func TestShareIPKey_StripsPort(t *testing.T) {
	if got, want := ShareIPKey("s1", "203.0.113.7:54321"), ShareIPKey("s1", "203.0.113.7:1"); got != want {
		t.Fatalf("keys must ignore the ephemeral port: %q vs %q", got, want)
	}
	if ShareIPKey("s1", "203.0.113.7:1") == ShareIPKey("s2", "203.0.113.7:1") {
		t.Fatalf("distinct shares must not share a bucket")
	}
	if ShareIPKey("s1", "203.0.113.7:1") == ShareIPKey("s1", "203.0.113.8:1") {
		t.Fatalf("distinct client IPs must not share a bucket")
	}
}
