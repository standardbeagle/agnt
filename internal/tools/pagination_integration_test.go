package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasFilters(t *testing.T) {
	tests := []struct {
		name     string
		input    ProxyLogInput
		expected bool
	}{
		{
			name:     "empty input",
			input:    ProxyLogInput{},
			expected: false,
		},
		{
			name:     "only proxy_id",
			input:    ProxyLogInput{ProxyID: "dev"},
			expected: false,
		},
		{
			name:     "only limit",
			input:    ProxyLogInput{Limit: 50},
			expected: false,
		},
		{
			name:     "only raw",
			input:    ProxyLogInput{Raw: true},
			expected: false,
		},
		{
			name:     "types filter",
			input:    ProxyLogInput{Types: []string{"error"}},
			expected: true,
		},
		{
			name:     "url_pattern filter",
			input:    ProxyLogInput{URLPattern: "/api"},
			expected: true,
		},
		{
			name:     "methods filter",
			input:    ProxyLogInput{Methods: []string{"POST"}},
			expected: true,
		},
		{
			name:     "status_codes filter",
			input:    ProxyLogInput{StatusCodes: []int{500}},
			expected: true,
		},
		{
			name:     "since filter",
			input:    ProxyLogInput{Since: "5m"},
			expected: true,
		},
		{
			name:     "until filter",
			input:    ProxyLogInput{Until: "2026-01-01T00:00:00Z"},
			expected: true,
		},
		{
			name:     "errors_only filter",
			input:    ProxyLogInput{ErrorsOnly: true},
			expected: true,
		},
		{
			name:     "diagnostic_levels filter",
			input:    ProxyLogInput{DiagnosticLevels: []string{"error"}},
			expected: true,
		},
		{
			name:     "multiple filters",
			input:    ProxyLogInput{Types: []string{"http"}, Methods: []string{"GET"}, ErrorsOnly: true},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.input.hasFilters())
		})
	}
}
