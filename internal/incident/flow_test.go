package incident

import (
	"testing"
	"time"
)

func TestFlowController_CriticalAlwaysAllowed(t *testing.T) {
	t.Parallel()
	fc := NewFlowController(DefaultBucketConfigs)
	for i := 0; i < 1000; i++ {
		if !fc.TryPing(SeverityCritical) {
			t.Fatalf("critical ping denied at iteration %d", i)
		}
	}
}

func TestFlowController_ErrorBucketFillAndDrain(t *testing.T) {
	t.Parallel()
	cfg := map[Severity]BucketConfig{
		SeverityError: {Size: 5, RefillRate: 0}, // no refill — pure burst test
	}
	fc := NewFlowController(cfg)
	allowed := 0
	for i := 0; i < 10; i++ {
		if fc.TryPing(SeverityError) {
			allowed++
		}
	}
	if allowed != 5 {
		t.Errorf("allowed: got %d, want 5 (bucket size)", allowed)
	}
}

func TestFlowController_RefillOverTime(t *testing.T) {
	t.Parallel()
	// 2 tokens, refill 100/s → after 50ms we should have ~5 new tokens.
	cfg := map[Severity]BucketConfig{
		SeverityError: {Size: 10, RefillRate: 100},
	}
	fc := NewFlowController(cfg)
	// Drain the bucket.
	for i := 0; i < 10; i++ {
		fc.TryPing(SeverityError)
	}
	// After 60ms at 100/s, 6 tokens should be available (capped at 10).
	time.Sleep(60 * time.Millisecond)
	allowed := 0
	for i := 0; i < 10; i++ {
		if fc.TryPing(SeverityError) {
			allowed++
		}
	}
	if allowed < 5 {
		t.Errorf("refill: allowed=%d, want ≥5 after 60ms at 100/s", allowed)
	}
}

func TestFlowController_WarningThrottled(t *testing.T) {
	t.Parallel()
	// 5 token warning bucket, no refill for burst test.
	cfg := map[Severity]BucketConfig{
		SeverityWarning: {Size: 5, RefillRate: 0},
	}
	fc := NewFlowController(cfg)
	const n = 100
	allowed := 0
	for i := 0; i < n; i++ {
		if fc.TryPing(SeverityWarning) {
			allowed++
		}
	}
	if allowed > 5 {
		t.Errorf("warning throttle: %d pings allowed, want ≤5", allowed)
	}
}

func TestFlowController_InfoThrottledMoreThanWarning(t *testing.T) {
	t.Parallel()
	fc := NewFlowController(DefaultBucketConfigs)
	// Drain both buckets simultaneously.
	infoAllowed, warnAllowed := 0, 0
	for i := 0; i < 50; i++ {
		if fc.TryPing(SeverityInfo) {
			infoAllowed++
		}
		if fc.TryPing(SeverityWarning) {
			warnAllowed++
		}
	}
	if infoAllowed > warnAllowed {
		t.Errorf("info bucket (%d) should be tighter than warning (%d)", infoAllowed, warnAllowed)
	}
}

func TestFlowController_UnconfiguredSeverityAllowed(t *testing.T) {
	t.Parallel()
	fc := NewFlowController(map[Severity]BucketConfig{}) // no buckets
	for i := 0; i < 100; i++ {
		if !fc.TryPing(SeverityError) {
			t.Fatalf("unconfigured severity should be allowed, denied at %d", i)
		}
	}
}
