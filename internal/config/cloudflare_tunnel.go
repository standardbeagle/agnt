package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// CloudflareTunnelConfig declares a named Cloudflare tunnel in front of one
// proxy (`cloudflare-tunnel` inside a proxy block). Declaring it makes the
// proxy public at Hostname, so it must also declare an Access application
// for the proxy to verify, or opt out explicitly with allow-unauthenticated.
//
// The tunnel, its DNS route and its credential file are provisioned outside
// agnt with the Cloudflare account token; agnt only runs the tunnel.
type CloudflareTunnelConfig struct {
	// ID is the tunnel UUID (the target of the hostname's CNAME).
	ID string `kdl:"id"`
	// Hostname is the public hostname routed to the tunnel, without scheme.
	Hostname string `kdl:"hostname"`
	// CredentialsFile is the tunnel credential JSON. A leading ~/ expands to
	// the home directory; a relative path resolves against the project.
	CredentialsFile string `kdl:"credentials-file"`
	// Access is the Cloudflare Access application protecting Hostname. Every
	// request arriving through the tunnel must carry a token it issued.
	Access *CloudflareAccessConfig `kdl:"access"`
	// AllowUnauthenticated serves the tunnel with no Access check. It exists
	// so the choice to run a public, unauthenticated dev proxy is written
	// down rather than defaulted into.
	AllowUnauthenticated bool `kdl:"allow-unauthenticated"`
}

// CloudflareAccessConfig identifies the Access application whose tokens the
// proxy accepts.
type CloudflareAccessConfig struct {
	// TeamDomain is the Access team hostname, <team>.cloudflareaccess.com.
	TeamDomain string `kdl:"team-domain"`
	// AUD is the Access application's Audience tag.
	AUD string `kdl:"aud"`
}

// AccessTeamDomainSuffix is the only domain an Access team hostname can have.
const AccessTeamDomainSuffix = ".cloudflareaccess.com"

// NormalizeAccessTeamDomain lowercases and validates an Access team domain.
// The proxy fetches signing keys from this host, so anything other than a
// single label under cloudflareaccess.com is refused.
func NormalizeAccessTeamDomain(domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	label := strings.TrimSuffix(domain, AccessTeamDomainSuffix)
	if label == domain || label == "" || strings.ContainsAny(label, "./:@") {
		return "", fmt.Errorf("access team-domain %q must be <team>%s", domain, AccessTeamDomainSuffix)
	}
	return domain, nil
}

// Validate rejects a cloudflare-tunnel block that is incomplete or that
// would expose the proxy without either an Access check or an explicit
// opt-out.
func (c *CloudflareTunnelConfig) Validate() error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("cloudflare-tunnel: id is required (the tunnel UUID)")
	}
	if err := validateTunnelHostname(c.Hostname); err != nil {
		return fmt.Errorf("cloudflare-tunnel: %w", err)
	}
	if strings.TrimSpace(c.CredentialsFile) == "" {
		return fmt.Errorf("cloudflare-tunnel: credentials-file is required")
	}
	switch {
	case c.Access != nil && c.AllowUnauthenticated:
		return fmt.Errorf("cloudflare-tunnel: access and allow-unauthenticated contradict each other; keep one")
	case c.Access == nil && !c.AllowUnauthenticated:
		return fmt.Errorf("cloudflare-tunnel: %s would be public with no authentication; add an access { team-domain aud } block, or allow-unauthenticated true to accept that", c.Hostname)
	case c.Access != nil:
		if _, err := NormalizeAccessTeamDomain(c.Access.TeamDomain); err != nil {
			return fmt.Errorf("cloudflare-tunnel: %w", err)
		}
		if strings.TrimSpace(c.Access.AUD) == "" {
			return fmt.Errorf("cloudflare-tunnel: access aud is required (the Access application's Audience tag)")
		}
	}
	return nil
}

// CredentialsPath resolves CredentialsFile: ~/ against the home directory,
// a relative path against projectDir.
func (c *CloudflareTunnelConfig) CredentialsPath(projectDir string) (string, error) {
	p := strings.TrimSpace(c.CredentialsFile)
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cloudflare-tunnel: resolving ~ in credentials-file: %w", err)
		}
		return filepath.Join(home, rest), nil
	}
	if !filepath.IsAbs(p) {
		return filepath.Join(projectDir, p), nil
	}
	return p, nil
}

// validateTunnelHostname accepts a bare DNS name: no scheme, port, path or
// wildcard, since it is used verbatim as the public URL's host.
func validateTunnelHostname(h string) error {
	if h == "" {
		return fmt.Errorf("hostname is required")
	}
	if strings.ContainsAny(h, "/:*@ ") || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") || !strings.Contains(h, ".") {
		return fmt.Errorf("hostname %q must be a bare DNS name like dev.example.com", h)
	}
	if net.ParseIP(h) != nil {
		return fmt.Errorf("hostname %q is an IP address; a named tunnel is reached by DNS name", h)
	}
	return nil
}
