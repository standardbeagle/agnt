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
