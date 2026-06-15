package replaytest

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorderAssemblesScenario(t *testing.T) {
	entries := []proxy.LogEntry{
		{Type: proxy.LogTypeInteraction, Interaction: &proxy.InteractionEvent{
			EventType: "click",
			Target:    proxy.InteractionTarget{Selector: "a#log"},
		}},
		{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{
			Method:          "GET",
			URL:             "/api/items/7?date=x&_=1700000000",
			StatusCode:      200,
			ResponseBody:    `{"ok":true}`,
			ResponseHeaders: map[string]string{"Content-Type": "application/json"},
		}},
	}
	sc := AssembleScenario("demo", "http://localhost:3000", entries)
	require.NotNil(t, sc)
	assert.Equal(t, "demo", sc.Name)
	require.Len(t, sc.Recordings, 1)
	assert.Equal(t, "/api/items/:id", sc.Recordings[0].Match.Path)
	assert.Equal(t, []string{"date"}, sc.Recordings[0].Match.QueryKeys)
	assert.Equal(t, `{"ok":true}`, sc.Blobs[sc.Recordings[0].BodyRef])
	require.Len(t, sc.Steps, 1)
	assert.Equal(t, StepClick, sc.Steps[0].Kind)
	assert.Equal(t, "a#log", sc.Steps[0].Selector)
}

func TestRecorderCoalescesIdenticalRecordings(t *testing.T) {
	mk := func() proxy.LogEntry {
		return proxy.LogEntry{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{
			Method: "GET", URL: "/api/ping", StatusCode: 200, ResponseBody: "pong",
		}}
	}
	sc := AssembleScenario("x", "http://h", []proxy.LogEntry{mk(), mk(), mk()})
	require.Len(t, sc.Recordings, 1)
	assert.Equal(t, 3, sc.Recordings[0].Hits)
	assert.Len(t, sc.Blobs, 3) // each HTTP entry out-lines its own blob
}

func TestRecorderStripsSchemeAndHost(t *testing.T) {
	sc := AssembleScenario("x", "http://h", []proxy.LogEntry{
		{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{
			Method: "GET", URL: "http://localhost:3000/api/items/42?q=1", StatusCode: 200,
		}},
	})
	require.Len(t, sc.Recordings, 1)
	assert.Equal(t, "/api/items/:id", sc.Recordings[0].Match.Path)
	assert.Equal(t, []string{"q"}, sc.Recordings[0].Match.QueryKeys)
}
