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
