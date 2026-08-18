package config

// PublicPlaneConfig holds the anonymous-viewer public-plane REQUEST-rate limits
// that sit alongside the transport/connection caps (which are house constants in
// internal/httpcaps) and the per-POST body cap (FeedbackConfig.MaxBodyBytes).
// These are the two rate dimensions an operator may need to tune when a listener
// is tunnelled:
//
//   - Artifact: inbound GETs per (share, client IP). Throttles a single client
//     flooding one share.
//   - Outbound: guarded upstream fetches per upstream origin, ACROSS all shares.
//     This is the amplification bound INV-13 does not provide: INV-13 bounds
//     WHICH origins may be fetched, not HOW OFTEN, so N distinct shares pointed
//     at one origin would otherwise turn an anonymous GET flood into an outbound
//     flood at a third party. See the public-plane security spec / task
//     01KYT4EHBZQ2Z4JF86THHV7WAB.
//
// Per the repo config doctrine these are KDL settings (see KDLPublicPlane); JSON
// tags exist only for parity with the rest of Config.
type PublicPlaneConfig struct {
	// ArtifactRatePerMinute is the sustained artifact-GET budget per (share, IP).
	ArtifactRatePerMinute int `json:"artifact_rate_per_minute"`
	// ArtifactBurst is the token-bucket burst for artifact GETs per (share, IP).
	// A page load fans out to a few requests (artifact + variants.json +
	// walkthrough.json), so the burst must comfortably exceed one page's fan-out.
	ArtifactBurst int `json:"artifact_burst"`
	// OutboundRatePerMinute is the sustained guarded-fetch budget per upstream
	// origin, across every share that names that origin.
	OutboundRatePerMinute int `json:"outbound_rate_per_minute"`
	// OutboundBurst is the token-bucket burst for guarded fetches per origin.
	OutboundBurst int `json:"outbound_burst"`
}

// Public-plane rate defaults. Generous for a human viewer, a hard ceiling for a
// flood. The outbound bound is deliberately tighter than the inbound one because
// it protects a THIRD PARTY, not this daemon.
const (
	defaultPublicArtifactRatePerMinute = 120
	defaultPublicArtifactBurst         = 30
	defaultPublicOutboundRatePerMinute = 60
	defaultPublicOutboundBurst         = 20
)

// DefaultPublicPlaneConfig returns the house public-plane rate limits.
func DefaultPublicPlaneConfig() PublicPlaneConfig {
	return PublicPlaneConfig{
		ArtifactRatePerMinute: defaultPublicArtifactRatePerMinute,
		ArtifactBurst:         defaultPublicArtifactBurst,
		OutboundRatePerMinute: defaultPublicOutboundRatePerMinute,
		OutboundBurst:         defaultPublicOutboundBurst,
	}
}

// Normalize replaces any non-positive field with its house default so a partial
// or empty KDL block never yields a rate-0 limiter (which would refuse every
// request) — deny-by-default safety: a misconfigured value falls back to the
// safe house value rather than disabling the guard.
func (p PublicPlaneConfig) Normalize() PublicPlaneConfig {
	d := DefaultPublicPlaneConfig()
	if p.ArtifactRatePerMinute <= 0 {
		p.ArtifactRatePerMinute = d.ArtifactRatePerMinute
	}
	if p.ArtifactBurst <= 0 {
		p.ArtifactBurst = d.ArtifactBurst
	}
	if p.OutboundRatePerMinute <= 0 {
		p.OutboundRatePerMinute = d.OutboundRatePerMinute
	}
	if p.OutboundBurst <= 0 {
		p.OutboundBurst = d.OutboundBurst
	}
	return p
}

// KDLPublicPlane is the KDL binding for the public-plane rate block.
//
//	public-plane {
//	    artifact-rate-per-minute 120
//	    artifact-burst 30
//	    outbound-rate-per-minute 60
//	    outbound-burst 20
//	}
type KDLPublicPlane struct {
	ArtifactRatePerMinute int `kdl:"artifact-rate-per-minute"`
	ArtifactBurst         int `kdl:"artifact-burst"`
	OutboundRatePerMinute int `kdl:"outbound-rate-per-minute"`
	OutboundBurst         int `kdl:"outbound-burst"`
}

// toPublicPlaneConfig converts the KDL binding to a normalized PublicPlaneConfig,
// filling any omitted key with its house default.
func (k *KDLPublicPlane) toPublicPlaneConfig() PublicPlaneConfig {
	if k == nil {
		return DefaultPublicPlaneConfig()
	}
	return PublicPlaneConfig{
		ArtifactRatePerMinute: k.ArtifactRatePerMinute,
		ArtifactBurst:         k.ArtifactBurst,
		OutboundRatePerMinute: k.OutboundRatePerMinute,
		OutboundBurst:         k.OutboundBurst,
	}.Normalize()
}
