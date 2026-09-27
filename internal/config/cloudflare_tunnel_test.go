package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tunnelKDLHead = `proxies {
    dev {
        url "http://localhost:5173"
        cloudflare-tunnel {
            id "6ff42ae2-765d-4adf-8112-31c55c1551ef"
            hostname "dev.example.com"
            credentials-file "~/.config/cloudflared/dev.json"
`

func TestParseCloudflareTunnel(t *testing.T) {
	cfg, err := ParseAgntConfig(tunnelKDLHead + `
            access {
                team-domain "beagle.cloudflareaccess.com"
                aud "aud-tag"
            }
        }
    }
}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ct := cfg.Proxies["dev"].CloudflareTunnel
	if ct == nil || ct.ID != "6ff42ae2-765d-4adf-8112-31c55c1551ef" || ct.Hostname != "dev.example.com" ||
		ct.Access == nil || ct.Access.TeamDomain != "beagle.cloudflareaccess.com" || ct.Access.AUD != "aud-tag" || ct.AllowUnauthenticated {
		t.Fatalf("cloudflare-tunnel block not parsed into its fields: %+v", ct)
	}

	cfg, err = ParseAgntConfig(tunnelKDLHead + `
            allow-unauthenticated true
        }
    }
}`)
	if err != nil {
		t.Fatalf("parse opt-out: %v", err)
	}
	if !cfg.Proxies["dev"].CloudflareTunnel.AllowUnauthenticated {
		t.Fatal("allow-unauthenticated not parsed")
	}
}

func TestParseCloudflareTunnelRefusals(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"no access, no opt-out": {"", "public with no authentication"},
		"both access and opt-out": {`allow-unauthenticated true
            access { team-domain "beagle.cloudflareaccess.com"; aud "a"; }`, "contradict"},
		"team domain off cloudflareaccess.com": {`access { team-domain "evil.com"; aud "a"; }`, "team-domain"},
		"missing aud":                          {`access { team-domain "beagle.cloudflareaccess.com"; }`, "aud is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAgntConfig(tunnelKDLHead + tc.body + "\n        }\n    }\n}")
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `proxy "dev"`) {
				t.Fatalf("err = %v, want it to name proxy \"dev\" and contain %q", err, tc.want)
			}
		})
	}
}

func TestCloudflareTunnelValidateFields(t *testing.T) {
	ok := func() *CloudflareTunnelConfig {
		return &CloudflareTunnelConfig{ID: "id", Hostname: "dev.example.com", CredentialsFile: "c.json", AllowUnauthenticated: true}
	}
	if err := ok().Validate(); err != nil {
		t.Fatalf("premise: baseline valid: %v", err)
	}
	mutations := map[string]func(*CloudflareTunnelConfig){
		"no id":             func(c *CloudflareTunnelConfig) { c.ID = " " },
		"no credentials":    func(c *CloudflareTunnelConfig) { c.CredentialsFile = "" },
		"no hostname":       func(c *CloudflareTunnelConfig) { c.Hostname = "" },
		"hostname scheme":   func(c *CloudflareTunnelConfig) { c.Hostname = "https://dev.example.com" },
		"hostname port":     func(c *CloudflareTunnelConfig) { c.Hostname = "dev.example.com:443" },
		"hostname path":     func(c *CloudflareTunnelConfig) { c.Hostname = "dev.example.com/x" },
		"hostname wildcard": func(c *CloudflareTunnelConfig) { c.Hostname = "*.example.com" },
		"hostname bare":     func(c *CloudflareTunnelConfig) { c.Hostname = "localhost" },
		"hostname IP":       func(c *CloudflareTunnelConfig) { c.Hostname = "10.0.0.1" },
	}
	for name, mutate := range mutations {
		c := ok()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var nilCfg *CloudflareTunnelConfig
	if err := nilCfg.Validate(); err != nil {
		t.Errorf("absent block must validate: %v", err)
	}
}

func TestCloudflareTunnelCredentialsPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for in, want := range map[string]string{
		"~/.config/cloudflared/dev.json": filepath.Join(home, ".config/cloudflared/dev.json"),
		"secrets/dev.json":               filepath.Join("/proj", "secrets/dev.json"),
		"/etc/cloudflared/dev.json":      "/etc/cloudflared/dev.json",
	} {
		got, err := (&CloudflareTunnelConfig{CredentialsFile: in}).CredentialsPath("/proj")
		if err != nil || got != want {
			t.Errorf("CredentialsPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
