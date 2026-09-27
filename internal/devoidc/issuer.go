// Package devoidc is a dev-only OIDC issuer: authorization code + PKCE,
// refresh, userinfo and discovery, signing tokens for personas declared in
// .agnt.kdl. It is a test fixture for relying parties, not an identity
// provider: there are no passwords, keys live in memory only, and who may
// assume which persona is decided by the caller classification the proxy
// supplies (see docs/superpowers/specs/2026-09-27-dev-oidc-design.md).
package devoidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Prefix is where every proxy mounts the issuer.
const Prefix = "/__agnt/oidc"

const (
	codeTTL         = 60 * time.Second
	accessTokenTTL  = time.Hour
	refreshTokenTTL = 24 * time.Hour
	// maxOutstanding bounds codes and refresh tokens each. The issuer is a
	// dev fixture; hitting this means something is looping, so it refuses
	// loudly rather than growing without limit.
	maxOutstanding = 10000
)

// Persona is one identity the issuer can sign in as.
type Persona struct {
	Name        string
	Email       string
	DisplayName string
	Roles       []string
	Claims      map[string]string
}

// Client is one relying party.
type Client struct {
	ID             string
	RedirectURIs   []string // patterns; see config.RedirectURIMatches
	Secret         string   // empty = public client, PKCE required
	Audience       string   // empty = ID
	LoginPath      string
	SessionCookies []string
}

// Config is the issuer's view of the dev-oidc block.
type Config struct {
	Issuer         string // empty = the mount's local origin + Prefix
	Clients        map[string]Client
	Personas       map[string]Persona
	DefaultPersona string
	Allow          map[string][]string // verified Access email -> persona names
}

// Caller says how a request reached the issuer. The proxy fills it in from
// the listener the request arrived on; the issuer never derives it from
// headers.
type Caller struct {
	// Local: the proxy's own loopback listener, with every local check passed.
	Local bool
	// AccessEmail: the verified Cloudflare Access email, for requests that
	// arrived through a named tunnel. Empty otherwise.
	AccessEmail string
}

