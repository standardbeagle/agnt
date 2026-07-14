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
type tokenBucketLimiter struct {
	rate  float64 // tokens per second (RatePerMinute / 60)
	burst float64 // bucket capacity
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

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
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}
