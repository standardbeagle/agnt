package config

// FeedbackConfig holds the public-plane anonymous feedback ingestion limits
// (P8 of the walkthrough-publish epic). The values encode the P1 security spec
// (docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md §5)
// so the durable feedback sink is rate-limited, size-capped, and
// retention-bounded. Per the repo config doctrine these are KDL settings (see
// KDLFeedback); JSON tags exist only for parity with the rest of Config and are
// not a supported load surface.
type FeedbackConfig struct {
	// RatePerMinute is the sustained feedback POST budget per (share token, IP)
	// (§5: "Feedback rate = 10 req/min per (token,IP)").
	RatePerMinute int `json:"rate_per_minute"`
	// Burst is the token-bucket burst allowance per (share token, IP)
	// (§5: "burst 5").
	Burst int `json:"burst"`
	// MaxBodyBytes caps a single feedback POST body
	// (§5: "Max feedback body = 4096 bytes").
	MaxBodyBytes int `json:"max_body_bytes"`
	// MaxRowsPerShare is the retention ring depth per share
	// (§5: "Feedback retention = 500 rows per share ... oldest evicted").
	MaxRowsPerShare int `json:"max_rows_per_share"`
	// RetentionDays evicts rows older than this many days
	// (§5: "... OR 90 days, whichever first").
	RetentionDays int `json:"retention_days"`
}

// Default feedback limits, verbatim from spec §5.
const (
	defaultFeedbackRatePerMinute   = 10
	defaultFeedbackBurst           = 5
	defaultFeedbackMaxBodyBytes    = 4096
	defaultFeedbackMaxRowsPerShare = 500
	defaultFeedbackRetentionDays   = 90
)

// DefaultFeedbackConfig returns the spec §5 feedback limits.
func DefaultFeedbackConfig() FeedbackConfig {
	return FeedbackConfig{
		RatePerMinute:   defaultFeedbackRatePerMinute,
		Burst:           defaultFeedbackBurst,
		MaxBodyBytes:    defaultFeedbackMaxBodyBytes,
		MaxRowsPerShare: defaultFeedbackMaxRowsPerShare,
		RetentionDays:   defaultFeedbackRetentionDays,
	}
}

// Normalize replaces any non-positive field with its spec default so a partial
// or empty KDL block never yields an unbounded (rate 0 = never allow, or cap 0 =
// reject everything) limiter. Deny-by-default safety: a misconfigured value
// falls back to the safe spec value rather than disabling a guard.
func (f FeedbackConfig) Normalize() FeedbackConfig {
	d := DefaultFeedbackConfig()
	if f.RatePerMinute <= 0 {
		f.RatePerMinute = d.RatePerMinute
	}
	if f.Burst <= 0 {
		f.Burst = d.Burst
	}
	if f.MaxBodyBytes <= 0 {
		f.MaxBodyBytes = d.MaxBodyBytes
	}
	if f.MaxRowsPerShare <= 0 {
		f.MaxRowsPerShare = d.MaxRowsPerShare
	}
	if f.RetentionDays <= 0 {
		f.RetentionDays = d.RetentionDays
	}
	return f
}

// KDLFeedback is the KDL binding for the feedback block (app settings are KDL per
// the repo config doctrine).
//
//	feedback {
//	    rate-per-minute 10
//	    burst 5
//	    max-body-bytes 4096
//	    max-rows-per-share 500
//	    retention-days 90
//	}
type KDLFeedback struct {
	RatePerMinute   int `kdl:"rate-per-minute"`
	Burst           int `kdl:"burst"`
	MaxBodyBytes    int `kdl:"max-body-bytes"`
	MaxRowsPerShare int `kdl:"max-rows-per-share"`
	RetentionDays   int `kdl:"retention-days"`
}

// toFeedbackConfig converts the KDL binding to a normalized FeedbackConfig,
// filling any omitted key with its spec default.
func (k *KDLFeedback) toFeedbackConfig() FeedbackConfig {
	if k == nil {
		return DefaultFeedbackConfig()
	}
	return FeedbackConfig{
		RatePerMinute:   k.RatePerMinute,
		Burst:           k.Burst,
		MaxBodyBytes:    k.MaxBodyBytes,
		MaxRowsPerShare: k.MaxRowsPerShare,
		RetentionDays:   k.RetentionDays,
	}.Normalize()
}
