package proxy

import (
	"net/http"
	"testing"
)

func TestCheckWSOrigin(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		origin    string
		publicURL string
		want      bool
	}{
		{name: "missing origin", host: "127.0.0.1:19191", want: false},
		{name: "DNS rebinding host", host: "evil.example:19191", origin: "http://evil.example:19191", want: false},
		{name: "loopback same origin", host: "127.0.0.1:19191", origin: "http://127.0.0.1:19191", want: true},
		{name: "localhost same origin", host: "localhost:19191", origin: "http://localhost:19191", want: true},
		{name: "public proxy origin", host: "127.0.0.1:19191", origin: "https://example.trycloudflare.com", publicURL: "https://example.trycloudflare.com", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := &ProxyServer{PublicURL: tt.publicURL}
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