// AllowedPersonas returns the persona names this caller may assume, sorted.
func (c *Config) AllowedPersonas(caller Caller) []string {
	var names []string
	switch {
	case caller.Local:
		for name := range c.Personas {
			names = append(names, name)
		}
	case caller.AccessEmail != "":
		for email, list := range c.Allow {
			if strings.EqualFold(email, caller.AccessEmail) {
				for _, name := range list {
					if _, ok := c.Personas[name]; ok {
						names = append(names, name)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return dedupe(names)
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// Issuer holds the signing key and the outstanding codes and refresh tokens
// for one project. Config can be replaced while serving.
type Issuer struct {
	cfg atomic.Pointer[Config]
	key *rsa.PrivateKey
	kid string
	now func() time.Time

	codes       sync.Map // code -> *authCode
	codeCount   atomic.Int64
	refresh     sync.Map // token -> *refreshGrant
	refreshSize atomic.Int64
}

type authCode struct {
	clientID    string
	redirectURI string
	challenge   string
	persona     string
	scope       string
	nonce       string
	issuer      string
	expires     time.Time
}

type refreshGrant struct {
	clientID string
	persona  string
	scope    string
	issuer   string
	expires  time.Time
}

// New generates the signing key and returns an issuer for cfg.
func New(cfg Config) (*Issuer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("devoidc: generating signing key: %w", err)
	}
	return newWithKey(cfg, key), nil
}

// newWithKey builds an issuer around an existing key. Key generation is the
// slow part of New; tests share one key through this.
func newWithKey(cfg Config, key *rsa.PrivateKey) *Issuer {
	sum := sha256.Sum256(key.PublicKey.N.Bytes())
	is := &Issuer{key: key, kid: "agnt-dev-" + hex.EncodeToString(sum[:6]), now: time.Now}
	is.SetConfig(cfg)
	return is
}

// SetConfig replaces the issuer's config. Outstanding codes and refresh
// tokens stay valid only while their client and persona still exist.
func (is *Issuer) SetConfig(cfg Config) {
	c := cfg
	is.cfg.Store(&c)
}

// Config returns the current config.
func (is *Issuer) Config() *Config { return is.cfg.Load() }

// IssuerURL returns the `iss` for a mount whose local origin is localOrigin.
func (is *Issuer) IssuerURL(localOrigin string) string {
	if iss := is.Config().Issuer; iss != "" {
		return strings.TrimSuffix(iss, "/")
	}
	return strings.TrimSuffix(localOrigin, "/") + Prefix
}

// MintAccessToken signs an access token for persona without a browser flow.
// It backs the devauth MCP action for API tests; MCP is a local surface.
func (is *Issuer) MintAccessToken(issuer, clientID, persona string) (string, error) {
	cfg := is.Config()
	cl, ok := cfg.Clients[clientID]
	if !ok {
		return "", fmt.Errorf("devoidc: unknown client %q", clientID)
	}
	p, ok := cfg.Personas[persona]
	if !ok {
		return "", fmt.Errorf("devoidc: unknown persona %q", persona)
	}
	p.Name = persona
	return is.accessToken(issuer, cl, p, "openid profile email"), nil
}

func (is *Issuer) accessToken(issuer string, cl Client, p Persona, scope string) string {
	aud := cl.Audience
	if aud == "" {
		aud = cl.ID
	}
	claims := is.identityClaims(issuer, p)
	claims["aud"] = aud
	claims["client_id"] = cl.ID
	claims["scope"] = scope
	claims["exp"] = is.now().Add(accessTokenTTL).Unix()
	return is.sign(claims)
}

func (is *Issuer) idToken(issuer string, cl Client, p Persona, nonce string) string {
	claims := is.identityClaims(issuer, p)
	claims["aud"] = cl.ID
	claims["azp"] = cl.ID
	claims["exp"] = is.now().Add(accessTokenTTL).Unix()
	claims["auth_time"] = is.now().Unix()
	if nonce != "" {
		claims["nonce"] = nonce
	}
	return is.sign(claims)
}

func (is *Issuer) identityClaims(issuer string, p Persona) map[string]any {
	claims := map[string]any{}
	for k, v := range p.Claims {
		claims[k] = v
	}
	name := p.DisplayName
	if name == "" {
		name = p.Name
	}
	roles := p.Roles
	if roles == nil {
		roles = []string{}
	}
	claims["iss"] = issuer
	claims["sub"] = "agnt-dev|" + p.Name
	claims["iat"] = is.now().Unix()
	claims["email"] = p.Email
	claims["email_verified"] = true
	claims["name"] = name
	claims["preferred_username"] = p.Name
	claims["roles"] = roles
	return claims
}

func (is *Issuer) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": is.kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, is.key, crypto.SHA256, digest[:])
	if err != nil {
		// Signing with a freshly generated in-memory key only fails if the
		// system random source fails, which Go treats as fatal anyway.
		panic(fmt.Sprintf("devoidc: signing token: %v", err))
	}
	return signing + "." + b64(sig)
}

var (
	errBadToken     = errors.New("invalid token")
	errExpiredToken = errors.New("token expired")
)

// verify checks a token this issuer signed and returns its claims.
func (is *Issuer) verify(token, issuer string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errBadToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errBadToken
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&is.key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
		return nil, errBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errBadToken
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil, errBadToken
	}
	if claims["iss"] != issuer {
		return nil, errBadToken
	}
	exp, _ := claims["exp"].(float64)
	if is.now().Unix() >= int64(exp) {
		return nil, errExpiredToken
	}
	return claims, nil
}

func (is *Issuer) storeCode(c *authCode) (string, error) {
	is.sweep()
	if is.codeCount.Load() >= maxOutstanding {
		return "", errors.New("too many outstanding authorization codes")
	}
	code := randomToken()
	is.codes.Store(code, c)
	is.codeCount.Add(1)
	return code, nil
}

// takeCode returns the code's grant and deletes it: a code is single use
// even when the exchange that follows fails.
func (is *Issuer) takeCode(code string) (*authCode, bool) {
	v, ok := is.codes.LoadAndDelete(code)
	if !ok {
		return nil, false
	}
	is.codeCount.Add(-1)
	c := v.(*authCode)
	if is.now().After(c.expires) {
		return nil, false
	}
	return c, true
}

func (is *Issuer) storeRefresh(g *refreshGrant) (string, error) {
	is.sweep()
	if is.refreshSize.Load() >= maxOutstanding {
		return "", errors.New("too many outstanding refresh tokens")
	}
	tok := randomToken()
	is.refresh.Store(tok, g)
	is.refreshSize.Add(1)
	return tok, nil
}

// takeRefresh consumes a refresh token; the token endpoint issues a new one
// with every refresh, so each is single use.
func (is *Issuer) takeRefresh(tok string) (*refreshGrant, bool) {
	v, ok := is.refresh.LoadAndDelete(tok)
	if !ok {
		return nil, false
	}
	is.refreshSize.Add(-1)
	g := v.(*refreshGrant)
	if is.now().After(g.expires) {
		return nil, false
	}
	return g, true
}

// sweep drops expired codes and refresh tokens. It runs on insert, so the
// stores stay bounded without a background goroutine.
func (is *Issuer) sweep() {
	now := is.now()
	is.codes.Range(func(k, v any) bool {
		if now.After(v.(*authCode).expires) {
			if _, ok := is.codes.LoadAndDelete(k); ok {
				is.codeCount.Add(-1)
			}
		}
		return true
	})
	is.refresh.Range(func(k, v any) bool {
		if now.After(v.(*refreshGrant).expires) {
			if _, ok := is.refresh.LoadAndDelete(k); ok {
				is.refreshSize.Add(-1)
			}
		}
		return true
	})
}

func (is *Issuer) jwks() map[string]any {
	pub := is.key.PublicKey
	e := make([]byte, 0, 4)
	for v := pub.E; v > 0; v >>= 8 {
		e = append([]byte{byte(v)}, e...)
	}
	return map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": is.kid,
		"n": b64(pub.N.Bytes()), "e": b64(e),
	}}}
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("devoidc: random source failed: %v", err))
	}
	return b64(b[:])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// pkceS256 is the S256 code challenge for verifier.
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64(sum[:])
}
