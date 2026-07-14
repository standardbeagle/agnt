package publish

import (
	"sync"
	"time"
)

// tokenBucketLimiter is a per-key token-bucket rate limiter for the anonymous
// public feedback plane (spec §5/§7: "10 req/min per (token,IP), burst 5").
//
// # Concurrency
//
// The public plane is concurrent by definition (many anonymous viewers). Every
// bucket read-modify-write happens under a single mutex, so there are no lost
// updates and no double-spend under parallel posts — the -race feedback test
// pins this. A sharded/lock-free design is not warranted at this rate.
//
// # Clock
//
// now is injected so tests advance time deterministically (no wall-clock sleeps).
// Refill is continuous: tokens accrue at rate tokens/second, capped at burst.
//
// # Bounded memory (DoS resistance)
//
// buckets holds one entry per (share, real client IP). Left unbounded, an
// attacker rotating source IPs (e.g. an IPv6 /64) would create entries forever
// and exhaust daemon memory — defeating the limiter. So allow() opportunistically
// reaps once the map crosses reapSoftCap: any bucket that would be FULL at the
// current (injected) clock is deleted, because a returning client simply gets a
// fresh full bucket — identical to what a full bucket would have granted, so no
// limit is relaxed. A currently-throttling bucket (which refills to < burst) is
// never dropped, so its limit still applies. reapHardCap is an absolute ceiling:
// above it, only NON-throttling buckets (refilled >= 1 token) are evicted, so a
// depleted bucket is still never discarded.
type tokenBucketLimiter struct {
	rate  float64 // tokens per second (RatePerMinute / 60)
	burst float64 // bucket capacity
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

// Bucket-map bounds. reapSoftCap triggers the cheap "drop full buckets" sweep;
// reapHardCap is the absolute ceiling that additionally sheds non-throttling
// buckets. Both are amortized over allow() calls — a sweep only fires when the
// map is over the soft cap.
const (
	reapSoftCap = 4096
	reapHardCap = 16384
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// newTokenBucketLimiter builds a limiter from a per-minute rate and a burst. now
// may be nil (defaults to time.Now).
func newTokenBucketLimiter(ratePerMinute, burst int, now func() time.Time) *tokenBucketLimiter {
	if now == nil {
		now = time.Now
	}
	return &tokenBucketLimiter{
		rate:    float64(ratePerMinute) / 60.0,
		burst:   float64(burst),
		now:     now,
		buckets: make(map[string]*tokenBucket),
	}
}

// allow reports whether a request keyed by key may proceed, consuming one token
// if so. A fresh key starts full (burst) so a first-time viewer is never
// throttled. Concurrency-safe.
func (l *tokenBucketLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	t := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: l.burst, last: t}
		l.buckets[key] = b
	} else {
		// Continuous refill since the last observation, clamped to burst.
		elapsed := t.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.rate
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
			b.last = t
		}
	}
	allowed := false
	if b.tokens >= 1 {
		b.tokens -= 1
		allowed = true
	}

	// Reap opportunistically: only walk the map once it has grown past the soft
	// cap, so the common path stays O(1). The bucket just touched above is never
	// full (it just spent or lacked a token), so it is never reaped here.
	if len(l.buckets) > reapSoftCap {
		l.reap(t)
	}
	return allowed
}

// reap bounds the bucket map. Caller holds l.mu.
//
// Pass 1 (always) deletes every bucket that would be FULL at now: recreating it
// fresh on the client's next request yields the same burst, so dropping it
// relaxes no limit. A currently-throttling bucket refills to < burst and is kept.
//
// Pass 2 (only over the hard ceiling) sheds additional buckets that are NOT
// currently throttling (refilled >= 1 token — the client would be allowed right
// now anyway). A depleted bucket (refilled < 1) is never evicted, so an active
// limit is always preserved even under a massive distributed flood.
func (l *tokenBucketLimiter) reap(now time.Time) {
	for k, b := range l.buckets {
		if l.refilled(b, now) >= l.burst {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) <= reapHardCap {
		return
	}
	for k, b := range l.buckets {
		if len(l.buckets) <= reapHardCap {
			break
		}
		if l.refilled(b, now) >= 1 {
			delete(l.buckets, k)
		}
	}
}

// refilled returns the token count b would hold at now, clamped to burst,
// without mutating b (reap must not consume the client's stored penalty).
func (l *tokenBucketLimiter) refilled(b *tokenBucket, now time.Time) float64 {
	tokens := b.tokens
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		tokens += elapsed * l.rate
	}
	if tokens > l.burst {
		tokens = l.burst
	}
	return tokens
}

// size returns the current bucket count (test observability for the memory
// bound). Concurrency-safe.
func (l *tokenBucketLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
