package publish

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// subresource.go carries the reference-binding primitive for the public plane's
// subresource route (design spec 2026-08-19 §4a, INV-16).
//
// The threat it closes: a route shaped /s/{token}/sub?u=<url> lets ANY anonymous
// token holder drive daemon-side fetches at arbitrary URLs — an open relay
// bounded only by the SSRF deny-list, which contradicts the keystone premise
// that every fetched origin is PUBLISHER-named. Binding fixes that: the daemon
// signs only the references it derived itself from a guarded fetch of that
// share's own upstream, so a viewer can replay those and mint none.
//
// The MAC is keyed per-daemon and lives only in memory. It is deliberately NOT
// persisted: a restart invalidates outstanding subresource URLs, which fails
// CLOSED (the stale URL 404s, the artifact is revalidated — Cache-Control
// max-age=0, must-revalidate — and the fresh document carries freshly signed
// references). Persisting it would add a long-lived secret on disk for no
// correctness gain.

// SubresourceSigner mints and verifies the MAC that binds a subresource URL to
// one share and one nesting depth. The zero value is unusable; construct with
// NewSubresourceSigner. Safe for concurrent use — the key is immutable.
type SubresourceSigner struct {
	key []byte
}

// NewSubresourceSigner generates a fresh per-daemon MAC key from the CSPRNG. An
// error is fatal for the subresource route rather than degradable: a caller that
// cannot obtain a key must refuse to sign AND refuse to serve, never fall back to
// an unbound route.
func NewSubresourceSigner() (*SubresourceSigner, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("subresource signer: key generation failed: %w", err)
	}
	return &SubresourceSigner{key: key}, nil
}

// Sign returns the base64url MAC binding absURL to shareID at depth. A nil
// signer returns "" so an unsigned reference can never be mistaken for a signed
// one (Verify rejects "" unconditionally).
//
// shareID is in the MAC because revoke and cross-share replay are the same
// question: a URL signed for share A must not resolve under share B's token even
// while both are live. depth is in the MAC because the nesting cap is a security
// bound, not a hint — an unbound depth would let a viewer replay a document-level
// reference as if it had come from a CSS file and walk the chain deeper than
// §4d permits.
func (s *SubresourceSigner) Sign(shareID, absURL string, depth int) string {
	if s == nil {
		return ""
	}
	mac := hmac.New(sha256.New, s.key)
	writeMACField(mac, shareID)
	writeMACField(mac, absURL)
	writeMACField(mac, strconv.Itoa(depth))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify constant-time checks a MAC against the tuple it must be bound to. A nil
// signer, an empty signature, or any mismatch returns false — there is no branch
// that admits an unverified reference.
func (s *SubresourceSigner) Verify(shareID, absURL string, depth int, sig string) bool {
	if s == nil || sig == "" {
		return false
	}
	want := s.Sign(shareID, absURL, depth)
	// hmac.Equal is constant time for equal-length inputs and leaks only length,
	// which is fixed here (both sides are a base64url SHA-256 digest).
	return hmac.Equal([]byte(want), []byte(sig))
}

// writeMACField writes a length-prefixed field so distinct tuples cannot collide
// by concatenation: without the prefix, ("ab", "c") and ("a", "bc") would MAC
// identically, letting a URL signed for one share verify under another whose id
// is a prefix-shifted variant.
func writeMACField(w interface{ Write([]byte) (int, error) }, field string) {
	var b strings.Builder
	b.WriteString(strconv.Itoa(len(field)))
	b.WriteByte(':')
	b.WriteString(field)
	_, _ = w.Write([]byte(b.String()))
}
