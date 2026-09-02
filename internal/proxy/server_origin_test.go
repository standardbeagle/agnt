package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestCheckWSOrigin(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		origin    string
		publicURL string
		tailnet   []string
		want      bool
	}{
		{name: "missing origin", host: "127.0.0.1:19191", want: false},
		{name: "DNS rebinding host", host: "evil.example:19191", origin: "http://evil.example:19191", want: false},
		{name: "loopback same origin", host: "127.0.0.1:19191", origin: "http://127.0.0.1:19191", want: true},
		{name: "localhost same origin", host: "localhost:19191", origin: "http://localhost:19191", want: true},
		{name: "public proxy origin", host: "127.0.0.1:19191", origin: "https://example.trycloudflare.com", publicURL: "https://example.trycloudflare.com", want: true},
		{name: "tailnet magicdns same origin", host: "node.tailnet.ts.net:19191", origin: "http://node.tailnet.ts.net:19191", tailnet: []string{"node.tailnet.ts.net.", "100.101.102.103"}, want: true},
		{name: "tailnet ip same origin", host: "100.101.102.103:19191", origin: "http://100.101.102.103:19191", tailnet: []string{"node.tailnet.ts.net", "100.101.102.103"}, want: true},
		{name: "tailnet ipv6 same origin", host: "[fd7a:115c:a1e0::1]:19191", origin: "http://[fd7a:115c:a1e0::1]:19191", tailnet: []string{"fd7a:115c:a1e0::1"}, want: true},
		{name: "tailnet host case insensitive", host: "Node.Tailnet.ts.net:19191", origin: "http://Node.Tailnet.ts.net:19191", tailnet: []string{"node.tailnet.ts.net"}, want: true},
		{name: "rebinding hostname on tailnet node", host: "evil.example:19191", origin: "http://evil.example:19191", tailnet: []string{"node.tailnet.ts.net", "100.101.102.103"}, want: false},
		{name: "cross origin against tailnet host", host: "node.tailnet.ts.net:19191", origin: "http://evil.example", tailnet: []string{"node.tailnet.ts.net"}, want: false},
		{name: "tailscale unavailable fails closed", host: "node.tailnet.ts.net:19191", origin: "http://node.tailnet.ts.net:19191", tailnet: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := &ProxyServer{tailnetIdentities: func(context.Context) []string { return tt.tailnet }}
			if tt.publicURL != "" {
				ps.SetPublicURL(tt.publicURL)
			}
			req := &http.Request{Host: tt.host, Header: make(http.Header)}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			if got := ps.checkWSOrigin(req); got != tt.want {
				t.Fatalf("checkWSOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckWSOrigin_TailnetLookupCached pins that the tailscale lookup runs
// once per TTL, not once per upgrade, and never runs for loopback traffic.
func TestCheckWSOrigin_TailnetLookupCached(t *testing.T) {
	calls := 0
	ps := &ProxyServer{tailnetIdentities: func(context.Context) []string {
		calls++
		return []string{"node.tailnet.ts.net"}
	}}
	mk := func(host string) *http.Request {
		req := &http.Request{Host: host, Header: make(http.Header)}
		req.Header.Set("Origin", "http://"+host)
		return req
	}

	if !ps.checkWSOrigin(mk("127.0.0.1:19191")) {
		t.Fatal("loopback same origin must be accepted")
	}
	if calls != 0 {
		t.Fatalf("loopback traffic triggered %d tailscale lookups, want 0", calls)
	}
	for i := 0; i < 3; i++ {
		if !ps.checkWSOrigin(mk("node.tailnet.ts.net:19191")) {
			t.Fatalf("tailnet same origin rejected on call %d", i)
		}
	}
	if calls != 1 {
		t.Fatalf("tailscale lookup ran %d times within TTL, want 1", calls)
	}

	ps.tailnetCache.Store(&tailnetIdentitySet{hosts: map[string]struct{}{}, expires: time.Now().Add(-time.Second)})
	if !ps.checkWSOrigin(mk("node.tailnet.ts.net:19191")) {
		t.Fatal("tailnet same origin rejected after cache expiry")
	}
	if calls != 2 {
		t.Fatalf("tailscale lookup ran %d times after expiry, want 2", calls)
	}
}
