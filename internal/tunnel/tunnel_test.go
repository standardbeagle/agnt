package tunnel

import (
	"testing"
)

func TestCloudflareURLPattern(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "INF | https://threaded-fathers-explore-supplier.trycloudflare.com |",
			expected: "https://threaded-fathers-explore-supplier.trycloudflare.com",
		},
		{
			input:    "https://abc-def-ghi.trycloudflare.com",
			expected: "https://abc-def-ghi.trycloudflare.com",
		},
		{
			input:    "2024/01/15 10:00:00 https://test123.trycloudflare.com connected",
			expected: "https://test123.trycloudflare.com",
		},
		{
			input:    "no url here",
			expected: "",
		},
		{
			input:    "https://example.com is not cloudflare",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			match := cloudflareURLPattern.FindString(tt.input)
			if match != tt.expected {
				t.Errorf("got %q, want %q", match, tt.expected)
			}
		})
	}
}

func TestNgrokURLPattern(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "Forwarding https://abc123def.ngrok.io -> http://localhost:8080",
			expected: "https://abc123def.ngrok.io",
		},
		{
			input:    "https://abcd-1234-wxyz.ngrok-free.app",
			expected: "https://abcd-1234-wxyz.ngrok-free.app",
		},
		{
			input:    "no url here",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			match := ngrokURLPattern.FindString(tt.input)
			if match != tt.expected {
				t.Errorf("got %q, want %q", match, tt.expected)
			}
		})
	}
}

func TestTunnelState(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateIdle, "idle"},
		{StateStarting, "starting"},
		{StateConnected, "connected"},
		{StateFailed, "failed"},
		{StateStopped, "stopped"},
		{State(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestTailscaleURLPattern(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "Available within your tailnet: https://machine.tailnet.ts.net/",
			expected: "https://machine.tailnet.ts.net/",
		},
		{
			input:    "https://my-host.example-tnet.ts.net",
			expected: "https://my-host.example-tnet.ts.net",
		},
		{
			input:    "  https://host.tail-net.ts.net/path?x=1 (proxy http://127.0.0.1:8080)",
			expected: "https://host.tail-net.ts.net/path?x=1",
		},
		{
			input:    "no url here",
			expected: "",
		},
		{
			input:    "https://example.com is not tailscale",
			expected: "",
		},
		{
			// Funnel URLs share the ts.net suffix; pattern still matches.
			// This is intentional — we don't try to distinguish serve vs
			// funnel from the URL alone.
			input:    "https://funneled.tnet.ts.net",
			expected: "https://funneled.tnet.ts.net",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			match := tailscaleURLPattern.FindString(tt.input)
			if match != tt.expected {
				t.Errorf("got %q, want %q", match, tt.expected)
			}
		})
	}
}

func TestNewTunnel(t *testing.T) {
	config := Config{
		Provider:  ProviderCloudflare,
		LocalPort: 8080,
	}

	tunnel := New(config)

	if tunnel.State() != StateIdle {
		t.Errorf("expected state Idle, got %s", tunnel.State())
	}

	if tunnel.PublicURL() != "" {
		t.Errorf("expected empty public URL, got %q", tunnel.PublicURL())
	}

	info := tunnel.Info()
	if info.LocalAddr != "localhost:8080" {
		t.Errorf("expected localhost:8080, got %s", info.LocalAddr)
	}
}

func TestTunnelInfo(t *testing.T) {
	config := Config{
		Provider:  ProviderCloudflare,
		LocalPort: 3000,
		LocalHost: "127.0.0.1",
	}

	tunnel := New(config)
	info := tunnel.Info()

	if info.Provider != ProviderCloudflare {
		t.Errorf("expected provider cloudflare, got %s", info.Provider)
	}
	if info.State != "idle" {
		t.Errorf("expected state idle, got %s", info.State)
	}
	if info.LocalAddr != "127.0.0.1:3000" {
		t.Errorf("expected 127.0.0.1:3000, got %s", info.LocalAddr)
	}
}
