package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tailnetTestDomain = "node.tnet.ts.net"

// tailnetTestCert is a self-signed certificate for host, standing in for the
// one tailscaled issues.
func tailnetTestCert(t *testing.T, host string, notAfter time.Time) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// tailnetBindConfig is a tailnet-bound proxy config with every tailscale
// lookup stubbed; it is only constructed, never started (the stub address is
// not an interface on the test host).
func tailnetBindConfig(domain func(context.Context) string, fetch func(context.Context, string) (*tls.Certificate, error)) ProxyConfig {
	return ProxyConfig{
		ID:                "tn",
		TargetURL:         "http://127.0.0.1:1",
		ListenPort:        31536,
		MaxLogSize:        10,
		BindAddress:       BindTailscale,
		TailnetIP:         stubTailnetIP("100.101.102.103"),
		TailnetCertDomain: domain,
		TailnetCert:       fetch,
	}
}

func TestNewProxyServer_TailnetHTTPSDetection(t *testing.T) {
	cert := tailnetTestCert(t, tailnetTestDomain, time.Now().Add(90*24*time.Hour))
	offered := func(context.Context) string { return tailnetTestDomain }

	t.Run("tailnet issues certificates and this user may fetch one", func(t *testing.T) {
		var fetched atomic.Int32
		ps, err := NewProxyServer(tailnetBindConfig(offered, func(_ context.Context, d string) (*tls.Certificate, error) {
			fetched.Add(1)
			if d != tailnetTestDomain {
				t.Errorf("fetched a certificate for %q, want the MagicDNS name", d)
			}
			return cert, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := ps.ListenerOrigin(), "https://"+tailnetTestDomain+":31536"; got != want {
			t.Errorf("ListenerOrigin = %q, want %q", got, want)
		}
		if ps.tailnetHTTPSReason != "" {
			t.Errorf("no reason to report when https is on, got %q", ps.tailnetHTTPSReason)
		}
		if fetched.Load() != 1 {
			t.Errorf("certificate fetched %d times at construction, want 1", fetched.Load())
		}
	})

	t.Run("tailnet does not issue certificates", func(t *testing.T) {
		ps, err := NewProxyServer(tailnetBindConfig(func(context.Context) string { return "" },
			func(context.Context, string) (*tls.Certificate, error) {
				t.Error("no certificate may be fetched when the tailnet offers none")
				return nil, errors.New("unreachable")
			}))
		if err != nil {
			t.Fatal(err)
		}
		if o := ps.ListenerOrigin(); !strings.HasPrefix(o, "http://") {
			t.Errorf("ListenerOrigin = %q, want http", o)
		}
		if !strings.Contains(ps.tailnetHTTPSReason, "HTTPS") {
			t.Errorf("reason must say the tailnet has HTTPS off, got %q", ps.tailnetHTTPSReason)
		}
	})

	t.Run("tailnet issues certificates but this user may not fetch one", func(t *testing.T) {
		ps, err := NewProxyServer(tailnetBindConfig(offered, func(context.Context, string) (*tls.Certificate, error) {
			return nil, errors.New("tailscale cert node.tnet.ts.net: Access denied: cert access denied (let this user ...)")
		}))
		if err != nil {
			t.Fatalf("a refused certificate must not refuse the proxy: %v", err)
		}
		if o := ps.ListenerOrigin(); !strings.HasPrefix(o, "http://") {
			t.Errorf("ListenerOrigin = %q, want http", o)
		}
		if !strings.Contains(ps.tailnetHTTPSReason, "cert access denied") {
			t.Errorf("reason must carry the fetch error, got %q", ps.tailnetHTTPSReason)
		}
	})

	t.Run("loopback proxies never look", func(t *testing.T) {
		ps, err := NewProxyServer(ProxyConfig{
			ID: "lo", TargetURL: "http://127.0.0.1:1", ListenPort: 0, MaxLogSize: 10,
			TailnetCertDomain: func(context.Context) string { t.Error("loopback proxy asked tailscale"); return "" },
		})
		if err != nil {
			t.Fatal(err)
		}
		if ps.tailnetTLS != nil || ps.tailnetHTTPSReason != "" {
			t.Errorf("loopback proxy has tailnet TLS state: %v %q", ps.tailnetTLS, ps.tailnetHTTPSReason)
		}
	})
}

// A proxy with tailnet TLS serves HTTPS with the tailnet certificate, and the
// app behind it is told the browser used https. The listener is loopback here
// because the test host has no tailnet address; the TLS wiring is the same.
func TestTailnetTLS_ServesTheTailnetCertificate(t *testing.T) {
	var gotProto atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto.Store(r.Header.Get("X-Forwarded-Proto"))
		io.WriteString(w, "app")
	}))
	defer upstream.Close()

	ps, err := NewProxyServer(ProxyConfig{ID: "tls", TargetURL: upstream.URL, ListenPort: 0, MaxLogSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	cert := tailnetTestCert(t, tailnetTestDomain, time.Now().Add(90*24*time.Hour))
	ps.tailnetTLS = newTailnetTLS(tailnetTestDomain, cert, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ps.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer ps.Stop(context.Background())

	port := strconv.Itoa(ps.BoundPort())
	if got, want := ps.ListenerOrigin(), "https://"+tailnetTestDomain+":"+port; got != want {
		t.Errorf("ListenerOrigin = %q, want %q", got, want)
	}
	if got, want := ps.URL(), "https://"+tailnetTestDomain+":"+port; got != want {
		t.Errorf("URL = %q, want %q (the certificate names the host, not the address)", got, want)
	}
	if got := ps.Stats().URL; got != ps.URL() {
		t.Errorf("Stats().URL = %q, want %q", got, ps.URL())
	}

	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	addr := ps.ListenAddr
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	resp, err := client.Get("https://" + tailnetTestDomain + ":" + port + "/")
	if err != nil {
		t.Fatalf("https GET with the tailnet certificate trusted: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "app") {
		t.Fatalf("https GET = %d %q, want the app", resp.StatusCode, body)
	}
	if p, _ := gotProto.Load().(string); p != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https (the app builds redirect URIs from it)", p)
	}
	if st := resp.TLS; st == nil || len(st.NegotiatedProtocol) > 0 && st.NegotiatedProtocol != "http/1.1" {
		t.Errorf("want TLS with http/1.1 (WebSocket upgrades need it), got %+v", st)
	}

	// Plain HTTP on the same port is not served as the app.
	plain, err := http.Get("http://" + addr + "/")
	if err == nil {
		b, _ := io.ReadAll(plain.Body)
		plain.Body.Close()
		if plain.StatusCode == http.StatusOK || strings.Contains(string(b), "app") {
			t.Errorf("plain http reached the app: %d %q", plain.StatusCode, b)
		}
	}
}

func TestTailnetTLS_Renewal(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	fresh := tailnetTestCert(t, tailnetTestDomain, now.Add(80*24*time.Hour))
	expiring := tailnetTestCert(t, tailnetTestDomain, now.Add(10*24*time.Hour))
	renewed := tailnetTestCert(t, tailnetTestDomain, now.Add(90*24*time.Hour))

	t.Run("a certificate far from expiry is served without fetching", func(t *testing.T) {
		st := newTailnetTLS(tailnetTestDomain, fresh, func(context.Context, string) (*tls.Certificate, error) {
			t.Error("fetched while the certificate was fresh")
			return nil, nil
		})
		st.now = func() time.Time { return now }
		for i := 0; i < 5; i++ {
			if c, _ := st.getCertificate(nil); c != fresh {
				t.Fatal("served another certificate")
			}
		}
	})

	t.Run("an expiring certificate is renewed once, in the background", func(t *testing.T) {
		release := make(chan struct{})
		var fetches atomic.Int32
		st := newTailnetTLS(tailnetTestDomain, expiring, func(context.Context, string) (*tls.Certificate, error) {
			fetches.Add(1)
			<-release
			return renewed, nil
		})
		st.now = func() time.Time { return now }
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if c, _ := st.getCertificate(nil); c != expiring {
					t.Error("a handshake during renewal must get the current certificate, not wait")
				}
			}()
		}
		wg.Wait()
		close(release)
		deadline := time.Now().Add(5 * time.Second)
		for st.cert.Load() != renewed {
			if time.Now().After(deadline) {
				t.Fatal("renewed certificate never installed")
			}
			time.Sleep(time.Millisecond)
		}
		if n := fetches.Load(); n != 1 {
			t.Errorf("%d concurrent handshakes caused %d fetches, want 1", 20, n)
		}
	})

	t.Run("a failed renewal is reported and not retried on every handshake", func(t *testing.T) {
		var fetches atomic.Int32
		reported := make(chan error, 4)
		st := newTailnetTLS(tailnetTestDomain, expiring, func(context.Context, string) (*tls.Certificate, error) {
			fetches.Add(1)
			return nil, errors.New("tailscale cert: boom")
		})
		st.onRenewError = func(err error) { reported <- err }
		clock := now
		st.now = func() time.Time { return clock }

		st.getCertificate(nil)
		select {
		case err := <-reported:
			if !strings.Contains(err.Error(), "boom") {
				t.Errorf("reported %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("renewal failure never reported")
		}
		for st.renewing.Load() {
			time.Sleep(time.Millisecond)
		}
		for i := 0; i < 10; i++ {
			if c, _ := st.getCertificate(nil); c != expiring {
				t.Fatal("a failed renewal must keep serving the certificate it has")
			}
		}
		if n := fetches.Load(); n != 1 {
			t.Errorf("fetched %d times inside the retry window, want 1", n)
		}
		clock = now.Add(tailnetCertRetryAfter + time.Minute)
		st.getCertificate(nil)
		<-reported
		if n := fetches.Load(); n != 2 {
			t.Errorf("after the retry window: %d fetches, want 2", n)
		}
	})
}
