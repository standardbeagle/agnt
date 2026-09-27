package devoidc

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// PersonaCookie holds the browser's chosen persona. It is a preference, not
// an authorization: every use is re-checked against the caller's allowed set.
const PersonaCookie = "__agnt_persona"

// Mount is one proxy's view of the issuer.
type Mount struct {
	// LocalOrigin is the proxy's own loopback origin, e.g.
	// http://localhost:12345. It is the default issuer origin and the
	// back-channel origin handed to local discovery requests.
	LocalOrigin string
	// Caller classifies a request by the listener it arrived on.
	Caller func(*http.Request) Caller
	// SameOrigin reports whether a state-changing POST came from a page on
	// the proxy's own origin (or its public URL).
	SameOrigin func(*http.Request) bool
}

// Handler serves the issuer under Prefix for one proxy.
func (is *Issuer) Handler(m Mount) http.Handler {
	h := &handler{is: is, m: m}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"/.well-known/openid-configuration", h.discovery)
	mux.HandleFunc("GET "+Prefix+"/jwks", h.jwks)
	mux.HandleFunc("GET "+Prefix+"/authorize", h.authorize)
	mux.HandleFunc("POST "+Prefix+"/pick", h.pick)
	mux.HandleFunc("POST "+Prefix+"/token", h.token)
	mux.HandleFunc("GET "+Prefix+"/userinfo", h.userinfo)
	mux.HandleFunc("GET "+Prefix+"/logout", h.logout)
	mux.HandleFunc("POST "+Prefix+"/switch", h.switchPersona)
	mux.HandleFunc("GET "+Prefix+"/state", h.state)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(h.is.Config().AllowedPersonas(h.m.Caller(r))) == 0 {
			http.Error(w, "dev-oidc is not available on this connection", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type handler struct {
	is *Issuer
	m  Mount
}

func (h *handler) issuer() string { return h.is.IssuerURL(h.m.LocalOrigin) }

func (h *handler) discovery(w http.ResponseWriter, r *http.Request) {
	iss := h.issuer()
	back := iss
	if h.m.Caller(r).Local {
		// A backend fetching discovery locally cannot reach a tunnel
		// hostname through Access, so it gets local back-channel URLs.
		// `issuer` stays the configured value either way.
		back = strings.TrimSuffix(h.m.LocalOrigin, "/") + Prefix
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/authorize",
		"end_session_endpoint":                  iss + "/logout",
		"token_endpoint":                        back + "/token",
		"jwks_uri":                              back + "/jwks",
		"userinfo_endpoint":                     back + "/userinfo",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"claims_supported":                      []string{"sub", "email", "email_verified", "name", "preferred_username", "roles"},
	})
}

func (h *handler) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.is.jwks())
}

// authRequest is a validated authorization request.
type authRequest struct {
	client      Client
	redirectURI string
	state       string
	nonce       string
	scope       string
	challenge   string
	prompt      string
}

// parseAuthRequest validates client and redirect URI first. Until both are
// known good, errors are shown as a page and never redirected, so a bad
// request cannot bounce the browser to an arbitrary URL.
func (h *handler) parseAuthRequest(w http.ResponseWriter, r *http.Request, q url.Values) (*authRequest, bool) {
	cfg := h.is.Config()
	cl, ok := cfg.Clients[q.Get("client_id")]
	if !ok {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return nil, false
	}
	redirect := q.Get("redirect_uri")
	if !slices.ContainsFunc(cl.RedirectURIs, func(p string) bool { return RedirectURIMatches(p, redirect) }) {
		http.Error(w, "redirect_uri is not registered for this client", http.StatusBadRequest)
		return nil, false
	}
	ar := &authRequest{
		client: cl, redirectURI: redirect, state: q.Get("state"), nonce: q.Get("nonce"),
		scope: q.Get("scope"), challenge: q.Get("code_challenge"), prompt: q.Get("prompt"),
	}
	if q.Get("response_type") != "code" {
		redirectError(w, r, ar, "unsupported_response_type", "only response_type=code is supported")
		return nil, false
	}
	if ar.challenge != "" && q.Get("code_challenge_method") != "S256" {
		redirectError(w, r, ar, "invalid_request", "code_challenge_method must be S256")
		return nil, false
	}
	if cl.Secret == "" && ar.challenge == "" {
		redirectError(w, r, ar, "invalid_request", "public clients must use PKCE (S256)")
		return nil, false
	}
	return ar, true
}

