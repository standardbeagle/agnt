package devoidc

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testRedirect = "http://localhost:3000/cb"
)

type fixture struct {
	t      *testing.T
	is     *Issuer
	srv    *httptest.Server
	caller atomic.Pointer[Caller]
	cross  atomic.Bool // SameOrigin answers false when set
	client *http.Client
}

func testConfig() Config {
	return Config{
		Clients: map[string]Client{
			"spa": {ID: "spa", RedirectURIs: []string{"http://localhost:*/cb"}},
			"web": {ID: "web", RedirectURIs: []string{"http://localhost:*/cb"}, Secret: "s3cret", Audience: "story-api",
				LoginPath: "/auth/login", SessionCookies: []string{"story.sid"}},
		},
		Personas: map[string]Persona{
			"standard": {Email: "std@example.com", DisplayName: "Standard <b>", Roles: []string{"user"}},
			"admin":    {Email: "admin@example.com", Roles: []string{"admin", "user"}, Claims: map[string]string{"tenant": "acme"}},
		},
		DefaultPersona: "standard",
		Allow:          map[string][]string{"andy@example.com": {"admin"}, "both@example.com": {"standard", "admin"}},
	}
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	f := &fixture{t: t, is: newWithKey(cfg, sharedKey(t))}
	is := f.is
	f.caller.Store(&Caller{Local: true})
	var h http.Handler
	// TLS, because the tunnel-side persona cookie is Secure and a cookie jar
	// rightly withholds it over plain HTTP.
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(f.srv.Close)
	h = is.Handler(Mount{
		LocalOrigin: f.srv.URL,
		Caller:      func(*http.Request) Caller { return *f.caller.Load() },
		SameOrigin:  func(*http.Request) bool { return !f.cross.Load() },
	})
	jar, _ := cookiejar.New(nil)
	f.client = &http.Client{Jar: jar, Transport: f.srv.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return f
}

func (f *fixture) url(path string) string { return f.srv.URL + Prefix + path }

func (f *fixture) authorizeQuery(client string, extra url.Values) url.Values {
	q := url.Values{
		"response_type": {"code"}, "client_id": {client}, "redirect_uri": {testRedirect},
		"scope": {"openid profile email"}, "state": {"st-1"}, "nonce": {"n-1"},
		"code_challenge": {pkceS256(testVerifier)}, "code_challenge_method": {"S256"},
	}
	for k, v := range extra {
		q[k] = v
	}
	return q
}

func (f *fixture) get(path string, q url.Values) *http.Response {
	f.t.Helper()
	u := f.url(path)
	if q != nil {
		u += "?" + q.Encode()
	}
	resp, err := f.client.Get(u)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *fixture) post(path string, form url.Values, basicUser, basicPass string) *http.Response {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.url(path), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicUser != "" {
		req.SetBasicAuth(basicUser, basicPass)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// codeFrom asserts a redirect to the registered URI and returns its code.
func (f *fixture) codeFrom(resp *http.Response) string {
	f.t.Helper()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("authorize: status %d, want 302: %s", resp.StatusCode, body)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if !strings.HasPrefix(loc.String(), testRedirect+"?") {
		f.t.Fatalf("redirected to %q, want the registered redirect URI", loc)
	}
	if loc.Query().Get("state") != "st-1" || loc.Query().Get("code") == "" {
		f.t.Fatalf("redirect lacks code/state: %s", loc)
	}
	return loc.Query().Get("code")
}

func (f *fixture) exchange(client, secret string, form url.Values) (int, map[string]any) {
	f.t.Helper()
	resp := f.post("/token", form, client, secret)
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func codeForm(code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {testRedirect}, "code_verifier": {testVerifier}}
}

// verifyWithJWKS checks a token's signature against the published JWKS,
// independently of the issuer's own verify, and returns its claims.
func (f *fixture) verifyWithJWKS(token string) map[string]any {
	f.t.Helper()
	var doc struct {
		Keys []struct{ Kid, N, E string } `json:"keys"`
	}
	resp := f.get("/jwks", nil)
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || len(doc.Keys) != 1 {
		f.t.Fatalf("jwks: %v %+v", err, doc)
	}
	n, _ := base64.RawURLEncoding.DecodeString(doc.Keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(doc.Keys[0].E)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	parts := strings.Split(token, ".")
	var header struct{ Alg, Kid string }
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(hb, &header)
	if header.Alg != "RS256" || header.Kid != doc.Keys[0].Kid {
		f.t.Fatalf("token header %+v does not name the published key", header)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		f.t.Fatalf("token does not verify against JWKS: %v", err)
	}
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(pb, &claims)
	return claims
}

func TestCodeFlowPublicClientDefaultPersona(t *testing.T) {
	f := newFixture(t, testConfig())
	code := f.codeFrom(f.get("/authorize", f.authorizeQuery("spa", nil)))
	status, tok := f.exchange("", "", mergeForm(codeForm(code), url.Values{"client_id": {"spa"}}))
	if status != http.StatusOK {
		t.Fatalf("token: %d %v", status, tok)
	}
	iss := f.srv.URL + Prefix
	id := f.verifyWithJWKS(tok["id_token"].(string))
	if id["iss"] != iss || id["aud"] != "spa" || id["nonce"] != "n-1" || id["sub"] != "agnt-dev|standard" ||
		id["email"] != "std@example.com" || id["preferred_username"] != "standard" || id["name"] != "Standard <b>" {
		t.Fatalf("id_token claims: %v", id)
	}
	at := f.verifyWithJWKS(tok["access_token"].(string))
	if at["aud"] != "spa" || at["client_id"] != "spa" || at["iss"] != iss {
		t.Fatalf("access_token claims: %v", at)
	}

	req, _ := http.NewRequest(http.MethodGet, f.url("/userinfo"), nil)
	req.Header.Set("Authorization", "Bearer "+tok["access_token"].(string))
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ui map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&ui)
	if resp.StatusCode != 200 || ui["email"] != "std@example.com" || ui["aud"] != nil {
		t.Fatalf("userinfo: %d %v", resp.StatusCode, ui)
	}

	// Refresh: new tokens, and the old refresh token is spent.
	rt := tok["refresh_token"].(string)
	refreshForm := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"spa"}}
	status, again := f.exchange("", "", refreshForm)
	if status != http.StatusOK || again["refresh_token"] == rt || f.verifyWithJWKS(again["id_token"].(string))["sub"] != "agnt-dev|standard" {
		t.Fatalf("refresh: %d %v", status, again)
	}
	if status, body := f.exchange("", "", refreshForm); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("reused refresh token: %d %v, want invalid_grant", status, body)
	}
}

