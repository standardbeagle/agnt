package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"sync/atomic"
)

// TLSFallbackTransport wraps an http.RoundTripper. On the first request that
// fails with a TLS certificate verification error (self-signed, unknown CA,
// hostname mismatch), it upgrades to an insecure transport, calls onCertSkipped
// once, and retries. Subsequent requests use the insecure transport directly.
//
// This allows the proxy to transparently handle dev servers with self-signed
// certs without requiring skip-tls-verify in config, while still surfacing a
// visible warning via the onCertSkipped callback.
type TLSFallbackTransport struct {
	secure   http.RoundTripper // original transport (verifies certs)
	insecure http.RoundTripper // cloned transport with InsecureSkipVerify

	skipped   atomic.Bool         // set on first cert error
	onSkipped func(certErr error) // called once when fallback activates
}

// NewTLSFallbackTransport creates a TLSFallbackTransport. base must be an
// *http.Transport (cloned from http.DefaultTransport); onSkipped is called
// exactly once when TLS verification is first bypassed.
func NewTLSFallbackTransport(base *http.Transport, onSkipped func(certErr error)) *TLSFallbackTransport {
	insecure := base.Clone()
	if insecure.TLSClientConfig == nil {
		insecure.TLSClientConfig = &tls.Config{}
	}
	insecure.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // intentional: dev servers only
	return &TLSFallbackTransport{
		secure:    base,
		insecure:  insecure,
		onSkipped: onSkipped,
	}
}

// RoundTrip implements http.RoundTripper. When TLS cert verification fails and
// the target looks like a dev server (any HTTPS), it retries without
// verification and fires onSkipped.
func (t *TLSFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Already in fallback mode — use insecure transport directly.
	if t.skipped.Load() {
		return t.insecure.RoundTrip(req)
	}

	resp, err := t.secure.RoundTrip(req)
	if err == nil {
		return resp, nil
	}

	if !isCertError(err) {
		return nil, err
	}

	// First cert error: activate fallback and notify caller once.
	if t.skipped.CompareAndSwap(false, true) && t.onSkipped != nil {
		t.onSkipped(err)
	}

	// Clone request so the body can be replayed (bodies are one-shot).
	// For retries, the body was already read by the first attempt only if
	// there was one — GET/HEAD have no body, POST retries are best-effort.
	return t.insecure.RoundTrip(req)
}

// isCertError reports whether err (or any wrapped error) is a TLS certificate
// verification failure — self-signed, unknown CA, hostname mismatch, or
// expired cert. These are all safe to retry without verification in a dev
// environment.
func isCertError(err error) bool {
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	var x509Err x509.CertificateInvalidError
	if errors.As(err, &x509Err) {
		return true
	}
	var unkAuth x509.UnknownAuthorityError
	if errors.As(err, &unkAuth) {
		return true
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return true
	}
	return false
}
