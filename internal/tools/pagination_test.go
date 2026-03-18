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
