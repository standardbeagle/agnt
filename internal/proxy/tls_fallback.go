package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
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
	// secure and insecure are swappable so FlushConnections can replace their
	// connection pools without replacing this wrapper (and losing skipped).
	secure   *swapTransport // original transport (verifies certs)
	insecure *swapTransport // cloned transport with InsecureSkipVerify

	skipped   atomic.Bool         // set on first cert error
	onSkipped func(certErr error) // called once when fallback activates

	// allow decides, on the first certificate error, whether host is a target
	// the bypass may apply to. Nil allows every host (direct constructors and
	// tests); NewProxyServer installs isLocalFallbackTarget. A refused host
	// keeps verification on and gets the certificate error back.
	allow func(host string) bool
	// onRefused is called once, with the certificate error, the first time
	// allow refuses a host, so a failing remote target is not a silent 502.
	onRefused   func(certErr error)
	refusedOnce atomic.Bool
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
		secure:    newSwapTransport(base),
		insecure:  newSwapTransport(insecure),
		onSkipped: onSkipped,
	}
}

// flush replaces both connection pools. The wrapper and its skipped state are
// untouched, so a backend already known to need the bypass keeps it.
func (t *TLSFallbackTransport) flush() {
	t.secure.flush()
	t.insecure.flush()
}

// closeIdle releases idle connections on both pools.
func (t *TLSFallbackTransport) closeIdle() {
	t.secure.closeIdle()
	t.insecure.closeIdle()
}

// swapTransport is an http.RoundTripper over an *http.Transport that can be
// replaced while requests are in flight. RoundTrip loads the current transport
// atomically, so flush never races it.
type swapTransport struct {
	cur atomic.Pointer[http.Transport]
}

func newSwapTransport(t *http.Transport) *swapTransport {
	s := &swapTransport{}
	s.cur.Store(t)
	return s
}

// RoundTrip implements http.RoundTripper on the current transport.
func (s *swapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.cur.Load().RoundTrip(req)
}

// flush swaps in a clone (fresh connection pool, same dial/TLS/timeout
// settings) and closes the old pool's idle connections. In-flight requests
// finish on the old transport. Concurrent flushes coalesce: only the one that
// wins the swap closes the old pool.
func (s *swapTransport) flush() {
	old := s.cur.Load()
	if s.cur.CompareAndSwap(old, old.Clone()) {
		old.CloseIdleConnections()
	}
}

func (s *swapTransport) closeIdle() {
	s.cur.Load().CloseIdleConnections()
}

// tlsFallbackLookupTimeout bounds the name lookup isLocalFallbackTarget makes.
const tlsFallbackLookupTimeout = 2 * time.Second

// isLocalFallbackTarget reports whether host is a target the automatic TLS
// bypass may apply to: loopback, private, link-local or tailnet. A name is
// judged by every address it resolves to, so a public host with a broken (or
// intercepted) certificate is never silently downgraded — that needs an
// explicit skip-tls-verify.
func isLocalFallbackTarget(host string) bool {
	return isLocalFallbackTargetWith(host, func(ctx context.Context, h string) ([]net.IPAddr, error) {
		return net.DefaultResolver.LookupIPAddr(ctx, h)
	})
}

func isLocalFallbackTargetWith(host string, lookup func(context.Context, string) ([]net.IPAddr, error)) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isLocalFallbackIP(ip)
	}
	ctx, cancel := context.WithTimeout(context.Background(), tlsFallbackLookupTimeout)
	defer cancel()
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !isLocalFallbackIP(a.IP) {
			return false
		}
	}
	return true
}

func isLocalFallbackIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		platform.IsTailnetPeerAddress(ip.String())
}

// RoundTrip implements http.RoundTripper. When TLS cert verification fails
// and the target is one the bypass may apply to (see allow), it retries
// without verification and fires onSkipped.
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

	if t.allow != nil && !t.allow(req.URL.Hostname()) {
		if t.refusedOnce.CompareAndSwap(false, true) && t.onRefused != nil {
			t.onRefused(err)
		}
		return nil, err
	}

	// First cert error: activate fallback and notify caller once.
	if t.skipped.CompareAndSwap(false, true) && t.onSkipped != nil {
		t.onSkipped(err)
	}

	// Retry without verification. The failed RoundTrip has closed the
	// request body, so a request with one is retried only when it can be
	// replayed (GetBody); otherwise this request fails with the certificate
	// error, and the bypass, now active, serves the next one.
	retry := req
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, fmt.Errorf("TLS verification bypass enabled, but this request's body cannot be replayed; retry the request: %w", err)
		}
		body, gerr := req.GetBody()
		if gerr != nil {
			return nil, fmt.Errorf("TLS verification bypass enabled, but replaying the request body failed (%v): %w", gerr, err)
		}
		retry = req.Clone(req.Context())
		retry.Body = body
	}
	return t.insecure.RoundTrip(retry)
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
