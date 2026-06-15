package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatcherKeyAndQueue(t *testing.T) {
	recs := []Recording{
		{Match: MatchKey{Method: "GET", Path: "/api/items", QueryKeys: []string{"date"}}, Status: 200, BodyRef: "b0", Hits: 1},
		{Match: MatchKey{Method: "GET", Path: "/api/items", QueryKeys: []string{"date"}}, Status: 200, BodyRef: "b1", Hits: 1},
		{Match: MatchKey{Method: "POST", Path: "/api/items"}, RequestBodySig: "sig1", Status: 201, BodyRef: "b2", Hits: 1},
	}
	m := NewMatcher(recs)

	r1, ok := m.Match("GET", "/api/items/99?date=2026-06-15&_=1", "")
	require.True(t, ok)
	assert.Equal(t, "b0", r1.BodyRef)
	r2, ok := m.Match("GET", "/api/items?date=x", "")
	require.True(t, ok)
	assert.Equal(t, "b1", r2.BodyRef)

	_, ok = m.Match("GET", "/api/items?date=x", "")
	assert.False(t, ok)

	rp, ok := m.Match("POST", "/api/items", "sig1")
	require.True(t, ok)
	assert.Equal(t, 201, rp.Status)
}

func TestMatchKeyString(t *testing.T) {
	k := buildKey("get", "/api/items/5?b=2&a=1", "")
	assert.Equal(t, "GET /api/items/:id ?a,b", k)
}
