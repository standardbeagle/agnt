package proxy

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"
)

// TestCopyBindConfigTo_KeepsATailnetBindAcrossARebuild pins the restart path:
// PROXY RESTART rebuilds the proxy from the running server's settings. It used
// to copy the resolved BindAddress (the node's 100.x address), which re-entered
// classifyBindAddress as a plain external IP and was refused for lacking
// allow_external — a tailnet bind never needs it — after Replace had already
// stopped the old server, so the proxy stayed down. The rebuild must see the
// configured "tailscale" spelling and the same tailnet lookups, and so come
// back tailnet-bound, without allow_external, on https when the first one was.
func TestCopyBindConfigTo_KeepsATailnetBindAcrossARebuild(t *testing.T) {
	cert := tailnetTestCert(t, tailnetTestDomain, time.Now().Add(90*24*time.Hour))
	offered := func(context.Context) string { return tailnetTestDomain }
	fetch := func(context.Context, string) (*tls.Certificate, error) { return cert, nil }

	first, err := NewProxyServer(tailnetBindConfig(offered, fetch))
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	rebuilt := ProxyConfig{ID: first.ID, TargetURL: first.TargetURL.String(), ListenPort: 31536, MaxLogSize: 10}
	first.CopyBindConfigTo(&rebuilt)
	if rebuilt.BindAddress != BindTailscale {
		t.Errorf("rebuilt BindAddress = %q, want the configured %q", rebuilt.BindAddress, BindTailscale)
	}
	if rebuilt.AllowExternal {
		t.Error("a tailnet bind must not be widened to allow_external by a restart")
	}

	second, err := NewProxyServer(rebuilt)
	if err != nil {
		t.Fatalf("rebuild of a tailnet-bound proxy failed: %v", err)
	}
	if second.BindAddress != first.BindAddress {
		t.Errorf("rebuilt bound to %q, want %q", second.BindAddress, first.BindAddress)
	}
	if got := second.ListenerOrigin(); !strings.HasPrefix(got, "https://") {
		t.Errorf("rebuilt ListenerOrigin = %q, want the tailnet https origin kept", got)
	}
}

// TestCopyBindConfigTo_KeepsAnExplicitExternalBind covers the other postures:
// a literal bind with allow_external comes back the same, and a default
// loopback proxy stays loopback.
func TestCopyBindConfigTo_KeepsAnExplicitExternalBind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bind  string
		allow bool
		want  string
	}{
		{"loopback default", "", false, "127.0.0.1"},
		{"all interfaces", "0.0.0.0", true, "0.0.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := NewProxyServer(ProxyConfig{ID: "p", TargetURL: "http://127.0.0.1:1", MaxLogSize: 10, BindAddress: tc.bind, AllowExternal: tc.allow})
			if err != nil {
				t.Fatal(err)
			}
			rebuilt := ProxyConfig{ID: "p", TargetURL: "http://127.0.0.1:1", MaxLogSize: 10}
			first.CopyBindConfigTo(&rebuilt)
			second, err := NewProxyServer(rebuilt)
			if err != nil {
				t.Fatalf("rebuild: %v", err)
			}
			if second.BindAddress != tc.want {
				t.Errorf("BindAddress = %q, want %q", second.BindAddress, tc.want)
			}
		})
	}
}
