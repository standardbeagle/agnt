package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
)

const (
	// tailnetCertFetchTimeout bounds one `tailscale cert`. A first issuance
	// goes through the CA and can take tens of seconds; a cached one is
	// immediate.
	tailnetCertFetchTimeout = 90 * time.Second
	// tailnetCertRenewBefore is how close to expiry a served certificate
	// triggers a background refetch. tailscaled renews its certificates with
	// about a third of their 90-day life left, so this asks just after.
	tailnetCertRenewBefore = 30 * 24 * time.Hour
	// tailnetCertRetryAfter spaces refetches after a failed or unchanged
	// renewal, so an expiring certificate does not exec tailscale on every
	// handshake.
	tailnetCertRetryAfter = time.Hour
)

// tailnetTLS serves the tailnet certificate for this node's MagicDNS name on
// a tailnet-bound proxy. The certificate is swapped atomically on renewal, so
// a handshake never waits on tailscale.
type tailnetTLS struct {
	domain       string
	fetch        func(context.Context, string) (*tls.Certificate, error)
	cert         atomic.Pointer[tls.Certificate]
	renewing     atomic.Bool
	lastAttempt  atomic.Int64 // unix nanos of the last renewal attempt
	onRenewError func(error)
	now          func() time.Time
}

func newTailnetTLS(domain string, cert *tls.Certificate, fetch func(context.Context, string) (*tls.Certificate, error)) *tailnetTLS {
	t := &tailnetTLS{domain: domain, fetch: fetch, now: time.Now}
	t.cert.Store(cert)
	return t
}

// detectTailnetTLS decides whether a tailnet-bound proxy serves HTTPS: the
// tailnet must issue a certificate for this node's name, and this user must
// be able to fetch it. When either fails it returns the reason, which the
// proxy reports on start; the proxy then serves plain HTTP.
func detectTailnetTLS(certDomain func(context.Context) string, fetch func(context.Context, string) (*tls.Certificate, error)) (*tailnetTLS, string) {
	if certDomain == nil {
		certDomain = platform.TailscaleCertDomain
	}
	if fetch == nil {
		fetch = platform.TailscaleCert
	}
	domain := certDomain(context.Background())
	if domain == "" {
		return nil, "the tailnet does not issue HTTPS certificates for this node (enable HTTPS in the Tailscale admin console, DNS page)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), tailnetCertFetchTimeout)
	defer cancel()
	cert, err := fetch(ctx, domain)
	if err != nil {
		return nil, err.Error()
	}
	if cert == nil || cert.Leaf == nil {
		return nil, fmt.Sprintf("tailscale cert %s returned no parseable certificate", domain)
	}
	return newTailnetTLS(domain, cert, fetch), ""
}

// getCertificate is the tls.Config hook. It always answers with the current
// certificate and, when that one nears expiry, starts at most one background
// refetch.
func (t *tailnetTLS) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert := t.cert.Load()
	now := t.now()
	if cert.Leaf != nil && now.After(cert.Leaf.NotAfter.Add(-tailnetCertRenewBefore)) &&
		now.UnixNano()-t.lastAttempt.Load() >= int64(tailnetCertRetryAfter) &&
		t.renewing.CompareAndSwap(false, true) {
		t.lastAttempt.Store(now.UnixNano())
		go t.renew(cert)
	}
	return cert, nil
}

func (t *tailnetTLS) renew(current *tls.Certificate) {
	defer t.renewing.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), tailnetCertFetchTimeout)
	defer cancel()
	next, err := t.fetch(ctx, t.domain)
	if err == nil && (next == nil || next.Leaf == nil) {
		err = fmt.Errorf("tailscale cert %s returned no parseable certificate", t.domain)
	}
	if err != nil {
		if t.onRenewError != nil {
			t.onRenewError(err)
		}
		return
	}
	if next.Leaf.NotAfter.After(current.Leaf.NotAfter) {
		t.cert.CompareAndSwap(current, next)
	}
}

// wrap puts TLS on a listener. HTTP/1.1 only: WebSocket upgrades (the agnt
// control channel, dev-server HMR) need it.
func (t *tailnetTLS) wrap(l net.Listener) net.Listener {
	return tls.NewListener(l, &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: t.getCertificate,
		NextProtos:     []string{"http/1.1"},
	})
}
