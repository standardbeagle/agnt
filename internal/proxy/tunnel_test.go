package proxy

import (
	"context"
	"testing"
	"time"

	"devtool-mcp/internal/protocol"
)

func TestNewTunnelManager(t *testing.T) {
	config := &protocol.TunnelConfig{
		Provider: "ngrok",
	}

	tm := NewTunnelManager(config, 8080)

	if tm == nil {
		t.Fatal("expected non-nil TunnelManager")
	}
	if tm.proxyPort != 8080 {
		t.Errorf("expected proxyPort 8080, got %d", tm.proxyPort)
	}
	if tm.config.Provider != "ngrok" {
		t.Errorf("expected provider ngrok, got %s", tm.config.Provider)
	}
	if tm.IsRunning() {
		t.Error("expected IsRunning to be false initially")
	}
	if tm.PublicURL() != "" {
		t.Errorf("expected empty PublicURL initially, got %s", tm.PublicURL())
	}
}

func TestTunnelManager_ExtractURL(t *testing.T) {
	tm := NewTunnelManager(&protocol.TunnelConfig{Provider: "ngrok"}, 8080)

	tests := []struct {
		name     string
		line     string
		expected string
	}{
		// ngrok patterns
		{
			name:     "ngrok forwarding",
			line:     "Forwarding https://abc123.ngrok.io -> http://localhost:8080",
			expected: "https://abc123.ngrok.io",
		},
		{
			name:     "ngrok-free app domain",
			line:     "https://abc123.ngrok-free.app",
			expected: "https://abc123.ngrok-free.app",
		},
		{
			name:     "ngrok url= format",
			line:     "url=https://def456.ngrok.io",
			expected: "https://def456.ngrok.io",
		},

		// cloudflared patterns
		{
			name:     "cloudflared trycloudflare",
			line:     "https://random-name-here.trycloudflare.com",
			expected: "https://random-name-here.trycloudflare.com",
		},
		{
			name:     "cloudflared with pipe separator",
			line:     "INF | https://random-name.trycloudflare.com |",
			expected: "https://random-name.trycloudflare.com",
		},

		// tailscale patterns
		{
			name:     "tailscale funnel",
			line:     "https://mybox.tail12345.ts.net",
			expected: "https://mybox.tail12345.ts.net",
		},
		{
			name:     "tailscale with port",
			line:     "Serving https://mybox.tailnet.ts.net:443",
			expected: "https://mybox.tailnet.ts.net:443",
		},

		// localtunnel patterns
		{
			name:     "localtunnel",
			line:     "your url is: https://warm-phones-lie.loca.lt",
			expected: "https://warm-phones-lie.loca.lt",
		},

		// Generic HTTPS
		{
			name:     "generic https",
			line:     "Tunnel ready at https://example.tunnel.dev",
			expected: "https://example.tunnel.dev",
		},

		// No match
		{
			name:     "no url",
			line:     "Starting tunnel...",
			expected: "",
		},
		{
			name:     "http not https",
			line:     "local server at http://localhost:8080",
			expected: "", // We mainly care about public HTTPS URLs
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tm.extractURL(tt.line)
			if result != tt.expected {
				t.Errorf("extractURL(%q) = %q, expected %q", tt.line, result, tt.expected)
			}
		})
	}
}

func TestTunnelManager_BuildCommand_Ngrok(t *testing.T) {
	tests := []struct {
		name         string
		config       *protocol.TunnelConfig
		port         int
		expectedCmd  string
		expectedArgs []string
		expectError  bool
	}{
		{
			name: "basic ngrok",
			config: &protocol.TunnelConfig{
				Provider: "ngrok",
			},
			port:         8080,
			expectedCmd:  "ngrok",
			expectedArgs: []string{"http", "8080"},
		},
		{
			name: "ngrok with auth token",
			config: &protocol.TunnelConfig{
				Provider:  "ngrok",
				AuthToken: "secret123",
			},
			port:         3000,
			expectedCmd:  "ngrok",
			expectedArgs: []string{"http", "3000", "--authtoken", "secret123"},
		},
		{
			name: "ngrok with region",
			config: &protocol.TunnelConfig{
				Provider: "ngrok",
				Region:   "eu",
			},
			port:         5000,
			expectedCmd:  "ngrok",
			expectedArgs: []string{"http", "5000", "--region", "eu"},
		},
		{
			name: "ngrok with extra args",
			config: &protocol.TunnelConfig{
				Provider: "ngrok",
				Args:     []string{"--hostname", "myapp.ngrok.io"},
			},
			port:         8080,
			expectedCmd:  "ngrok",
			expectedArgs: []string{"http", "8080", "--hostname", "myapp.ngrok.io"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm := NewTunnelManager(tt.config, tt.port)
			cmd, args, err := tm.buildCommand()

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cmd != tt.expectedCmd {
				t.Errorf("expected cmd %q, got %q", tt.expectedCmd, cmd)
			}

			if len(args) != len(tt.expectedArgs) {
				t.Fatalf("expected %d args, got %d: %v", len(tt.expectedArgs), len(args), args)
			}

			for i, expected := range tt.expectedArgs {
				if args[i] != expected {
					t.Errorf("args[%d] = %q, expected %q", i, args[i], expected)
				}
			}
		})
	}
}

