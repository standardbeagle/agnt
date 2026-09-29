package tools

import "testing"

// TestProxyAccessURL guards the "http://localhost127.0.0.1:47341" typo fix
// (feedback #6), the 0.0.0.0 port-only rendering, and the scheme: the URL
// comes from the daemon, so a tailnet proxy serving https is shown as https.
func TestProxyAccessURL(t *testing.T) {
	tests := []struct {
		name      string
		proxyURL  string
		bind      string
		publicURL string
		want      string
	}{
		{"loopback", "http://127.0.0.1:47341", "127.0.0.1", "", "http://127.0.0.1:47341"},
		{"all-interfaces shows port only", "http://0.0.0.0:8080", "0.0.0.0", "", "http://<your-ip>:8080"},
		{"public wins over local", "http://127.0.0.1:47341", "127.0.0.1", "http://pub:9000", "http://pub:9000"},
		{"public (tunnel-set) wins over all-interfaces", "http://0.0.0.0:8080", "0.0.0.0", "https://x.trycloudflare.com", "https://x.trycloudflare.com"},
		{"ipv6", "http://[::1]:47341", "::1", "", "http://[::1]:47341"},
		{"tailnet https", "https://build1.tnet.ts.net:31536", "100.87.26.14", "", "https://build1.tnet.ts.net:31536"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proxyAccessURL(tt.proxyURL, tt.bind, tt.publicURL)
			if got != tt.want {
				t.Errorf("proxyAccessURL(%q,%q,%q) = %q, want %q",
					tt.proxyURL, tt.bind, tt.publicURL, got, tt.want)
			}
		})
	}
}
