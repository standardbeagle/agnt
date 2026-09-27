package proxy

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testTeam = "beagle.cloudflareaccess.com"
const testAUD = "aud-tag-0123"

type accessFixture struct {
	t       *testing.T
	keys    map[string]*rsa.PrivateKey
	served  atomic.Pointer[[]string] // kids the JWKS endpoint currently serves
	fetches atomic.Int32
	now     time.Time
	access  *CloudflareAccess
}

func newAccessFixture(t *testing.T, kids ...string) *accessFixture {
	t.Helper()
	f := &accessFixture{t: t, keys: map[string]*rsa.PrivateKey{}, now: time.Unix(1_800_000_000, 0)}
	for _, kid := range []string{"k1", "k2", "rogue"} {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		f.keys[kid] = k
	}
	f.serve(kids...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		var doc struct {
			Keys []map[string]string `json:"keys"`
		}
		for _, kid := range *f.served.Load() {
			pub := f.keys[kid].PublicKey
			doc.Keys = append(doc.Keys, map[string]string{
				"kid": kid, "kty": "RSA", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			})
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)

	a, err := NewCloudflareAccess(testTeam, testAUD)
	if err != nil {
		t.Fatal(err)
	}
	a.certsURL = srv.URL
	a.client = srv.Client()
	a.now = func() time.Time { return f.now }
	f.access = a
	return f
}

func (f *accessFixture) serve(kids ...string) { f.served.Store(&kids) }

// sign builds an RS256 token over claims with the named key; header overrides
// replace fields of the default header.
func (f *accessFixture) sign(kid string, claims map[string]any, header map[string]any) string {
	f.t.Helper()
	h := map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}
	for k, v := range header {
		h[k] = v
	}
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			f.t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(h) + "." + enc(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.keys[kid], crypto.SHA256, digest[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *accessFixture) claims(overrides map[string]any) map[string]any {
	c := map[string]any{
		"iss": "https://" + testTeam,
		"aud": []string{testAUD},
		"exp": f.now.Add(time.Hour).Unix(),
		"nbf": f.now.Add(-time.Minute).Unix(),
		"sub": "user-1",
	}
	for k, v := range overrides {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

func TestCloudflareAccessVerify(t *testing.T) {
	f := newAccessFixture(t, "k1")
	good := f.sign("k1", f.claims(nil), nil)
	parts := strings.Split(good, ".")
	tamperedClaims, _ := json.Marshal(f.claims(map[string]any{"sub": "admin"}))
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedClaims) + "." + parts[2]

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"valid, aud as array", good, nil},
		{"valid, aud as string", f.sign("k1", f.claims(map[string]any{"aud": testAUD}), nil), nil},
		{"valid inside exp leeway", f.sign("k1", f.claims(map[string]any{"exp": f.now.Add(-10 * time.Second).Unix()}), nil), nil},
		{"missing", "", ErrAccessMissingToken},
		{"two segments", "a.b", ErrAccessMalformed},
		{"garbage header", "!!.e30.sig", ErrAccessMalformed},
		{"alg none", f.sign("k1", f.claims(nil), map[string]any{"alg": "none"}), ErrAccessAlgorithm},
		{"alg HS256", f.sign("k1", f.claims(nil), map[string]any{"alg": "HS256"}), ErrAccessAlgorithm},
		{"payload tampered after signing", tampered, ErrAccessSignature},
		{"signed by key outside JWKS", f.sign("rogue", f.claims(nil), map[string]any{"kid": "k1"}), ErrAccessSignature},
		{"wrong issuer", f.sign("k1", f.claims(map[string]any{"iss": "https://other.cloudflareaccess.com"}), nil), ErrAccessIssuer},
		{"wrong audience", f.sign("k1", f.claims(map[string]any{"aud": []string{"someone-else"}}), nil), ErrAccessAudience},
		{"no audience", f.sign("k1", f.claims(map[string]any{"aud": nil}), nil), ErrAccessAudience},
		{"expired", f.sign("k1", f.claims(map[string]any{"exp": f.now.Add(-time.Hour).Unix()}), nil), ErrAccessExpired},
		{"no exp", f.sign("k1", f.claims(map[string]any{"exp": nil}), nil), ErrAccessExpired},
		{"not yet valid", f.sign("k1", f.claims(map[string]any{"nbf": f.now.Add(time.Hour).Unix()}), nil), ErrAccessNotYetValid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.access.Verify(tc.token); !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
	if n := f.fetches.Load(); n != 1 {
		t.Fatalf("JWKS fetched %d times across one key id, want 1 (cache not used)", n)
	}
}

// Key rotation: a new kid triggers exactly one refetch; a second unknown kid
// inside the refresh window does not reach the endpoint again.
func TestCloudflareAccessKeyRotationAndRefreshBound(t *testing.T) {
	f := newAccessFixture(t, "k1")
	if err := f.access.Verify(f.sign("k1", f.claims(nil), nil)); err != nil {
		t.Fatalf("premise: k1 token should verify: %v", err)
	}
	f.serve("k1", "k2")
	f.now = f.now.Add(accessMinRefresh + time.Second)
	if err := f.access.Verify(f.sign("k2", f.claims(nil), nil)); err != nil {
		t.Fatalf("rotated key k2 not picked up: %v", err)
	}
	if n := f.fetches.Load(); n != 2 {
		t.Fatalf("fetches after rotation = %d, want 2", n)
	}
	err := f.access.Verify(f.sign("rogue", f.claims(nil), nil))
	if !errors.Is(err, ErrAccessUnknownKey) {
		t.Fatalf("unknown kid inside refresh window = %v, want ErrAccessUnknownKey", err)
	}
	if n := f.fetches.Load(); n != 2 {
		t.Fatalf("unknown kid inside refresh window fetched JWKS again (fetches=%d)", n)
	}
	// k1 survives the rotation and the refused refresh.
	if err := f.access.Verify(f.sign("k1", f.claims(nil), nil)); err != nil {
		t.Fatalf("k1 lost after rotation: %v", err)
	}
}

func TestCloudflareAccessGuard(t *testing.T) {
	f := newAccessFixture(t, "k1")
	var reached atomic.Int32
	var denials []error
	f.access.SetOnDeny(func(err error) { denials = append(denials, err) })
	h := f.access.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	do := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://dev.example.com/", nil)
		if token != "" {
			req.Header.Set(AccessJWTHeader, token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(f.sign("k1", f.claims(nil), nil)); rec.Code != http.StatusNoContent || reached.Load() != 1 {
		t.Fatalf("valid token: code=%d reached=%d", rec.Code, reached.Load())
	}
	for i := 0; i < 3; i++ {
		rec := do("")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("missing token: code=%d", rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "Jwt") || strings.Contains(body, "access:") {
			t.Fatalf("403 body leaks the denial reason: %q", body)
		}
	}
	if reached.Load() != 1 {
		t.Fatalf("refused requests reached the wrapped handler (%d)", reached.Load())
	}
	if len(denials) != 1 || !errors.Is(denials[0], ErrAccessMissingToken) {
		t.Fatalf("denials surfaced = %v, want one ErrAccessMissingToken (rate-limited)", denials)
	}
	f.now = f.now.Add(accessDenyLogInterval + time.Second)
	do("")
	if len(denials) != 2 {
		t.Fatalf("denial not re-surfaced after the log interval (%d)", len(denials))
	}
}

func TestNewCloudflareAccessValidation(t *testing.T) {
	cases := []struct {
		team, aud string
		ok        bool
	}{
		{"beagle.cloudflareaccess.com", "aud", true},
		{"  Beagle.CloudflareAccess.com ", "aud", true},
		{"cloudflareaccess.com", "aud", false},
		{".cloudflareaccess.com", "aud", false},
		{"evil.com", "aud", false},
		{"evil.com/x.cloudflareaccess.com", "aud", false},
		{"a.b.cloudflareaccess.com", "aud", false},
		{"user@x.cloudflareaccess.com", "aud", false},
		{"beagle.cloudflareaccess.com", " ", false},
	}
	for _, tc := range cases {
		a, err := NewCloudflareAccess(tc.team, tc.aud)
		if (err == nil) != tc.ok {
			t.Errorf("NewCloudflareAccess(%q, %q) err=%v, want ok=%v", tc.team, tc.aud, err, tc.ok)
		}
		if err == nil && !strings.HasPrefix(a.certsURL, "https://beagle.cloudflareaccess.com/") {
			t.Errorf("certs URL %q does not stay on the team domain", a.certsURL)
		}
	}
}