func (h *handler) authorize(w http.ResponseWriter, r *http.Request) {
	ar, ok := h.parseAuthRequest(w, r, r.URL.Query())
	if !ok {
		return
	}
	caller := h.m.Caller(r)
	cfg := h.is.Config()
	allowed := cfg.AllowedPersonas(caller)
	forcePick := ar.prompt == "login" || ar.prompt == "select_account"

	if !forcePick {
		if c, err := r.Cookie(PersonaCookie); err == nil && slices.Contains(allowed, c.Value) {
			h.issueCode(w, r, ar, c.Value)
			return
		}
		switch {
		case len(allowed) == 1:
			h.issueCode(w, r, ar, allowed[0])
			return
		case caller.Local && cfg.DefaultPersona != "" && slices.Contains(allowed, cfg.DefaultPersona):
			h.issueCode(w, r, ar, cfg.DefaultPersona)
			return
		}
	}
	if ar.prompt == "none" {
		redirectError(w, r, ar, "login_required", "a persona must be chosen")
		return
	}
	renderPicker(w, cfg, allowed, r.URL.Query())
}

func (h *handler) pick(w http.ResponseWriter, r *http.Request) {
	if !h.m.SameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ar, ok := h.parseAuthRequest(w, r, r.PostForm)
	if !ok {
		return
	}
	persona := r.PostForm.Get("persona")
	if !slices.Contains(h.is.Config().AllowedPersonas(h.m.Caller(r)), persona) {
		http.Error(w, "persona not available to you", http.StatusForbidden)
		return
	}
	h.setPersonaCookie(w, r, persona)
	h.issueCode(w, r, ar, persona)
}

func (h *handler) issueCode(w http.ResponseWriter, r *http.Request, ar *authRequest, persona string) {
	code, err := h.is.storeCode(&authCode{
		clientID: ar.client.ID, redirectURI: ar.redirectURI, challenge: ar.challenge,
		persona: persona, scope: ar.scope, nonce: ar.nonce, issuer: h.issuer(),
		expires: h.is.now().Add(codeTTL),
	})
	if err != nil {
		redirectError(w, r, ar, "temporarily_unavailable", err.Error())
		return
	}
	v := url.Values{"code": {code}}
	if ar.state != "" {
		v.Set("state", ar.state)
	}
	v.Set("iss", h.issuer())
	http.Redirect(w, r, withQuery(ar.redirectURI, v), http.StatusFound)
}

func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	cl, ok := h.authenticateClient(w, r)
	if !ok {
		return
	}
	cfg := h.is.Config()
	var persona, scope, nonce string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		ac, ok := h.is.takeCode(r.PostForm.Get("code"))
		switch {
		case !ok:
			tokenError(w, http.StatusBadRequest, "invalid_grant", "code is unknown, used or expired")
			return
		case ac.clientID != cl.ID:
			tokenError(w, http.StatusBadRequest, "invalid_grant", "code was issued to another client")
			return
		case ac.redirectURI != r.PostForm.Get("redirect_uri"):
			tokenError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
			return
		case ac.challenge != "" && pkceS256(r.PostForm.Get("code_verifier")) != ac.challenge:
			tokenError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match code_challenge")
			return
		case ac.issuer != h.issuer():
			tokenError(w, http.StatusBadRequest, "invalid_grant", "code was issued by another issuer")
			return
		}
		persona, scope, nonce = ac.persona, ac.scope, ac.nonce
	case "refresh_token":
		g, ok := h.is.takeRefresh(r.PostForm.Get("refresh_token"))
		switch {
		case !ok:
			tokenError(w, http.StatusBadRequest, "invalid_grant", "refresh_token is unknown, used or expired")
			return
		case g.clientID != cl.ID || g.issuer != h.issuer():
			tokenError(w, http.StatusBadRequest, "invalid_grant", "refresh_token was issued to another client")
			return
		}
		persona, scope = g.persona, g.scope
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	p, ok := cfg.Personas[persona]
	if !ok {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "persona no longer exists")
		return
	}
	p.Name = persona
	refresh, err := h.is.storeRefresh(&refreshGrant{
		clientID: cl.ID, persona: persona, scope: scope, issuer: h.issuer(),
		expires: h.is.now().Add(refreshTokenTTL),
	})
	if err != nil {
		tokenError(w, http.StatusServiceUnavailable, "temporarily_unavailable", err.Error())
		return
	}
	resp := map[string]any{
		"access_token":  h.is.accessToken(h.issuer(), cl, p, scope),
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         scope,
	}
	if slices.Contains(strings.Fields(scope), "openid") {
		resp["id_token"] = h.is.idToken(h.issuer(), cl, p, nonce)
	}
	writeJSON(w, http.StatusOK, resp)
}

// authenticateClient accepts client_secret_basic, client_secret_post, or no
// secret for a public client. A confidential client must present its secret.
func (h *handler) authenticateClient(w http.ResponseWriter, r *http.Request) (Client, bool) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	cl, ok := h.is.Config().Clients[id]
	if !ok {
		tokenError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return Client{}, false
	}
	if cl.Secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(cl.Secret)) != 1 {
		tokenError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return Client{}, false
	}
	return cl, true
}