func TestConfidentialClientCustomClaimsAndAudience(t *testing.T) {
	f := newFixture(t, testConfig())
	f.caller.Store(&Caller{AccessEmail: "andy@example.com"}) // exactly one allowed persona: admin
	code := f.codeFrom(f.get("/authorize", f.authorizeQuery("web", nil)))
	status, tok := f.exchange("web", "s3cret", codeForm(code))
	if status != http.StatusOK {
		t.Fatalf("token: %d %v", status, tok)
	}
	at := f.verifyWithJWKS(tok["access_token"].(string))
	roles, _ := at["roles"].([]any)
	if at["aud"] != "story-api" || at["tenant"] != "acme" || at["sub"] != "agnt-dev|admin" || len(roles) != 2 {
		t.Fatalf("admin access token: %v", at)
	}
}

func TestTokenEndpointRefusals(t *testing.T) {
	f := newFixture(t, testConfig())
	fresh := func(client string) string { return f.codeFrom(f.get("/authorize", f.authorizeQuery(client, nil))) }

	used := fresh("web")
	if s, _ := f.exchange("web", "s3cret", codeForm(used)); s != 200 {
		t.Fatalf("premise: first exchange should succeed, got %d", s)
	}
	cases := []struct {
		name, client, secret string
		form                 url.Values
		status               int
		err, desc            string
	}{
		{"code reused", "web", "s3cret", codeForm(used), 400, "invalid_grant", "unknown, used or expired"},
		{"unknown code", "web", "s3cret", codeForm("nope"), 400, "invalid_grant", "unknown, used or expired"},
		{"wrong verifier", "web", "s3cret", mergeForm(codeForm(fresh("web")), url.Values{"code_verifier": {"wrong"}}), 400, "invalid_grant", "code_verifier"},
		{"wrong redirect", "web", "s3cret", mergeForm(codeForm(fresh("web")), url.Values{"redirect_uri": {"http://localhost:3001/cb"}}), 400, "invalid_grant", "redirect_uri"},
		{"other client's code", "", "", mergeForm(codeForm(fresh("web")), url.Values{"client_id": {"spa"}}), 400, "invalid_grant", "another client"},
		{"wrong secret", "web", "nope", codeForm(fresh("web")), 401, "invalid_client", "authentication failed"},
		{"missing secret", "", "", mergeForm(codeForm(fresh("web")), url.Values{"client_id": {"web"}}), 401, "invalid_client", "authentication failed"},
		{"unknown client", "ghost", "", codeForm(fresh("web")), 401, "invalid_client", "unknown client"},
		{"bad grant type", "web", "s3cret", url.Values{"grant_type": {"password"}}, 400, "unsupported_grant_type", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := f.exchange(tc.client, tc.secret, tc.form)
			desc, _ := body["error_description"].(string)
			if status != tc.status || body["error"] != tc.err || !strings.Contains(desc, tc.desc) {
				t.Fatalf("got %d %v, want %d %s (%q)", status, body, tc.status, tc.err, tc.desc)
			}
		})
	}

	// Expiry: a code older than its TTL is refused.
	code := fresh("web")
	f.is.now = func() time.Time { return time.Now().Add(codeTTL + time.Second) }
	if s, body := f.exchange("web", "s3cret", codeForm(code)); s != 400 || body["error"] != "invalid_grant" {
		t.Fatalf("expired code: %d %v", s, body)
	}
}