func TestTunnelManager_BuildCommand_Cloudflared(t *testing.T) {
	config := &protocol.TunnelConfig{
		Provider: "cloudflared",
	}

	tm := NewTunnelManager(config, 9000)
	cmd, args, err := tm.buildCommand()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "cloudflared" {
		t.Errorf("expected cmd 'cloudflared', got %q", cmd)
	}

	expectedArgs := []string{"tunnel", "--url", "http://localhost:9000"}
	if len(args) != len(expectedArgs) {
		t.Fatalf("expected %d args, got %d: %v", len(expectedArgs), len(args), args)
	}

	for i, expected := range expectedArgs {
		if args[i] != expected {
			t.Errorf("args[%d] = %q, expected %q", i, args[i], expected)
		}
	}
}

func TestTunnelManager_BuildCommand_Tailscale(t *testing.T) {
	config := &protocol.TunnelConfig{
		Provider: "tailscale",
	}

	tm := NewTunnelManager(config, 4000)
	cmd, args, err := tm.buildCommand()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd != "tailscale" {
		t.Errorf("expected cmd 'tailscale', got %q", cmd)
	}

	expectedArgs := []string{"funnel", "4000"}
	if len(args) != len(expectedArgs) {
		t.Fatalf("expected %d args, got %d: %v", len(expectedArgs), len(args), args)
	}

	for i, expected := range expectedArgs {
		if args[i] != expected {
			t.Errorf("args[%d] = %q, expected %q", i, args[i], expected)
		}
	}
}

func TestTunnelManager_BuildCommand_Custom(t *testing.T) {
	tests := []struct {
		name         string
		config       *protocol.TunnelConfig
		port         int
		expectedCmd  string
		expectedArgs []string
		expectError  bool
	}{
		{
			name: "custom with port placeholder",
			config: &protocol.TunnelConfig{
				Provider: "custom",
				Command:  "my-tunnel",
				Args:     []string{"--port", "{{PORT}}", "--mode", "http"},
			},
			port:         7777,
			expectedCmd:  "my-tunnel",
			expectedArgs: []string{"--port", "7777", "--mode", "http"},
		},
		{
			name: "custom without command",
			config: &protocol.TunnelConfig{
				Provider: "custom",
			},
			port:        8080,
			expectError: true,
		},
		{
			name: "custom with port in command",
			config: &protocol.TunnelConfig{
				Provider: "custom",
				Command:  "expose-{{PORT}}",
				Args:     []string{},
			},
			port:         5555,
			expectedCmd:  "expose-5555",
			expectedArgs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm := NewTunnelManager(tt.config, tt.port)
			cmd, args, err := tm.buildCommand()

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cmd != tt.expectedCmd {
				t.Errorf("expected cmd %q, got %q", tt.expectedCmd, cmd)
			}

			if len(args) != len(tt.expectedArgs) {
				t.Fatalf("expected %d args, got %d: %v", len(tt.expectedArgs), len(args), args)
			}

			for i, expected := range tt.expectedArgs {
				if args[i] != expected {
					t.Errorf("args[%d] = %q, expected %q", i, args[i], expected)
				}
			}
		})
	}
}

