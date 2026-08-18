package config

import "testing"

// TestParseKDLPublicPlaneBlock asserts the public-plane KDL block binds onto
// Config — i.e. a non-default value actually reaches the parsed config rather
// than being parsed and ignored (publish-security-review-lessons §5).
func TestParseKDLPublicPlaneBlock(t *testing.T) {
	kdl := `
version "1.0"
public-plane {
    artifact-rate-per-minute 45
    artifact-burst 12
    outbound-rate-per-minute 15
    outbound-burst 4
}
`
	cfg, err := ParseKDLConfig(kdl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := cfg.PublicPlane
	if p.ArtifactRatePerMinute != 45 || p.ArtifactBurst != 12 ||
		p.OutboundRatePerMinute != 15 || p.OutboundBurst != 4 {
		t.Fatalf("public-plane block not bound: %+v", p)
	}
	// The non-default values must differ from the house defaults, or this test
	// would pass vacuously.
	if p == DefaultPublicPlaneConfig() {
		t.Fatalf("test premise broken: chosen values equal the defaults")
	}
}

// TestParseKDLPublicPlaneAbsentKeepsDefaults asserts an absent block yields the
// house defaults, never a rate-0 (guard-disabling) config.
func TestParseKDLPublicPlaneAbsentKeepsDefaults(t *testing.T) {
	cfg, err := ParseKDLConfig(`version "1.0"`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.PublicPlane != DefaultPublicPlaneConfig() {
		t.Fatalf("absent public-plane block did not default: %+v", cfg.PublicPlane)
	}
}

// TestPublicPlaneNormalizeFillsZeros asserts a partial block never disables a
// guard — a zero/negative field falls back to its house default.
func TestPublicPlaneNormalizeFillsZeros(t *testing.T) {
	got := PublicPlaneConfig{ArtifactRatePerMinute: 200}.Normalize()
	d := DefaultPublicPlaneConfig()
	if got.ArtifactRatePerMinute != 200 {
		t.Fatalf("explicit value overwritten: %d", got.ArtifactRatePerMinute)
	}
	if got.ArtifactBurst != d.ArtifactBurst || got.OutboundRatePerMinute != d.OutboundRatePerMinute || got.OutboundBurst != d.OutboundBurst {
		t.Fatalf("zero fields not filled with defaults: %+v", got)
	}
}
