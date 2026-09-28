package platform

import "testing"

func TestParseTailscaleWhois(t *testing.T) {
	cases := map[string]struct {
		json, want string
	}{
		"person":           {`{"Node":{"Name":"ws.ts.net."},"UserProfile":{"LoginName":"andy@example.com"}}`, "andy@example.com"},
		"tagged node":      {`{"Node":{"Tags":["tag:server"]},"UserProfile":{"LoginName":"andy@example.com"}}`, ""},
		"tagged-devices":   {`{"Node":{},"UserProfile":{"LoginName":"tagged-devices"}}`, ""},
		"no login":         {`{"Node":{},"UserProfile":{}}`, ""},
		"not an identity":  {`{"Node":{},"UserProfile":{"LoginName":"robot"}}`, ""},
		"garbage":          {`not json`, ""},
		"whitespace login": {`{"Node":{},"UserProfile":{"LoginName":"  a@b.c "}}`, "a@b.c"},
	}
	for name, tc := range cases {
		if got := parseTailscaleWhois([]byte(tc.json)); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
	if TailscaleWhois(t.Context(), "not-an-ip") != "" {
		t.Error("non-IP input must return no identity without running tailscale")
	}
}

func TestIsTailnetPeerAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.87.26.14":              true,
		"100.64.0.1":                true,
		"100.128.0.1":               false,
		"192.168.1.5":               false,
		"127.0.0.1":                 false,
		"fd7a:115c:a1e0::5501:1a0e": true,
		"fd7a:115c:a1e1::1":         false,
		"::ffff:100.87.26.14":       true,
		"nope":                      false,
	} {
		if got := IsTailnetPeerAddress(addr); got != want {
			t.Errorf("IsTailnetPeerAddress(%q) = %v, want %v", addr, got, want)
		}
	}
}
