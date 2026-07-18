package proxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogFilter_MessagePattern verifies the message_pattern filter matches only
// message-bearing entries (error/custom/diagnostic) whose message contains the
// substring, and excludes entry types with no message (e.g. HTTP).
func TestLogFilter_MessagePattern(t *testing.T) {
	now := time.Now()
	tl := NewTrafficLogger(100)
	tl.LogError(FrontendError{Message: "TypeError: boom happened", Timestamp: now})
	tl.LogCustom(CustomLog{Level: "info", Message: "saved boom draft", Timestamp: now})
	tl.LogDiagnostic(ProxyDiagnostic{Level: DiagnosticError, Message: "backend refused", Timestamp: now})
	tl.LogHTTP(HTTPLogEntry{Method: "GET", URL: "/boom", Timestamp: now})

	got := tl.Query(LogFilter{MessagePattern: "boom"})
	// error + custom messages contain "boom"; diagnostic and HTTP do not.
	require.Len(t, got, 2)
	types := map[LogEntryType]bool{}
	for _, e := range got {
		types[e.Type] = true
	}
	assert.True(t, types[LogTypeError])
	assert.True(t, types[LogTypeCustom])
	assert.False(t, types[LogTypeHTTP], "HTTP has no message and must be excluded when message_pattern is set")

	// A pattern only in the diagnostic message matches just the diagnostic.
	got2 := tl.Query(LogFilter{MessagePattern: "refused"})
	require.Len(t, got2, 1)
	assert.Equal(t, LogTypeDiagnostic, got2[0].Type)
}

// TestLogFilter_MinDurationMs verifies the min_duration_ms filter keeps only
// HTTP entries at or above the threshold and drops non-HTTP entries entirely.
func TestLogFilter_MinDurationMs(t *testing.T) {
	now := time.Now()
	tl := NewTrafficLogger(100)
	tl.LogHTTP(HTTPLogEntry{Method: "GET", URL: "/fast", Duration: 10 * time.Millisecond, Timestamp: now})
	tl.LogHTTP(HTTPLogEntry{Method: "GET", URL: "/slow", Duration: 500 * time.Millisecond, Timestamp: now})
	tl.LogError(FrontendError{Message: "not http", Timestamp: now})

	got := tl.Query(LogFilter{MinDurationMs: 100})
	require.Len(t, got, 1)
	require.NotNil(t, got[0].HTTP)
	assert.Equal(t, "/slow", got[0].HTTP.URL)

	// Threshold at the boundary is inclusive.
	assert.Len(t, tl.Query(LogFilter{MinDurationMs: 10}), 2)
	assert.Len(t, tl.Query(LogFilter{MinDurationMs: 501}), 0)
}

// TestLogFilter_InteractionAndMutationTypes verifies the (previously daemon-only,
// now tool-exposed) interaction_types / mutation_types filters. These are
// type-specific sub-filters (like the HTTP method filter), so they are paired
// with a Types filter to isolate the entry kind under test.
func TestLogFilter_InteractionAndMutationTypes(t *testing.T) {
	now := time.Now()
	tl := NewTrafficLogger(100)
	tl.LogInteraction(InteractionEvent{EventType: "click", Timestamp: now})
	tl.LogInteraction(InteractionEvent{EventType: "scroll", Timestamp: now})
	tl.LogMutation(MutationEvent{MutationType: "added", Timestamp: now})
	tl.LogMutation(MutationEvent{MutationType: "removed", Timestamp: now})

	clicks := tl.Query(LogFilter{
		Types:            []LogEntryType{LogTypeInteraction},
		InteractionTypes: []string{"click"},
	})
	require.Len(t, clicks, 1)
	assert.Equal(t, "click", clicks[0].Interaction.EventType)

	added := tl.Query(LogFilter{
		Types:         []LogEntryType{LogTypeMutation},
		MutationTypes: []string{"added"},
	})
	require.Len(t, added, 1)
	assert.Equal(t, "added", added[0].Mutation.MutationType)
}
