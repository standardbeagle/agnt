package proxy

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// AccessJWTHeader is the header Cloudflare Access attaches to every request
// it lets through to the origin, WebSocket upgrades included.
const AccessJWTHeader = "Cf-Access-Jwt-Assertion"

const (
	// accessClockLeeway absorbs clock skew between Cloudflare's edge and this
	// machine when checking exp/nbf.
	accessClockLeeway = 30 * time.Second
	// accessMinRefresh bounds how often an unknown key id may trigger a JWKS
	// fetch, so a stream of forged tokens cannot turn the verifier into a
	// request amplifier against the team's certs endpoint.
	accessMinRefresh   = 30 * time.Second
	accessFetchTimeout = 5 * time.Second
	accessMaxJWKSBytes = 64 << 10
	// accessDenyLogInterval bounds how often one denial reason is surfaced as
	// a diagnostic; a misconfigured AUD would otherwise log every request.
	accessDenyLogInterval = time.Minute
)

// Denial reasons. Each one names the check that refused the request, so a
// test (or an operator reading a diagnostic) can attribute a 403 to the
// control that produced it rather than to an incidental failure.
var (
	ErrAccessMissingToken = errors.New("access: no " + AccessJWTHeader + " header")
	ErrAccessMalformed    = errors.New("access: malformed token")
	ErrAccessAlgorithm    = errors.New("access: algorithm is not RS256")
	ErrAccessUnknownKey   = errors.New("access: signing key not in team JWKS")
	ErrAccessSignature    = errors.New("access: signature does not verify")
	ErrAccessIssuer       = errors.New("access: issuer is not the configured team")
	ErrAccessAudience     = errors.New("access: audience does not include the configured AUD")
	ErrAccessExpired      = errors.New("access: token expired")
	ErrAccessNotYetValid  = errors.New("access: token not yet valid")
)

// CloudflareAccess verifies Cloudflare Access application tokens. Access is
// the authentication layer for a named tunnel; this is the origin-side check
// that makes a missing or misconfigured Access application fail closed
// instead of serving the dev proxy to anyone who reaches the hostname.
type CloudflareAccess struct {
	teamDomain string
	aud        string
	issuer     string

	// certsURL, client and now are seams. Their production values are set by
	// NewCloudflareAccess; tests in this package replace them.
	certsURL string
	client   *http.Client
	now      func() time.Time

	keys      atomic.Pointer[map[string]*rsa.PublicKey]
	lastFetch atomic.Int64 // unix nanos of the last fetch attempt
	refreshMu sync.Mutex   // coalesces concurrent fetches into one

	// onDeny, when set, receives a denial at most once per reason per
	// accessDenyLogInterval.
	onDeny     func(err error)
	denyLogged sync.Map // reason string -> *atomic.Int64 (unix nanos)
}

// NewCloudflareAccess validates the team domain and application audience and
// returns a verifier. The team domain must be the Access team hostname
// (<team>.cloudflareaccess.com): the verifier fetches signing keys from it,
// so it is never allowed to name an arbitrary host.
func NewCloudflareAccess(teamDomain, aud string) (*CloudflareAccess, error) {
	teamDomain, err := config.NormalizeAccessTeamDomain(teamDomain)
	if err != nil {
		return nil, err
	}
	aud = strings.TrimSpace(aud)
	if aud == "" {
		return nil, errors.New("access aud is required (the Access application's Audience tag)")
	}
	return &CloudflareAccess{
		teamDomain: teamDomain,
		aud:        aud,
		issuer:     "https://" + teamDomain,
		certsURL:   "https://" + teamDomain + "/cdn-cgi/access/certs",
		client:     &http.Client{Timeout: accessFetchTimeout},
		now:        time.Now,
	}, nil
}

// SetOnDeny installs the rate-limited denial observer. Call before serving.
func (a *CloudflareAccess) SetOnDeny(fn func(err error)) { a.onDeny = fn }

// Guard wraps next so that every request must carry a valid Access token.
// A refused request gets a constant 403 body: the reason goes to the denial
// observer, never to the client.
func (a *CloudflareAccess) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.verify(r.Header.Get(AccessJWTHeader))
		if err != nil {
			a.reportDeny(err)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessIdentityKey{}, id)))
	})
}

// Verify checks one token: RS256 only, signed by a key from the team JWKS,
// issued by the team, addressed to the configured audience, inside its
// validity window.
func (a *CloudflareAccess) Verify(token string) error {
	_, err := a.verify(token)
	return err
}

// AccessIdentity is who a verified Access token says the caller is. Email is
// empty for service tokens, which carry no user.
type AccessIdentity struct {
	Email string
}

