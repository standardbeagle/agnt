package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadinessGate_DefaultOpen(t *testing.T) {
	g := newReadinessGate()
	assert.True(t, g.IsReady(), "gate with no dependencies should be open")
	assert.Empty(t, g.PendingDependencies())
}

func TestReadinessGate_SetDependenciesClosesGate(t *testing.T) {
	g := newReadinessGate()
	g.SetDependencies([]string{"dev-backend", "dev-lib"})

	assert.False(t, g.IsReady(), "gate should be closed while deps pending")
	assert.Equal(t, []string{"dev-backend", "dev-lib"}, g.PendingDependencies(),
		"pending list should be sorted")
}

func TestReadinessGate_SetDependenciesEmptyOpensGate(t *testing.T) {
	g := newReadinessGate()
	g.SetDependencies([]string{"dev-backend"})
	require.False(t, g.IsReady())

	g.SetDependencies(nil)
	assert.True(t, g.IsReady(), "setting nil deps should open the gate")
	assert.Empty(t, g.PendingDependencies())
}

func TestReadinessGate_MarkDependencyReadyOpensGate(t *testing.T) {
	g := newReadinessGate()
	g.SetDependencies([]string{"dev-backend", "dev-lib"})

	openedNow := g.MarkDependencyReady("dev-backend")
	assert.False(t, openedNow, "one dep marked but one still pending")
	assert.False(t, g.IsReady())
	assert.Equal(t, []string{"dev-lib"}, g.PendingDependencies())

	openedNow = g.MarkDependencyReady("dev-lib")
	assert.True(t, openedNow, "last dep marked should flip the gate")
	assert.True(t, g.IsReady())
	assert.Empty(t, g.PendingDependencies())
}

func TestReadinessGate_MarkDependencyReadyIdempotent(t *testing.T) {
	g := newReadinessGate()
	g.SetDependencies([]string{"dev-backend"})

	openedNow := g.MarkDependencyReady("dev-backend")
	assert.True(t, openedNow)

	// Second call is a no-op: the gate is already open, no new transition.
	openedNow = g.MarkDependencyReady("dev-backend")
	assert.False(t, openedNow, "re-marking same dep should not re-open")
	assert.True(t, g.IsReady())
}

func TestReadinessGate_MarkUnknownDependency(t *testing.T) {
	g := newReadinessGate()
	g.SetDependencies([]string{"dev-backend"})

	openedNow := g.MarkDependencyReady("not-a-dep")
	assert.False(t, openedNow)
	assert.False(t, g.IsReady(), "unknown marks must not open the gate")
	assert.Equal(t, []string{"dev-backend"}, g.PendingDependencies())
}

func TestReadinessGate_DuplicateNamesCoalesced(t *testing.T) {
	g := newReadinessGate()
	g.SetDependencies([]string{"dev-backend", "dev-backend", "dev-lib"})
	assert.Equal(t, []string{"dev-backend", "dev-lib"}, g.PendingDependencies())
}

func TestReadinessGate_EmptyStringDependenciesIgnored(t *testing.T) {
	g := newReadinessGate()
	g.SetDependencies([]string{"", "dev-backend", ""})
	assert.Equal(t, []string{"dev-backend"}, g.PendingDependencies())
}

// TestWriteReadinessNotReady verifies the 503 response body contains the
// stable sentinel, pending list, and Retry-After header. The sentinel is
// the contract point for the incident adapter's filtering.
func TestWriteReadinessNotReady(t *testing.T) {
	rec := httptest.NewRecorder()
	writeReadinessNotReady(rec, []string{"dev-backend", "dev-lib"})

	assert.Equal(t, 503, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.Equal(t, "waiting-for-dependencies", rec.Header().Get("X-Agnt-Readiness"))

	var body ReadinessBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, ReadinessSentinel, body.Error)
	assert.Equal(t, []string{"dev-backend", "dev-lib"}, body.Pending)
	assert.Equal(t, ReadinessRetryAfterMs, body.RetryAfterMs)
	assert.Contains(t, body.Message, "dev-backend")
	assert.Contains(t, body.Message, "dev-lib")
}

func TestFormatPendingJSON(t *testing.T) {
	assert.Equal(t, "[]", formatPendingJSON(nil))
	assert.Equal(t, "[]", formatPendingJSON([]string{}))
	assert.Equal(t, `["a"]`, formatPendingJSON([]string{"a"}))
	assert.Equal(t, `["a","b"]`, formatPendingJSON([]string{"a", "b"}))
	// Control characters and quotes are escaped defensively.
	assert.Equal(t, `["a b","c\""]`, formatPendingJSON([]string{"a\tb", "c\""}))
}
