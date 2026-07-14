package config

import "testing"

// TestDefaultFeedbackConfigMatchesSpec pins the spec §5 verbatim numbers.
func TestDefaultFeedbackConfigMatchesSpec(t *testing.T) {
	d := DefaultFeedbackConfig()
	if d.RatePerMinute != 10 || d.Burst != 5 {
		t.Fatalf("rate/burst = %d/%d, want 10/5", d.RatePerMinute, d.Burst)
	}
	if d.MaxBodyBytes != 4096 {
		t.Fatalf("max body = %d, want 4096", d.MaxBodyBytes)
	}
	if d.MaxRowsPerShare != 500 || d.RetentionDays != 90 {
		t.Fatalf("retention = %d rows / %d days, want 500/90", d.MaxRowsPerShare, d.RetentionDays)
	}
}

// TestFeedbackConfigNormalizeFillsZeros asserts a partial config never disables a
// guard: any non-positive field falls back to the safe spec default.
func TestFeedbackConfigNormalizeFillsZeros(t *testing.T) {
	got := FeedbackConfig{RatePerMinute: 3}.Normalize()
	if got.RatePerMinute != 3 {
		t.Fatalf("explicit rate lost: %d", got.RatePerMinute)
	}
	if got.Burst != 5 || got.MaxBodyBytes != 4096 || got.MaxRowsPerShare != 500 || got.RetentionDays != 90 {
		t.Fatalf("zero fields not filled with defaults: %+v", got)
	}
}

// TestParseKDLFeedbackBlock asserts the feedback KDL block binds onto Config.
func TestParseKDLFeedbackBlock(t *testing.T) {
	kdl := `
version "1.0"
feedback {
    rate-per-minute 20
    burst 8
    max-body-bytes 2048
    max-rows-per-share 250
    retention-days 30
}
`
	cfg, err := ParseKDLConfig(kdl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	f := cfg.Feedback
	if f.RatePerMinute != 20 || f.Burst != 8 || f.MaxBodyBytes != 2048 || f.MaxRowsPerShare != 250 || f.RetentionDays != 30 {
		t.Fatalf("feedback block not bound: %+v", f)
	}
}

// TestParseKDLFeedbackAbsentKeepsDefaults asserts an absent feedback block yields
// the spec defaults rather than a zero (guard-disabling) config.
func TestParseKDLFeedbackAbsentKeepsDefaults(t *testing.T) {
	cfg, err := ParseKDLConfig(`version "1.0"`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Feedback != DefaultFeedbackConfig() {
		t.Fatalf("absent feedback block did not default: %+v", cfg.Feedback)
	}
}
