package proxy

import (
	"context"
	"strings"
	"testing"
)

// stubTailnetIP returns a fixed answer in place of the tailscale lookup.
func stubTailnetIP(addr string) func(context.Context) string {
	return func(context.Context) string { return addr }
}

func TestNewProxyServer_BindTailscaleResolvesToTheNodeAddress(t *testing.T) {
	ps, err := NewProxyServer(ProxyConfig{
		ID:          "p",
		TargetURL:   "http://localhost:3000",
		ListenPort:  12345,
		BindAddress: BindTailscale,
		TailnetIP:   stubTailnetIP("100.101.102.103"),
		// No AllowExternal: a tailnet address is its own posture, and the
		// gate that gatekeeps 0.0.0.0 must not also block this.
	})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	if ps.BindAddress != "100.101.102.103" {
		t.Errorf("BindAddress = %q, want the resolved tailnet address", ps.BindAddress)
	}
	if want := "100.101.102.103:12345"; ps.ListenAddr != want {
		t.Errorf("ListenAddr = %q, want %q", ps.ListenAddr, want)
	}
}

func TestNewProxyServer_BindTailscaleFailsLoudWithNoTailnetAddress(t *testing.T) {
	_, err := NewProxyServer(ProxyConfig{
		ID:          "p",
		TargetURL:   "http://localhost:3000",
		BindAddress: BindTailscale,
		TailnetIP:   stubTailnetIP(""),
	})
	if err == nil {
		t.Fatal("expected an error when the node has no tailnet address")
	}
	if !strings.Contains(err.Error(), "tailscale") {
		t.Errorf("error must name tailscale so the developer knows what to fix, got: %v", err)
	}
}

// The exemption from the allow-external gate is granted to tailnet addresses,
// not to the token. A resolver answering with a LAN or public address must be
// refused, so the exemption cannot be widened by whatever produced the answer.
func TestNewProxyServer_BindTailscaleRefusesAnAddressOutsideTheTailnet(t *testing.T) {
	for _, addr := range []string{"192.168.1.10", "0.0.0.0", "8.8.8.8"} {
		t.Run(addr, func(t *testing.T) {
			_, err := NewProxyServer(ProxyConfig{
				ID:          "p",
				TargetURL:   "http://localhost:3000",
				BindAddress: BindTailscale,
				TailnetIP:   stubTailnetIP(addr),
			})
			if err == nil {
				t.Fatalf("bind %q was accepted as a tailnet address", addr)
			}
		})
	}
}

// A literal external address still needs the explicit opt-in it always did.
func TestNewProxyServer_LiteralExternalBindStillNeedsAllowExternal(t *testing.T) {
	if _, err := NewProxyServer(ProxyConfig{
		ID:          "p",
		TargetURL:   "http://localhost:3000",
		BindAddress: "0.0.0.0",
	}); err == nil {
		t.Fatal("0.0.0.0 must still require allow_external")
	}
	if _, err := NewProxyServer(ProxyConfig{
		ID:            "p",
		TargetURL:     "http://localhost:3000",
		BindAddress:   "100.101.102.103",
		AllowExternal: false,
	}); err == nil {
		t.Fatal("a literal tailnet address is not the tailscale token and must still require allow_external")
	}
}
