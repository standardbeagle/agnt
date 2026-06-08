// Package license implements offline, self-hosted license validation for
// agnt Pro features. Licenses are hyperboloide/lk blobs: an ECDSA (P-384)
// signature over a JSON [Payload]. The signing private key never ships; the
// binary embeds only the public key (see key.go) and validates fully offline.
package license

import (
	"encoding/json"
	"time"
)

// Payload is the JSON document carried inside a signed license blob. It is the
// single source of truth for who a license is for, when it expires, and which
// Pro capabilities it grants. The signing server (Stripe webhook → issue.Mint)
// builds this from the customer record; the client only ever reads it.
type Payload struct {
	// Email identifies the licensee. Informational only — not an auth boundary.
	Email string `json:"email"`

	// CustomerID is the upstream (Stripe) customer reference, for support and
	// re-issue. Informational only.
	CustomerID string `json:"customer_id,omitempty"`

	// Plan is a human label for the tier (e.g. "team", "enterprise").
	Plan string `json:"plan,omitempty"`

	// IssuedAt records when the license was minted (UTC).
	IssuedAt time.Time `json:"issued_at"`

	// Expiry is when the paid term ends (UTC). After this, the license enters
	// the grace window (see Evaluate) before Pro features hard-block.
	Expiry time.Time `json:"expiry"`

	// Capabilities lists the Pro capability keys this license grants. A Check
	// for a capability not present here fails even on a fully valid license.
	Capabilities []string `json:"capabilities,omitempty"`
}

// Grants reports whether the payload authorizes the given capability.
func (p *Payload) Grants(cap Capability) bool {
	for _, c := range p.Capabilities {
		if c == string(cap) {
			return true
		}
	}
	return false
}

// marshal encodes the payload to the JSON bytes signed by lk.
func (p *Payload) marshal() ([]byte, error) {
	return json.Marshal(p)
}

// unmarshalPayload decodes the JSON bytes carried in a license blob.
func unmarshalPayload(b []byte) (*Payload, error) {
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