func TestAuthorizeRefusals(t *testing.T) {
	f := newFixture(t, testConfig())
	// Bad client or redirect: an error page, never a redirect.
	for name, q := range map[string]url.Values{
		"unknown client":        f.authorizeQuery("ghost", nil),
		"unregistered redirect": f.authorizeQuery("spa", url.Values{"redirect_uri": {"https://evil.example/cb"}}),
		"port-wildcard abuse":   f.authorizeQuery("spa", url.Values{"redirect_uri": {"http://localhost:3000@evil.example/cb"}}),
	} {
		if resp := f.get("/authorize", q); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d location=%q, want 400 with no redirect", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// Known client + redirect: errors go back to the app.
	for name, tc := range map[string]struct {
		q    url.Values
		want string
	}{
		"public client without PKCE": {f.authorizeQuery("spa", url.Values{"code_challenge": {""}}), "invalid_request"},
		"plain PKCE":                 {f.authorizeQuery("spa", url.Values{"code_challenge_method": {"plain"}}), "invalid_request"},
		"token response type":        {f.authorizeQuery("spa", url.Values{"response_type": {"token"}}), "unsupported_response_type"},
	} {
		resp := f.get("/authorize", tc.q)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || loc.Query().Get("error") != tc.want || loc.Query().Get("code") != "" {
			t.Errorf("%s: %d %s, want redirect with error=%s", name, resp.StatusCode, loc, tc.want)
		}
	}
}

func TestPersonaPolicyPickerAndCookie(t *testing.T) {
	f := newFixture(t, testConfig())

	// Two personas allowed over the tunnel and no local default: picker.
	f.caller.Store(&Caller{AccessEmail: "Both@Example.com"})
	resp := f.get("/authorize", f.authorizeQuery("spa", nil))
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if resp.StatusCode != 200 || !strings.Contains(page, `value="admin"`) || !strings.Contains(page, `value="standard"`) {
		t.Fatalf("picker: %d %s", resp.StatusCode, page)
	}
	if strings.Contains(page, "Standard <b>") || !strings.Contains(page, "Standard &lt;b&gt;") {
		t.Fatal("picker does not escape persona labels")
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("picker served without its CSP")
	}

	// Picking a persona outside the caller's set is refused.
	f.caller.Store(&Caller{AccessEmail: "andy@example.com"})
	pickForm := mergeForm(f.authorizeQuery("spa", nil), url.Values{"persona": {"standard"}})
	if r := f.post("/pick", pickForm, "", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("pick outside allowed set: %d, want 403", r.StatusCode)
	}
	// Cross-origin pick is refused.
	f.caller.Store(&Caller{AccessEmail: "both@example.com"})
	f.cross.Store(true)
	pickForm.Set("persona", "admin")
	if r := f.post("/pick", pickForm, "", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin pick: %d, want 403", r.StatusCode)
	}
	f.cross.Store(false)

	// A valid pick sets the cookie; the next authorize is silent for that persona.
	code := f.codeFrom(f.post("/pick", pickForm, "", ""))
	_, tok := f.exchange("", "", mergeForm(codeForm(code), url.Values{"client_id": {"spa"}}))
	if f.verifyWithJWKS(tok["id_token"].(string))["sub"] != "agnt-dev|admin" {
		t.Fatal("pick did not sign in as the picked persona")
	}
	code = f.codeFrom(f.get("/authorize", f.authorizeQuery("spa", nil)))
	_, tok = f.exchange("", "", mergeForm(codeForm(code), url.Values{"client_id": {"spa"}}))
	if f.verifyWithJWKS(tok["id_token"].(string))["sub"] != "agnt-dev|admin" {
		t.Fatal("persona cookie not honoured on the next authorize")
	}
	// prompt=select_account shows the picker again even with a cookie.
	if r := f.get("/authorize", f.authorizeQuery("spa", url.Values{"prompt": {"select_account"}})); r.StatusCode != 200 {
		t.Fatalf("prompt=select_account: %d, want the picker", r.StatusCode)
	}
	// A cookie naming a persona this caller may not use is ignored.
	f.caller.Store(&Caller{AccessEmail: "andy@example.com"})
	code = f.codeFrom(f.get("/authorize", f.authorizeQuery("spa", nil)))
	_, tok = f.exchange("", "", mergeForm(codeForm(code), url.Values{"client_id": {"spa"}}))
	if f.verifyWithJWKS(tok["id_token"].(string))["sub"] != "agnt-dev|admin" {
		t.Fatal("wrong persona for single-persona caller")
	}

	// No personas: every route is 403.
	for _, c := range []Caller{{}, {AccessEmail: "stranger@example.com"}} {
		f.caller.Store(&c)
		for _, path := range []string{"/.well-known/openid-configuration", "/jwks", "/state"} {
			if r := f.get(path, nil); r.StatusCode != http.StatusForbidden {
				t.Errorf("caller %+v %s: %d, want 403", c, path, r.StatusCode)
			}
		}
	}
}

func TestDiscoveryIssuerAndBackChannel(t *testing.T) {
	cfg := testConfig()
	cfg.Issuer = "https://dev.example.com/__agnt/oidc"
	f := newFixture(t, cfg)
	doc := func() map[string]any {
		var d map[string]any
		_ = json.NewDecoder(f.get("/.well-known/openid-configuration", nil).Body).Decode(&d)
		return d
	}
	local := doc()
	if local["issuer"] != cfg.Issuer || local["authorization_endpoint"] != cfg.Issuer+"/authorize" ||
		local["token_endpoint"] != f.srv.URL+Prefix+"/token" || local["jwks_uri"] != f.srv.URL+Prefix+"/jwks" {
		t.Fatalf("local discovery: %v", local)
	}
	f.caller.Store(&Caller{AccessEmail: "andy@example.com"})
	remote := doc()
	if remote["issuer"] != cfg.Issuer || remote["token_endpoint"] != cfg.Issuer+"/token" {
		t.Fatalf("tunnelled discovery: %v", remote)
	}
	code := f.codeFrom(f.get("/authorize", f.authorizeQuery("spa", nil)))
	_, tok := f.exchange("", "", mergeForm(codeForm(code), url.Values{"client_id": {"spa"}}))
	if f.verifyWithJWKS(tok["id_token"].(string))["iss"] != cfg.Issuer {
		t.Fatal("tokens must carry the configured issuer, not the serving host")
	}
}

func TestSwitchStateAndLogout(t *testing.T) {
	f := newFixture(t, testConfig())
	state := func() map[string]any {
		var s map[string]any
		_ = json.NewDecoder(f.get("/state", nil).Body).Decode(&s)
		return s
	}
	if s := state(); s["persona"] != "standard" || len(s["personas"].([]any)) != 2 {
		t.Fatalf("initial state: %v", s)
	}

	f.cross.Store(true)
	if r := f.post("/switch", url.Values{"persona": {"admin"}}, "", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin switch: %d", r.StatusCode)
	}
	f.cross.Store(false)
	if r := f.post("/switch", url.Values{"persona": {"root"}}, "", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("switch to unknown persona: %d", r.StatusCode)
	}
	r := f.post("/switch", url.Values{"persona": {"admin"}}, "", "")
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/auth/login" {
		t.Fatalf("switch: %d -> %q, want 303 to login-path", r.StatusCode, r.Header.Get("Location"))
	}
	var expired bool
	for _, c := range r.Cookies() {
		if c.Name == "story.sid" && c.MaxAge < 0 && c.Path == "/" {
			expired = true
		}
	}
	if !expired {
		t.Fatalf("switch did not expire the app session cookie: %v", r.Header["Set-Cookie"])
	}
	if s := state(); s["persona"] != "admin" {
		t.Fatalf("state after switch: %v", s)
	}

	// Logout clears the persona and only redirects to a registered origin.
	r = f.get("/logout", url.Values{"post_logout_redirect_uri": {"http://localhost:4000/bye"}, "state": {"x"}})
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "http://localhost:4000/bye?state=x" {
		t.Fatalf("logout redirect: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if s := state(); s["persona"] != "standard" {
		t.Fatalf("logout did not clear the persona: %v", s)
	}
	if r := f.get("/logout", url.Values{"post_logout_redirect_uri": {"https://evil.example/"}}); r.StatusCode != http.StatusOK {
		t.Fatalf("logout to unregistered origin: %d %q, want no redirect", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestMintAccessToken(t *testing.T) {
	f := newFixture(t, testConfig())
	tok, err := f.is.MintAccessToken("https://iss.example/o", "web", "admin")
	if err != nil {
		t.Fatal(err)
	}
	claims := f.verifyWithJWKS(tok)
	if claims["iss"] != "https://iss.example/o" || claims["aud"] != "story-api" || claims["sub"] != "agnt-dev|admin" {
		t.Fatalf("minted claims: %v", claims)
	}
	if _, err := f.is.MintAccessToken("x", "ghost", "admin"); err == nil {
		t.Fatal("minted for an unknown client")
	}
	if _, err := f.is.MintAccessToken("x", "web", "root"); err == nil {
		t.Fatal("minted for an unknown persona")
	}
}

var (
	sharedKeyOnce sync.Once
	sharedKeyVal  *rsa.PrivateKey
)

// sharedKey generates one signing key for the whole package's tests.
func sharedKey(t *testing.T) *rsa.PrivateKey {
	sharedKeyOnce.Do(func() {
		is, err := New(Config{})
		if err != nil {
			t.Fatal(err)
		}
		sharedKeyVal = is.key
	})
	return sharedKeyVal
}

func mergeForm(a, b url.Values) url.Values {
	out := url.Values{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestMintEndpointLocalOnly(t *testing.T) {
	f := newFixture(t, testConfig())
	form := url.Values{"client": {"web"}, "persona": {"admin"}}
	resp := f.post("/mint", form, "", "")
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != 200 || f.verifyWithJWKS(body["access_token"].(string))["sub"] != "agnt-dev|admin" {
		t.Fatalf("local mint: %d %v", resp.StatusCode, body)
	}
	if r := f.post("/mint", url.Values{"client": {"web"}, "persona": {"root"}}, "", ""); r.StatusCode != 400 {
		t.Fatalf("mint unknown persona: %d, want 400", r.StatusCode)
	}
	f.cross.Store(true)
	if r := f.post("/mint", form, "", ""); r.StatusCode != 403 {
		t.Fatalf("cross-origin mint: %d, want 403", r.StatusCode)
	}
	f.cross.Store(false)
	f.caller.Store(&Caller{AccessEmail: "andy@example.com"}) // may use admin, but not mint
	if r := f.post("/mint", form, "", ""); r.StatusCode != 403 {
		t.Fatalf("mint over the tunnel: %d, want 403", r.StatusCode)
	}
}

// TestSignInSwitchSignInAgain is the relying-party loop the feature exists
// for: sign in as the default persona, switch, sign in again, see the new
// persona's roles.
func TestSignInSwitchSignInAgain(t *testing.T) {
	f := newFixture(t, testConfig())
	login := func() map[string]any {
		t.Helper()
		code := f.codeFrom(f.get("/authorize", f.authorizeQuery("web", nil)))
		status, tok := f.exchange("web", "s3cret", codeForm(code))
		if status != http.StatusOK {
			t.Fatalf("token: %d %v", status, tok)
		}
		return f.verifyWithJWKS(tok["id_token"].(string))
	}
	first := login()
	if first["sub"] != "agnt-dev|standard" || fmt.Sprint(first["roles"]) != "[user]" {
		t.Fatalf("first login: %v", first)
	}
	if r := f.post("/switch", url.Values{"persona": {"admin"}}, "", ""); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("switch: %d", r.StatusCode)
	}
	second := login()
	if second["sub"] != "agnt-dev|admin" || fmt.Sprint(second["roles"]) != "[admin user]" || second["tenant"] != "acme" {
		t.Fatalf("login after switch: %v", second)
	}
}

func TestTailnetCaller(t *testing.T) {
	f := newFixture(t, testConfig())
	f.caller.Store(&Caller{TailnetLogin: "Both@Example.com"})
	var st State
	_ = json.NewDecoder(f.get("/state", nil).Body).Decode(&st)
	if strings.Join(st.Names(), ",") != "admin,standard" || st.Persona != "" {
		t.Fatalf("listed tailnet login: %+v (no default persona remotely)", st)
	}
	// Tailnet callers may mint, but only their own personas.
	resp := f.post("/mint", url.Values{"client": {"web"}, "persona": {"admin"}}, "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("tailnet mint: %d", resp.StatusCode)
	}
	f.caller.Store(&Caller{TailnetLogin: "andy@example.com"}) // admin only
	if r := f.post("/mint", url.Values{"client": {"web"}, "persona": {"standard"}}, "", ""); r.StatusCode != 403 {
		t.Fatalf("tailnet mint outside allowed personas: %d, want 403", r.StatusCode)
	}
	f.caller.Store(&Caller{TailnetLogin: "stranger@example.com"})
	if r := f.get("/state", nil); r.StatusCode != 403 {
		t.Fatalf("unlisted tailnet login: %d, want 403", r.StatusCode)
	}
	// The tailnet persona cookie is not Secure: the tailnet listener is plain
	// HTTP, and a Secure cookie would never come back.
	f.caller.Store(&Caller{TailnetLogin: "both@example.com"})
	r := f.post("/switch", url.Values{"persona": {"admin"}}, "", "")
	for _, c := range r.Cookies() {
		if c.Name == PersonaCookie && c.Secure {
			t.Fatal("tailnet persona cookie marked Secure")
		}
	}
}
