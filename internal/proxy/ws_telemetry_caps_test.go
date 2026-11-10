package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The browser chooses every string in a telemetry frame, an untrusted tunnel
// visitor included, and each parsed entry is kept in the traffic log ring and
// the page tracker. A 16 MiB frame limit therefore let 100 frames carrying an
// 8 MiB page title retain ~800 MiB. These tests pin that every retained
// string and list from performance, interaction and mutation frames, and the
// frame envelope itself, is cut at parse.

const hugeTelemetryField = 1 << 20

// maxRetainedEventBytes bounds what one interaction or mutation may leave in
// the log after every field and list cap (~1000 entries are kept).
const maxRetainedEventBytes = 128 << 10

var hugeField = strings.Repeat("x", hugeTelemetryField)

func huge() string { return hugeField }

// retainedJSONBytes is the encoded size of what a frame left in the log.
func retainedJSONBytes(t *testing.T, entries []LogEntry) int {
	t.Helper()
	b, err := json.Marshal(entries)
	require.NoError(t, err)
	return len(b)
}

func TestDecodeWSMessage_CapsTheEnvelope(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"type": "performance", "url": huge(), "session_id": huge(), "frame_id": huge(),
	})
	require.NoError(t, err)
	msg, err := decodeWSMessage(raw)
	require.NoError(t, err)
	if len(msg.URL) > maxFrontendErrorFieldBytes {
		t.Errorf("URL kept %d bytes", len(msg.URL))
	}
	if len(msg.SessionID) > maxEnvelopeIDBytes || len(msg.FrameID) > maxEnvelopeIDBytes {
		t.Errorf("session/frame id kept %d/%d bytes", len(msg.SessionID), len(msg.FrameID))
	}
}

func TestHandlePerformance_CapsTitleAndResourceNames(t *testing.T) {
	c := newTestConnState(t)
	resources := make([]any, 8)
	for i := range resources {
		resources[i] = map[string]any{"name": huge(), "duration": 1.0}
	}
	c.handlePerformance(&wsMessage{Type: "performance", Data: map[string]any{
		"page_title": huge(), "resources": resources,
	}})
	entries := c.ps.Logger().Query(LogFilter{})
	require.Len(t, entries, 1)
	perf := entries[0].Performance
	require.NotNil(t, perf)
	if len(perf.PageTitle) > maxFrontendErrorFieldBytes {
		t.Errorf("page title kept %d bytes", len(perf.PageTitle))
	}
	for _, r := range perf.Resources {
		if len(r.Name) > maxFrontendErrorFieldBytes {
			t.Fatalf("resource name kept %d bytes", len(r.Name))
		}
	}
}

func TestHandleInteractions_CapsEveryRetainedField(t *testing.T) {
	c := newTestConnState(t)
	classes := make([]any, 1000)
	attrs := map[string]any{}
	for i := range classes {
		classes[i] = huge()[:1024+i]
		attrs[strings.Repeat("k", 10+i)] = huge()[:2048]
	}
	event := map[string]any{
		"event_type": huge(),
		"target": map[string]any{
			"selector": huge(), "tag": huge(), "id": huge(), "text": huge(),
			"classes": classes, "attributes": attrs,
		},
		"key":   map[string]any{"key": huge(), "code": huge()},
		"value": huge(),
		"data":  map[string]any{"blob": huge()},
	}
	c.handleInteractions(&wsMessage{Type: "interactions", Data: map[string]any{"events": []any{event}}})
	entries := c.ps.Logger().Query(LogFilter{})
	require.Len(t, entries, 1)
	if n := retainedJSONBytes(t, entries); n > maxRetainedEventBytes {
		t.Errorf("one interaction retained %d bytes, cap %d", n, maxRetainedEventBytes)
	}
}

func TestHandleMutations_CapsEveryRetainedField(t *testing.T) {
	c := newTestConnState(t)
	nodes := make([]any, 1000)
	for i := range nodes {
		nodes[i] = map[string]any{"selector": huge()[:4096], "tag": "div", "id": "x", "html": huge()[:8192]}
	}
	event := map[string]any{
		"mutation_type": huge(),
		"target":        map[string]any{"selector": huge(), "tag": huge(), "id": huge()},
		"added":         nodes,
		"removed":       nodes,
		"attribute":     map[string]any{"name": huge(), "old_value": huge(), "new_value": huge()},
		"triggered_by":  map[string]any{"type": huge(), "target": huge()},
	}
	c.handleMutations(&wsMessage{Type: "mutations", Data: map[string]any{"events": []any{event}}})
	entries := c.ps.Logger().Query(LogFilter{})
	require.Len(t, entries, 1)
	if n := retainedJSONBytes(t, entries); n > maxRetainedEventBytes {
		t.Errorf("one mutation retained %d bytes, cap %d", n, maxRetainedEventBytes)
	}
}