func (h *handler) userinfo(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		http.Error(w, "bearer token required", http.StatusUnauthorized)
		return
	}
	claims, err := h.is.verify(strings.TrimSpace(tok), h.issuer())
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	for _, k := range []string{"aud", "exp", "iat", "scope", "client_id", "iss"} {
		delete(claims, k)
	}
	writeJSON(w, http.StatusOK, claims)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	h.clearPersonaCookie(w, r)
	target := r.URL.Query().Get("post_logout_redirect_uri")
	if target != "" && h.registeredOrigin(target) {
		if st := r.URL.Query().Get("state"); st != "" {
			target = withQuery(target, url.Values{"state": {st}})
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("Signed out of the agnt dev issuer.\n"))
}

// registeredOrigin reports whether target shares scheme and host with one of
// the clients' redirect URIs, so logout never redirects off the app.
func (h *handler) registeredOrigin(target string) bool {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return false
	}
	for _, cl := range h.is.Config().Clients {
		for _, pattern := range cl.RedirectURIs {
			if RedirectURIMatches(pattern, u.Scheme+"://"+u.Host+patternPath(pattern)) {
				return true
			}
		}
	}
	return false
}

// patternPath returns the path part of a redirect pattern, so an origin can
// be checked against the pattern's port wildcard.
func patternPath(pattern string) string {
	rest := pattern[strings.Index(pattern, "://")+3:]
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[i:]
	}
	return ""
}

// switchPersona sets the persona and sends the app to log in again. It is
// the one entry point for the indicator, the overlay palette and the MCP
// tool.
func (h *handler) switchPersona(w http.ResponseWriter, r *http.Request) {
	if !h.m.SameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	persona := r.PostForm.Get("persona")
	cfg := h.is.Config()
	if !slices.Contains(cfg.AllowedPersonas(h.m.Caller(r)), persona) {
		http.Error(w, "persona not available to you", http.StatusForbidden)
		return
	}
	h.setPersonaCookie(w, r, persona)
	for _, name := range sessionCookies(cfg) {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
	}
	http.Redirect(w, r, loginPath(cfg, r.PostForm.Get("client")), http.StatusSeeOther)
}

func sessionCookies(cfg *Config) []string {
	var names []string
	for _, cl := range cfg.Clients {
		names = append(names, cl.SessionCookies...)
	}
	sort.Strings(names)
	return dedupe(names)
}

// loginPath picks where a switch lands: the named client's login-path, else
// the only client's, else the first declared one's, else "/".
func loginPath(cfg *Config, clientID string) string {
	if cl, ok := cfg.Clients[clientID]; ok && cl.LoginPath != "" {
		return cl.LoginPath
	}
	ids := make([]string, 0, len(cfg.Clients))
	for id := range cfg.Clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if lp := cfg.Clients[id].LoginPath; lp != "" {
			return lp
		}
	}
	return "/"
}

type personaView struct {
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
}

func (h *handler) state(w http.ResponseWriter, r *http.Request) {
	cfg := h.is.Config()
	caller := h.m.Caller(r)
	allowed := cfg.AllowedPersonas(caller)
	current := ""
	if c, err := r.Cookie(PersonaCookie); err == nil && slices.Contains(allowed, c.Value) {
		current = c.Value
	} else if caller.Local && slices.Contains(allowed, cfg.DefaultPersona) {
		current = cfg.DefaultPersona
	}
	views := make([]personaView, 0, len(allowed))
	for _, name := range allowed {
		p := cfg.Personas[name]
		views = append(views, personaView{Name: name, Email: p.Email, DisplayName: p.DisplayName, Roles: p.Roles})
	}
	writeJSON(w, http.StatusOK, map[string]any{"persona": current, "personas": views})
}

func (h *handler) setPersonaCookie(w http.ResponseWriter, r *http.Request, persona string) {
	http.SetCookie(w, &http.Cookie{
		Name: PersonaCookie, Value: persona, Path: Prefix, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: h.m.Caller(r).AccessEmail != "",
	})
}

func (h *handler) clearPersonaCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: PersonaCookie, Value: "", Path: Prefix, MaxAge: -1, HttpOnly: true})
}

func redirectError(w http.ResponseWriter, r *http.Request, ar *authRequest, code, desc string) {
	v := url.Values{"error": {code}, "error_description": {desc}}
	if ar.state != "" {
		v.Set("state", ar.state)
	}
	http.Redirect(w, r, withQuery(ar.redirectURI, v), http.StatusFound)
}

func tokenError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withQuery(base string, v url.Values) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + v.Encode()
}
