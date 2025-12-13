package proxy

import (
	"strconv"
	"testing"
)

func TestDefaultPortForURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"localhost 3000", "http://localhost:3000"},
		{"localhost 8080", "http://localhost:8080"},
		{"localhost 5000", "http://localhost:5000"},
		{"127.0.0.1:3000", "http://127.0.0.1:3000"},
		{"example.com", "http://example.com"},
		{"example.com:443", "https://example.com:443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := DefaultPortForURL(tt.url)

			// Port should be in valid range
			if port < 10000 || port >= 60000 {
				t.Errorf("DefaultPortForURL(%q) = %d, want in range [10000, 60000)", tt.url, port)
			}

			// Port should be deterministic (same input = same output)
			port2 := DefaultPortForURL(tt.url)
			if port != port2 {
				t.Errorf("DefaultPortForURL(%q) not deterministic: got %d then %d", tt.url, port, port2)
			}
		})
	}
}

func TestDefaultPortForURL_Stability(t *testing.T) {
	// Same URL should always produce the same port
	url := "http://localhost:3000"
	expected := DefaultPortForURL(url)

	for i := 0; i < 100; i++ {
		got := DefaultPortForURL(url)
		if got != expected {
			t.Errorf("iteration %d: DefaultPortForURL(%q) = %d, want %d", i, url, got, expected)
		}
	}
}

func TestDefaultPortForURL_DifferentURLs(t *testing.T) {
	// Different URLs should (usually) produce different ports
	// Note: Collisions are possible but should be rare
	urls := []string{
		"http://localhost:3000",
		"http://localhost:3001",
		"http://localhost:8080",
		"http://127.0.0.1:3000",
		"http://example.com",
		"http://api.example.com",
	}

	ports := make(map[int]string)
	collisions := 0

	for _, url := range urls {
		port := DefaultPortForURL(url)
		if existing, ok := ports[port]; ok {
			t.Logf("Collision: %q and %q both map to port %d", existing, url, port)
			collisions++
		}
		ports[port] = url
	}

	// Allow at most 1 collision in this small set
	if collisions > 1 {
		t.Errorf("Too many collisions: %d (expected at most 1)", collisions)
	}
}

func TestDefaultPortForURL_PortSensitivity(t *testing.T) {
	// URLs that differ only in port should get different proxy ports
	url1 := "http://localhost:3000"
	url2 := "http://localhost:3001"

	port1 := DefaultPortForURL(url1)
	port2 := DefaultPortForURL(url2)

	// They could collide by chance, but it's worth checking they usually don't
	t.Logf("localhost:3000 -> port %d", port1)
	t.Logf("localhost:3001 -> port %d", port2)

	// This is informational - we log it but don't fail on collision
	// since with 50000 possible ports, collision probability is ~0.002%
}

func TestNewProxyServer_DefaultPort(t *testing.T) {
	targetURL := "http://localhost:3000"
	expectedPort := DefaultPortForURL(targetURL)
	// Default bind address is 127.0.0.1 for security
	expectedAddr := "127.0.0.1:" + strconv.Itoa(expectedPort)

	config := ProxyConfig{
		ID:         "test",
		TargetURL:  targetURL,
		ListenPort: -1, // Trigger default port calculation
	}

	ps, err := NewProxyServer(config)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}

	// Check ListenAddr matches expected hash-based port with default bind address
	if ps.ListenAddr != expectedAddr {
		t.Errorf("NewProxyServer() ListenAddr = %q, want %q", ps.ListenAddr, expectedAddr)
	}

	// Should definitely not be the old hardcoded default
	if ps.ListenAddr == ":8080" || ps.ListenAddr == "127.0.0.1:8080" {
		t.Errorf("NewProxyServer() ListenAddr = %s, should use hash-based default", ps.ListenAddr)
	}

	t.Logf("Target %s -> ListenAddr %s (expected port %d)", targetURL, ps.ListenAddr, expectedPort)
}
