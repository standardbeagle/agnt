package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
)

func devBlock(personas ...string) *config.DevOIDCConfig {
	b := &config.DevOIDCConfig{
		Issuer:         "https://dev.example.com/__agnt/oidc",
		Clients:        map[string]*config.DevOIDCClient{"app": {RedirectURIs: []string{"http://localhost:*/cb"}, Secret: "s", LoginPath: "/login"}},
		Personas:       map[string]*config.DevPersona{},
		DefaultPersona: personas[0],
		Allow:          map[string][]string{"a@x.com": {personas[0]}},
	}
	for _, p := range personas {
		b.Personas[p] = &config.DevPersona{Email: p + "@x.com", Name: "Display " + p, Roles: []string{p}}
	}
	return b
}

func TestDevIssuerForSharesOneIssuerPerProject(t *testing.T) {
	d := &Daemon{}
	first := d.devIssuerFor("/proj/a", devBlock("standard"), nil)
	if first == nil {
		t.Fatal("no issuer for a declared block")
	}
	again := d.devIssuerFor("/proj/a/", devBlock("standard", "admin"), nil)
	if again != first {
		t.Fatal("a second proxy of the same project got a different issuer (different signing key)")
	}
	if _, ok := first.Config().Personas["admin"]; !ok {
		t.Fatal("an edited block did not update the shared issuer")
	}
	if other := d.devIssuerFor("/proj/b", devBlock("standard"), nil); other == first {
		t.Fatal("two projects share an issuer")
	}
	if d.devIssuerFor("/proj/a", nil, nil) != nil {
		t.Fatal("removing the block must remove the issuer")
	}
	if fresh := d.devIssuerFor("/proj/a", devBlock("standard"), nil); fresh == first {
		t.Fatal("a re-added block reused the retired issuer")
	}
}

func TestDevIssuerConfigTranslation(t *testing.T) {
	cfg := devIssuerConfig(devBlock("standard", "admin"))
	p := cfg.Personas["admin"]
	if p.Name != "admin" || p.DisplayName != "Display admin" || p.Email != "admin@x.com" || p.Roles[0] != "admin" {
		t.Fatalf("persona: %+v", p)
	}
	c := cfg.Clients["app"]
	if c.ID != "app" || c.Secret != "s" || c.LoginPath != "/login" || c.RedirectURIs[0] != "http://localhost:*/cb" {
		t.Fatalf("client: %+v", c)
	}
	if cfg.Issuer != "https://dev.example.com/__agnt/oidc" || cfg.DefaultPersona != "standard" || cfg.Allow["a@x.com"][0] != "standard" {
		t.Fatalf("config: %+v", cfg)
	}
}
