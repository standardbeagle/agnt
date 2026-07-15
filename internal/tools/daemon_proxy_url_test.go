package tools

import "testing"

// TestProxyAccessURL guards the "http://localhost127.0.0.1:47341" typo fix
// (feedback #6) and the 0.0.0.0 port-only rendering.
func TestProxyAccessURL(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		bind       string
		publicURL  string
		tunnelURL  string
		want       string
	}{
		{"loopback host:port", "127.0.0.1:47341", "127.0.0.1", "", "", "http://127.0.0.1:47341"},
		{"all-interfaces shows port only", "0.0.0.0:8080", "0.0.0.0", "", "", "http://<your-ip>:8080"},
		{"tunnel wins", "127.0.0.1:47341", "127.0.0.1", "", "https://x.trycloudflare.com", "https://x.trycloudflare.com"},
		{"public wins over local", "127.0.0.1:47341", "127.0.0.1", "http://pub:9000", "", "http://pub:9000"},
		{"ipv6 host:port", "[::1]:47341", "::1", "", "", "http://[::1]:47341"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proxyAccessURL(tt.listenAddr, tt.bind, tt.publicURL, tt.tunnelURL)
			if got != tt.want {
				t.Errorf("proxyAccessURL(%q,%q,%q,%q) = %q, want %q",
					tt.listenAddr, tt.bind, tt.publicURL, tt.tunnelURL, got, tt.want)
			}
		})
	}
}
