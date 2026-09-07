package platform

import "testing"

func TestParseTailscaleDNSName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "trailing dot trimmed",
			input:    `{"Self":{"DNSName":"machine.tailnet.ts.net."}}`,
			expected: "machine.tailnet.ts.net",
		},
		{
			name:     "no trailing dot",
			input:    `{"Self":{"DNSName":"machine.tailnet.ts.net"}}`,
			expected: "machine.tailnet.ts.net",
		},
		{
			name:     "empty DNSName",
			input:    `{"Self":{"DNSName":""}}`,
			expected: "",
		},
		{
			name:     "missing Self",
			input:    `{}`,
			expected: "",
		},
		{
			name:     "extra fields ignored",
			input:    `{"Self":{"DNSName":"host.tnet.ts.net.","ID":"abc","Online":true},"BackendState":"Running"}`,
			expected: "host.tnet.ts.net",
		},
		{
			name:     "malformed JSON",
			input:    `not json`,
			expected: "",
		},
		{
			name:     "empty input",
			input:    ``,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTailscaleDNSName([]byte(tt.input))
			if got != tt.expected {
				t.Errorf("parseTailscaleDNSName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseTailscaleSelfIdentities(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "dns name and ips",
			input:    `{"Self":{"DNSName":"machine.tailnet.ts.net.","TailscaleIPs":["100.101.102.103","fd7a:115c:a1e0::1"]}}`,
			expected: []string{"machine.tailnet.ts.net", "100.101.102.103", "fd7a:115c:a1e0::1"},
		},
		{
			name:     "ips only",
			input:    `{"Self":{"DNSName":"","TailscaleIPs":["100.101.102.103"]}}`,
			expected: []string{"100.101.102.103"},
		},
		{
			name:     "missing Self",
			input:    `{}`,
			expected: nil,
		},
		{
			name:     "malformed JSON",
			input:    `not json`,
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTailscaleSelfIdentities([]byte(tt.input))
			if len(got) != len(tt.expected) {
				t.Fatalf("parseTailscaleSelfIdentities() = %v, want %v", got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Fatalf("parseTailscaleSelfIdentities() = %v, want %v", got, tt.expected)
				}
			}
		})
	}
}

func TestParseTailscaleIP(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "tailnet IPv4",
			output: `{"Self":{"DNSName":"host.tail1234.ts.net.","TailscaleIPs":["100.101.102.103","fd7a:115c:a1e0::1"]}}`,
			want:   "100.101.102.103",
		},
		{
			name:   "IPv6 first still yields the IPv4",
			output: `{"Self":{"TailscaleIPs":["fd7a:115c:a1e0::1","100.64.0.1"]}}`,
			want:   "100.64.0.1",
		},
		{
			// A bind resolved from this helper must never be a LAN or public
			// address, so an address outside 100.64.0.0/10 is refused even
			// when tailscale reports it as our own.
			name:   "address outside the tailnet range is refused",
			output: `{"Self":{"TailscaleIPs":["192.168.1.10","8.8.8.8","100.63.255.255","100.128.0.0"]}}`,
			want:   "",
		},
		{name: "no addresses", output: `{"Self":{"DNSName":"host.ts.net."}}`, want: ""},
		{name: "unparseable", output: `not json`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTailscaleIP([]byte(tt.output)); got != tt.want {
				t.Errorf("parseTailscaleIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
