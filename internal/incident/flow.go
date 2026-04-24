package incident

import (
	"sync"
	"time"
)

// BucketConfig configures a token bucket for one severity level.
type BucketConfig struct {
	Size       float64 // maximum tokens (burst capacity)
	RefillRate float64 // tokens added per second
}

// DefaultBucketConfigs are the production token-bucket parameters.
// Critical severity is not listed — it is always allowed (infinite bucket).
var DefaultBucketConfigs = map[Severity]BucketConfig{
	SeverityError:   {Size: 20, RefillRate: 10},
	SeverityWarning: {Size: 10, RefillRate: 5},
	SeverityInfo:    {Size: 5, RefillRate: 2},
}

type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
}

func newTokenBucket(cfg BucketConfig) *tokenBucket {
	return &tokenBucket{
		tokens:     cfg.Size,
		maxTokens:  cfg.Size,
		refillRate: cfg.RefillRate,
		lastRefill: time.Now(),
	}
}

// tryConsume refills based on elapsed time, then tries to consume one token.
// Returns true if the token was consumed.
func (b *tokenBucket) tryConsume() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens = min(b.maxTokens, b.tokens+elapsed*b.refillRate)
	b.lastRefill = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// FlowController is a per-severity token bucket that rate-limits ping emissions.
// Critical severity is never throttled. Over-budget events should still be
// inboxed — only their ping emission is suppressed.
type FlowController struct {
	buckets map[Severity]*tokenBucket
}

// NewFlowController creates a FlowController using the provided per-severity
// configs. Any severity not in configs (including SeverityCritical) is unthrottled.
func NewFlowController(configs map[Severity]BucketConfig) *FlowController {
	fc := &FlowController{
		buckets: make(map[Severity]*tokenBucket, len(configs)),
	}
	for sev, cfg := range configs {
		fc.buckets[sev] = newTokenBucket(cfg)
	}
	return fc
}

// TryPing returns true if a ping for the given severity is allowed.
// Critical always returns true. Other severities consume a token; if the
// bucket is empty TryPing returns false (caller should inbox but not ping).
func (fc *FlowController) TryPing(sev Severity) bool {
	if sev == SeverityCritical {
		return true
	}
	b, ok := fc.buckets[sev]
	if !ok {
		return true // unconfigured severity: allow
	}
	return b.tryConsume()
}
