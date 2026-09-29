package platform

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// TailscaleCertDomain returns this node's MagicDNS name when its tailnet issues
// HTTPS certificates for it (HTTPS enabled in the tailnet admin console), and
// "" otherwise, including when tailscale is unavailable or the lookup times out.
//
// It says only that the tailnet offers a certificate; whether this user may
// fetch it is answered by TailscaleCert.
func TailscaleCertDomain(ctx context.Context) string {
	output := tailscaleStatusJSON(ctx)
	if output == nil {
		return ""
	}
	return parseTailscaleCertDomain(output)
}

// parseTailscaleCertDomain returns Self.DNSName (lowercased, no trailing dot)
// when it appears in CertDomains. Split out for testability.
func parseTailscaleCertDomain(output []byte) string {
	var status struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
		CertDomains []string `json:"CertDomains"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return ""
	}
	self := strings.ToLower(strings.TrimSuffix(status.Self.DNSName, "."))
	if self == "" {
		return ""
	}
	for _, d := range status.CertDomains {
		if strings.EqualFold(strings.TrimSuffix(d, "."), self) {
			return self
		}
	}
	return ""
}

// TailscaleCert fetches the TLS certificate tailscaled holds for domain,
// issuing or renewing it first when needed (the first issuance can take tens
// of seconds, so give ctx a generous deadline). The key is read from the
// command's stdout and never written to disk.
//
// Non-root users need to be tailscale's operator; the error for that case
// names the command that grants it.
func TailscaleCert(ctx context.Context, domain string) (*tls.Certificate, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "tailscale", "cert", "--cert-file=-", "--key-file=-", domain)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, tailscaleCertError(domain, stderr.Bytes(), err)
	}
	return parseTailscaleCertPair(stdout.Bytes())
}

// parseTailscaleCertPair reads the certificate chain and key that `tailscale
// cert --cert-file=- --key-file=-` prints to one stream, and parses the leaf so
// its expiry can drive renewal.
func parseTailscaleCertPair(out []byte) (*tls.Certificate, error) {
	// X509KeyPair takes the CERTIFICATE blocks from its first argument and
	// the first key block from its second, skipping the rest, so the combined
	// stream serves as both.
	cert, err := tls.X509KeyPair(out, out)
	if err != nil {
		return nil, fmt.Errorf("tailscale cert output: %w", err)
	}
	return &cert, nil
}

// tailscaleCertError turns a failed `tailscale cert` into an error that names
// the cause from its stderr, and the fix when the cause is the usual one.
func tailscaleCertError(domain string, stderr []byte, runErr error) error {
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(stderr)), "\n", 2)[0])
	if first == "" {
		return fmt.Errorf("tailscale cert %s: %w", domain, runErr)
	}
	if strings.Contains(strings.ToLower(first), "access denied") {
		return fmt.Errorf("tailscale cert %s: %s (let this user fetch certificates with `sudo tailscale set --operator=$USER`)", domain, first)
	}
	return fmt.Errorf("tailscale cert %s: %s: %w", domain, first, runErr)
}
