package tools

import (
	"encoding/json"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestProxyServer(t *testing.T) *proxy.ProxyServer {
	t.Helper()
	config := proxy.ProxyConfig{
		ID:         "test-pagination",
		TargetURL:  "http://localhost:59999",
		ListenPort: 0,
		MaxLogSize: 100,
	}
	ps, err := proxy.NewProxyServer(config)
	require.NoError(t, err)
	return ps
}

func TestHandleProxyLogQuery_EmptyReturnsPagination(t *testing.T) {
	ps := createTestProxyServer(t)

	input := ProxyLogInput{
		ProxyID: "test-pagination",
	}

	result, output, err := handleProxyLogQuery(ps, input)
	require.NoError(t, err)
	// result is nil when compact handler returns output via the struct
	if result != nil {
		assert.False(t, result.IsError)
	}

	require.NotNil(t, output.Pagination, "pagination must be present even with zero results")
	assert.Equal(t, 0, output.Pagination.Count)
	assert.Equal(t, 0, output.Pagination.TotalAvailable)
	assert.Equal(t, 100, output.Pagination.Limit, "default limit should be 100")

	// Verify JSON serialization includes count:0
	b, err := json.Marshal(output)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"count":0`)
	assert.Contains(t, s, `"total_available":0`)
}

func TestHandleProxyLogQuery_WithFiltersShowsFiltered(t *testing.T) {
	ps := createTestProxyServer(t)

	input := ProxyLogInput{
		ProxyID: "test-pagination",
		Types:   []string{"error"},
	}

	_, output, err := handleProxyLogQuery(ps, input)
	require.NoError(t, err)
	require.NotNil(t, output.Pagination)
	assert.True(t, output.Pagination.Filtered, "filtered should be true when type filter is set")

	b, err := json.Marshal(output)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"filtered":true`)
}

func TestHandleProxyLogQuery_WithoutFiltersOmitsFiltered(t *testing.T) {
	ps := createTestProxyServer(t)

	input := ProxyLogInput{
		ProxyID: "test-pagination",
	}

	_, output, err := handleProxyLogQuery(ps, input)
	require.NoError(t, err)
	require.NotNil(t, output.Pagination)
	assert.False(t, output.Pagination.Filtered)

	b, err := json.Marshal(output)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"filtered"`, "filtered:false should be omitted from JSON")
}

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