func TestTunnelManager_BuildCommand_UnknownProvider(t *testing.T) {
	config := &protocol.TunnelConfig{
		Provider: "unknown-provider",
	}

	tm := NewTunnelManager(config, 8080)
	_, _, err := tm.buildCommand()

	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestTunnelManager_RecentOutput(t *testing.T) {
	tm := NewTunnelManager(&protocol.TunnelConfig{Provider: "ngrok"}, 8080)

	// Initially empty
	output := tm.RecentOutput()
	if len(output) != 0 {
		t.Errorf("expected empty output initially, got %d lines", len(output))
	}

	// Add some output manually (simulating monitorOutput behavior)
	tm.outputMu.Lock()
	tm.output = append(tm.output, "line 1", "line 2", "line 3")
	tm.outputMu.Unlock()

	output = tm.RecentOutput()
	if len(output) != 3 {
		t.Errorf("expected 3 lines, got %d", len(output))
	}

	// Verify it's a copy, not the original slice
	output[0] = "modified"
	original := tm.RecentOutput()
	if original[0] == "modified" {
		t.Error("RecentOutput should return a copy, not the original slice")
	}
}

func TestTunnelManager_WaitForURL_AlreadyAvailable(t *testing.T) {
	tm := NewTunnelManager(&protocol.TunnelConfig{Provider: "ngrok"}, 8080)
	tm.publicURL.Store("https://test.ngrok.io")
	tm.running.Store(true)

	url, err := tm.WaitForURL(100 * time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://test.ngrok.io" {
		t.Errorf("expected 'https://test.ngrok.io', got %q", url)
	}
}

func TestTunnelManager_WaitForURL_Timeout(t *testing.T) {
	tm := NewTunnelManager(&protocol.TunnelConfig{Provider: "ngrok"}, 8080)
	tm.running.Store(true)

	start := time.Now()
	_, err := tm.WaitForURL(100 * time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error")
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("expected at least 100ms elapsed, got %v", elapsed)
	}
}

func TestTunnelManager_WaitForURL_ProcessExited(t *testing.T) {
	tm := NewTunnelManager(&protocol.TunnelConfig{Provider: "ngrok"}, 8080)
	tm.running.Store(false) // Process not running

	_, err := tm.WaitForURL(1 * time.Second)
	if err == nil {
		t.Error("expected error when process not running")
	}
}

func TestTunnelManager_StartAlreadyRunning(t *testing.T) {
	tm := NewTunnelManager(&protocol.TunnelConfig{Provider: "ngrok"}, 8080)
	tm.running.Store(true)

	err := tm.Start(context.Background())
	if err == nil {
		t.Error("expected error when starting already running tunnel")
	}
}

func TestTunnelManager_StopNotRunning(t *testing.T) {
	tm := NewTunnelManager(&protocol.TunnelConfig{Provider: "ngrok"}, 8080)

	// Should not error when stopping a non-running tunnel
	err := tm.Stop()
	if err != nil {
		t.Errorf("unexpected error stopping non-running tunnel: %v", err)
	}
}

func TestTunnelManager_ProviderCaseInsensitive(t *testing.T) {
	providers := []string{"NGROK", "Ngrok", "CloudFlared", "CLOUDFLARE", "Tailscale", "TAILSCALE"}

	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			config := &protocol.TunnelConfig{
				Provider: provider,
			}
			tm := NewTunnelManager(config, 8080)
			_, _, err := tm.buildCommand()
			if err != nil {
				t.Errorf("provider %q should be recognized (case insensitive): %v", provider, err)
			}
		})
	}
}

// TestTunnelURLPatterns tests that the URL patterns compile and work correctly
func TestTunnelURLPatterns(t *testing.T) {
	// Verify all patterns compiled (would panic at init if not)
	if len(tunnelURLPatterns) == 0 {
		t.Error("expected at least one URL pattern")
	}

	// Test specific pattern matches
	testCases := []struct {
		input   string
		matches bool
	}{
		{"https://abc.ngrok.io", true},
		{"https://abc.ngrok-free.app", true},
		{"https://random.trycloudflare.com", true},
		{"https://box.tail123.ts.net", true},
		{"http://localhost:8080", false}, // We match http but prefer https in patterns
		{"not a url at all", false},
	}

	for _, tc := range testCases {
		matched := false
		for _, pattern := range tunnelURLPatterns {
			if pattern.MatchString(tc.input) {
				matched = true
				break
			}
		}
		if matched != tc.matches {
			t.Errorf("pattern match for %q: got %v, expected %v", tc.input, matched, tc.matches)
		}
	}
}
