package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaginationAlwaysSerializesZero(t *testing.T) {
	p := Pagination{Count: 0, TotalAvailable: 0, Limit: 100}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"count":0`)
	assert.Contains(t, string(b), `"total_available":0`)
	assert.Contains(t, string(b), `"limit":100`)
}

func TestPaginationFilteredOmittedWhenFalse(t *testing.T) {
	p := Pagination{Count: 5, TotalAvailable: 10, Limit: 100, Filtered: false}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"filtered"`)
}

func TestPaginationFilteredShownWhenTrue(t *testing.T) {
	p := Pagination{Count: 0, TotalAvailable: 10, Limit: 100, Filtered: true}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"filtered":true`)
}

func TestNewPagination(t *testing.T) {
	p := NewPagination(5, 42, 100, true)
	assert.Equal(t, 5, p.Count)
	assert.Equal(t, 42, p.TotalAvailable)
	assert.Equal(t, 100, p.Limit)
	assert.True(t, p.Filtered)
}

func TestProxyLogOutputZeroCountSerializes(t *testing.T) {
	pag := NewPagination(0, 0, 100, false)
	output := ProxyLogOutput{
		Pagination: &pag,
	}
	b, err := json.Marshal(output)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"count":0`)
	assert.Contains(t, s, `"total_available":0`)
	assert.Contains(t, s, `"limit":100`)
	assert.NotEqual(t, "{}", s, "zero-result output must not be empty JSON")
}

func TestProxyLogOutputFilteredShowsContext(t *testing.T) {
	pag := NewPagination(0, 42, 100, true)
	output := ProxyLogOutput{
		Pagination: &pag,
	}
	b, err := json.Marshal(output)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"count":0`)
	assert.Contains(t, s, `"total_available":42`)
	assert.Contains(t, s, `"filtered":true`)
}

func TestOutputStructsSerializeZeroCount(t *testing.T) {
	tests := []struct {
		name   string
		output interface{}
	}{
		{"ProcOutput", ProcOutput{}},
		{"ProxyOutput", ProxyOutput{}},
		{"CurrentPageOutput", CurrentPageOutput{}},
		{"StoreOutput", StoreOutput{}},
		{"AutomationOutput", AutomationOutput{}},
		{"BrowserOutput", BrowserOutput{}},
		{"SessionOutput", SessionOutput{}},
		{"TunnelOutput", TunnelOutput{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.output)
			require.NoError(t, err)
			s := string(b)
			assert.Contains(t, s, `"count":0`, "count:0 must always appear even when zero")
		})
	}
}
