package config

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/standardbeagle/agnt/internal/devoidc"
)

// DevOIDCConfig declares the dev-only OIDC issuer agnt serves on every proxy
// of the project (`dev-oidc` in .agnt.kdl). Personas are the identities it
// can issue tokens for; clients are the apps allowed to ask. See
// docs/superpowers/specs/2026-09-27-dev-oidc-design.md.
type DevOIDCConfig struct {
	// Issuer is the fixed `iss` of every token. Empty means the proxy's own
	// loopback origin + /__agnt/oidc.
	Issuer   string                    `kdl:"issuer"`
	Clients  map[string]*DevOIDCClient `kdl:"clients"`
	Personas map[string]*DevPersona    `kdl:"personas"`
	// DefaultPersona is used silently for local logins with no persona
	// cookie, so automated and agent logins never stop at the picker.
	DefaultPersona string `kdl:"default-persona"`
	// Allow maps a verified Cloudflare Access email to the personas that
	// person may assume through a named tunnel. No entry, no persona.
	Allow map[string][]string `kdl:"allow"`
}

// DevOIDCClient is one relying party.
type DevOIDCClient struct {
	RedirectURIs []string `kdl:"redirect-uri"`
	// Secret makes the client confidential. It is a dev-only literal: the
	// issuer only ever mints tokens for this file's personas.
	Secret string `kdl:"secret"`
	// Audience is the access token's aud. Empty means the client id.
	Audience string `kdl:"audience"`
	// LoginPath is where a persona switch sends the app to log in again.
	LoginPath string `kdl:"login-path"`
	// SessionCookies are expired on a persona switch so the app forgets the
	// previous persona's session.
	SessionCookies []string `kdl:"session-cookies"`
}

// DevPersona is one identity the issuer can sign in as.
type DevPersona struct {
	Email  string            `kdl:"email"`
	Name   string            `kdl:"name"`
	Roles  []string          `kdl:"roles"`
	Claims map[string]string `kdl:"claims"`
}

var devNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// devReservedClaims are set by the issuer itself and may not be overridden
// by a persona's claims block.
var devReservedClaims = map[string]bool{
	"iss": true, "sub": true, "aud": true, "exp": true, "iat": true, "nbf": true,
	"nonce": true, "azp": true, "auth_time": true, "scope": true, "client_id": true,
	"email": true, "email_verified": true, "name": true, "preferred_username": true, "roles": true,
}

// Validate rejects a dev-oidc block that is incomplete or ambiguous.
func (c *DevOIDCConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.Issuer != "" {
		u, err := url.Parse(c.Issuer)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("dev-oidc: issuer %q must be an absolute http(s) URL with no query or fragment", c.Issuer)
		}
	}
	if len(c.Clients) == 0 {
		return fmt.Errorf("dev-oidc: declare at least one client in clients { }")
	}
	if len(c.Personas) == 0 {
		return fmt.Errorf("dev-oidc: declare at least one persona in personas { }")
	}
	for _, id := range sortedKeys(c.Clients) {
		cl := c.Clients[id]
		if cl == nil || len(cl.RedirectURIs) == 0 {
			return fmt.Errorf("dev-oidc: client %q needs at least one redirect-uri", id)
		}
		for _, ru := range cl.RedirectURIs {
			if err := devoidc.ValidateRedirectPattern(ru); err != nil {
				return fmt.Errorf("dev-oidc: client %q: %w", id, err)
			}
		}
		if cl.LoginPath != "" && !strings.HasPrefix(cl.LoginPath, "/") {
			return fmt.Errorf("dev-oidc: client %q login-path %q must start with /", id, cl.LoginPath)
		}
	}
	for _, name := range sortedKeys(c.Personas) {
		p := c.Personas[name]
		if !devNamePattern.MatchString(name) {
			return fmt.Errorf("dev-oidc: persona name %q must match %s", name, devNamePattern)
		}
		if p == nil || strings.TrimSpace(p.Email) == "" {
			return fmt.Errorf("dev-oidc: persona %q needs an email", name)
		}
		for k := range p.Claims {
			if devReservedClaims[k] {
				return fmt.Errorf("dev-oidc: persona %q sets reserved claim %q; use the persona's own keys (email, name, roles) instead", name, k)
			}
		}
	}
	if c.DefaultPersona != "" && c.Personas[c.DefaultPersona] == nil {
		return fmt.Errorf("dev-oidc: default-persona %q is not a declared persona", c.DefaultPersona)
	}
	for _, email := range sortedKeys(c.Allow) {
		if !strings.Contains(email, "@") {
			return fmt.Errorf("dev-oidc: allow key %q must be an Access email", email)
		}
		if len(c.Allow[email]) == 0 {
			return fmt.Errorf("dev-oidc: allow %q lists no personas", email)
		}
		for _, name := range c.Allow[email] {
			if c.Personas[name] == nil {
				return fmt.Errorf("dev-oidc: allow %q names undeclared persona %q", email, name)
			}
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
