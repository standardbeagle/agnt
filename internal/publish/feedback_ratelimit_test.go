package publish

import (
	"fmt"
	"testing"
	"time"
)

// TestLimiterReapsFullBucketsAndKeepsThrottling proves the memory bound: after
// far more than reapSoftCap distinct keys are seen, idle-FULL buckets are reaped
// so the map does not grow without bound, while a currently-throttling
// (depleted) bucket is never dropped so its limit still applies.
func TestLimiterReapsFullBucketsAndKeepsThrottling(t *testing.T) {
	clk := newFakeClock()
	// rate 60/min = 1 tok/sec, burst 5. A bucket refills to full 5s after its
	// last request.
	l := newTokenBucketLimiter(60, 5, clk.now)

	// t0: create a wave of filler buckets (each consumes one token, leaving 4).
	fillers := reapSoftCap + 2000 // comfortably over the soft cap
	for i := 0; i < fillers; i++ {
		l.allow(fmt.Sprintf("share\x00filler-%d", i))
	}
	if l.size() != fillers {
		t.Fatalf("before reap: size = %d, want %d", l.size(), fillers)
	}

	// Advance 5s so every filler refills to burst (full → reapable).
	clk.advance(5 * time.Second)

	// A victim seen at the LATER instant, driven to depletion: it is actively
	// throttling and must survive reaping.
	victim := "share\x00victim"
	for i := 0; i < 5; i++ {
		l.allow(victim) // burst 5 → drains to ~0
	}
	if l.allow(victim) {
		t.Fatal("victim should be throttled after draining its burst")
	}

	// Draining the victim happened while the map was over the soft cap, so reap
	// already fired and dropped the now-full fillers. The map must have collapsed
	// to a small bounded set, and the victim must still be present + throttling.
	if l.size() > reapSoftCap {
		t.Fatalf("full idle buckets were not reaped: size = %d", l.size())
	}
	if l.allow(victim) {
		t.Fatal("victim bucket was dropped by reap — an active limit was relaxed")
	}
}

// TestLimiterFreshKeyAfterReapIsFull confirms the reap invariant is safe: a
// client whose FULL bucket was reaped is recreated fresh (also full), so it is
// never over-throttled by the eviction.
func TestLimiterFreshKeyAfterReapIsFull(t *testing.T) {
	clk := newFakeClock()
	l := newTokenBucketLimiter(60, 5, clk.now)

	// Fill the map past the soft cap with full-then-idle buckets.
	for i := 0; i < reapSoftCap+1; i++ {
		l.allow(fmt.Sprintf("k-%d", i))
	}
	clk.advance(5 * time.Second) // all refill to full
	l.allow("trigger")           // over soft cap → reap drops the full buckets
	if l.size() > reapSoftCap {
		t.Fatalf("reap did not bound the map: size = %d", l.size())
	}

	// A previously-reaped key returns and gets a fresh full burst — identical to
	// what its reaped full bucket would have granted, so no limit was relaxed.
	for i := 0; i < 5; i++ {
		if !l.allow("k-0") {
			t.Fatalf("reaped key should start full; denied at burst slot %d", i)
		}
	}
	if l.allow("k-0") {
		t.Fatal("6th request should be throttled")
	}
}
