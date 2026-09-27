package config

import (
	"strings"
	"testing"
)

const devOIDCKDL = `dev-oidc {
    issuer "https://dev.example.com/__agnt/oidc"
    clients {
        story-web {
            redirect-uri "http://localhost:*/auth/callback" "https://dev.example.com/auth/callback"
            secret "dev-only"
            audience "story-api"
            login-path "/auth/login"
            session-cookies "story.sid"
        }
    }
    personas {
        standard {
            email "std@example.com"
            name "Standard"
            roles "user"
        }
        admin {
            email "admin@example.com"
            roles "admin" "user"
            claims {
                tenant "acme"
            }
        }
    }
    default-persona "standard"
    allow {
        "andy@example.com" "standard" "admin"
    }
}`

func TestParseDevOIDC(t *testing.T) {
	cfg, err := ParseAgntConfig(devOIDCKDL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := cfg.DevOIDC
	if d == nil {
		t.Fatal("dev-oidc block not parsed")
	}
	cl := d.Clients["story-web"]
	if d.Issuer != "https://dev.example.com/__agnt/oidc" || cl == nil || len(cl.RedirectURIs) != 2 ||
		cl.Secret != "dev-only" || cl.Audience != "story-api" || cl.LoginPath != "/auth/login" ||
		len(cl.SessionCookies) != 1 || cl.SessionCookies[0] != "story.sid" {
		t.Fatalf("client not parsed into its fields: %+v / %+v", d, cl)
	}
	admin := d.Personas["admin"]
	if admin == nil || admin.Email != "admin@example.com" || len(admin.Roles) != 2 || admin.Claims["tenant"] != "acme" {
		t.Fatalf("persona not parsed: %+v", admin)
	}
	if d.DefaultPersona != "standard" || strings.Join(d.Allow["andy@example.com"], ",") != "standard,admin" {
		t.Fatalf("default/allow not parsed: %+v", d)
	}
}

func TestDevOIDCValidateRefusals(t *testing.T) {
	base := func() *DevOIDCConfig {
		return &DevOIDCConfig{
			Clients:  map[string]*DevOIDCClient{"web": {RedirectURIs: []string{"http://localhost:*/cb"}}},
			Personas: map[string]*DevPersona{"std": {Email: "s@x.com"}, "admin": {Email: "a@x.com"}},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("premise: baseline valid: %v", err)
	}
	cases := map[string]struct {
		mut  func(*DevOIDCConfig)
		want string
	}{
		"issuer not a URL":        {func(c *DevOIDCConfig) { c.Issuer = "dev.example.com" }, "issuer"},
		"issuer with query":       {func(c *DevOIDCConfig) { c.Issuer = "https://x.com/o?a=1" }, "issuer"},
		"no clients":              {func(c *DevOIDCConfig) { c.Clients = nil }, "at least one client"},
		"no personas":             {func(c *DevOIDCConfig) { c.Personas = nil }, "at least one persona"},
		"client without redirect": {func(c *DevOIDCConfig) { c.Clients["web"].RedirectURIs = nil }, "redirect-uri"},
		"redirect fragment":       {func(c *DevOIDCConfig) { c.Clients["web"].RedirectURIs = []string{"http://localhost:3000/cb#x"} }, "fragment"},
		"redirect relative":       {func(c *DevOIDCConfig) { c.Clients["web"].RedirectURIs = []string{"/cb"} }, "absolute"},
		"redirect host wildcard":  {func(c *DevOIDCConfig) { c.Clients["web"].RedirectURIs = []string{"https://*.example.com/cb"} }, "wildcard"},
		"redirect path wildcard":  {func(c *DevOIDCConfig) { c.Clients["web"].RedirectURIs = []string{"http://localhost:3000/*"} }, "wildcard"},
		"port wildcard off loopback": {func(c *DevOIDCConfig) {
			c.Clients["web"].RedirectURIs = []string{"https://dev.example.com:*/cb"}
		}, "only allowed on localhost"},
		"login-path relative":   {func(c *DevOIDCConfig) { c.Clients["web"].LoginPath = "auth/login" }, "login-path"},
		"persona name":          {func(c *DevOIDCConfig) { c.Personas["Bad Name"] = &DevPersona{Email: "b@x.com"} }, "persona name"},
		"persona without email": {func(c *DevOIDCConfig) { c.Personas["std"].Email = " " }, "needs an email"},
		"reserved claim":        {func(c *DevOIDCConfig) { c.Personas["std"].Claims = map[string]string{"sub": "root"} }, "reserved claim"},
		"unknown default":       {func(c *DevOIDCConfig) { c.DefaultPersona = "ghost" }, "default-persona"},
		"allow not an email":    {func(c *DevOIDCConfig) { c.Allow = map[string][]string{"andy": {"std"}} }, "Access email"},
		"allow empty":           {func(c *DevOIDCConfig) { c.Allow = map[string][]string{"a@x.com": nil} }, "no personas"},
		"allow unknown persona": {func(c *DevOIDCConfig) { c.Allow = map[string][]string{"a@x.com": {"root"}} }, "undeclared persona"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			tc.mut(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	var absent *DevOIDCConfig
	if err := absent.Validate(); err != nil {
		t.Fatalf("absent block must validate: %v", err)
	}
	if _, err := ParseAgntConfig("dev-oidc {\n    issuer \"nope\"\n}"); err == nil {
		t.Fatal("ParseAgntConfig accepted an invalid dev-oidc block")
	}
}