type accessIdentityKey struct{}

// AccessIdentityFrom returns the identity Guard verified for this request.
// It is only ever set by Guard, never read from a header.
func AccessIdentityFrom(ctx context.Context) (AccessIdentity, bool) {
	id, ok := ctx.Value(accessIdentityKey{}).(AccessIdentity)
	return id, ok
}

func (a *CloudflareAccess) verify(token string) (AccessIdentity, error) {
	if token == "" {
		return AccessIdentity{}, ErrAccessMissingToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return AccessIdentity{}, ErrAccessMalformed
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeJWTSegment(parts[0], &header); err != nil {
		return AccessIdentity{}, ErrAccessMalformed
	}
	if header.Alg != "RS256" {
		return AccessIdentity{}, ErrAccessAlgorithm
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return AccessIdentity{}, ErrAccessMalformed
	}
	key, err := a.keyFor(header.Kid)
	if err != nil {
		return AccessIdentity{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return AccessIdentity{}, ErrAccessSignature
	}

	var claims struct {
		Iss   string          `json:"iss"`
		Email string          `json:"email"`
		Aud   json.RawMessage `json:"aud"`
		Exp   *float64        `json:"exp"`
		Nbf   *float64        `json:"nbf"`
	}
	if err := decodeJWTSegment(parts[1], &claims); err != nil {
		return AccessIdentity{}, ErrAccessMalformed
	}
	if claims.Iss != a.issuer {
		return AccessIdentity{}, ErrAccessIssuer
	}
	if !audienceContains(claims.Aud, a.aud) {
		return AccessIdentity{}, ErrAccessAudience
	}
	now := a.now()
	if claims.Exp == nil || now.After(unixSeconds(*claims.Exp).Add(accessClockLeeway)) {
		return AccessIdentity{}, ErrAccessExpired
	}
	if claims.Nbf != nil && now.Add(accessClockLeeway).Before(unixSeconds(*claims.Nbf)) {
		return AccessIdentity{}, ErrAccessNotYetValid
	}
	return AccessIdentity{Email: claims.Email}, nil
}

// keyFor returns the public key for kid, fetching the team JWKS when the key
// is not cached and the last fetch is older than accessMinRefresh. Key
// rotation is handled this way: a new kid appears, one fetch picks it up.
func (a *CloudflareAccess) keyFor(kid string) (*rsa.PublicKey, error) {
	if key := a.cachedKey(kid); key != nil {
		return key, nil
	}
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	// Another request may have refreshed while this one waited.
	if key := a.cachedKey(kid); key != nil {
		return key, nil
	}
	if last := a.lastFetch.Load(); last != 0 && a.now().Sub(time.Unix(0, last)) < accessMinRefresh {
		return nil, ErrAccessUnknownKey
	}
	a.lastFetch.Store(a.now().UnixNano())
	keys, err := a.fetchKeys()
	if err != nil {
		// Keep the previous key set: a failed fetch must not drop keys that
		// still verify tokens.
		a.reportDeny(fmt.Errorf("access: fetching team JWKS: %w", err))
		return nil, ErrAccessUnknownKey
	}
	a.keys.Store(&keys)
	if key := keys[kid]; key != nil {
		return key, nil
	}
	return nil, ErrAccessUnknownKey
}

func (a *CloudflareAccess) cachedKey(kid string) *rsa.PublicKey {
	if keys := a.keys.Load(); keys != nil {
		return (*keys)[kid]
	}
	return nil
}

func (a *CloudflareAccess) fetchKeys() (map[string]*rsa.PublicKey, error) {
	resp, err := a.client.Get(a.certsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, accessMaxJWKSBytes)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decoding: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		exp := new(big.Int).SetBytes(e)
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exp.Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("no usable RSA keys")
	}
	return keys, nil
}

func (a *CloudflareAccess) reportDeny(err error) {
	debug.Log("proxy", "cloudflare access denied: %v", err)
	if a.onDeny == nil {
		return
	}
	v, _ := a.denyLogged.LoadOrStore(err.Error(), new(atomic.Int64))
	last := v.(*atomic.Int64)
	now := a.now().UnixNano()
	prev := last.Load()
	if prev != 0 && time.Duration(now-prev) < accessDenyLogInterval {
		return
	}
	if last.CompareAndSwap(prev, now) {
		a.onDeny(err)
	}
}

func decodeJWTSegment(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// audienceContains accepts the two JSON shapes the aud claim may take: a
// single string or an array of strings.
func audienceContains(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

func unixSeconds(s float64) time.Time {
	return time.Unix(int64(s), 0)
}
