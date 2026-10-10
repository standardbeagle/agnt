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
	require.Len(t, sc.Steps, 2)
	assert.Equal(t, StepNavigate, sc.Steps[0].Kind)
	assert.Equal(t, StepClick, sc.Steps[1].Kind)
	assert.Equal(t, "a#log", sc.Steps[1].Selector)
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

// TestRecorderEmitsReplayableSteps pins what a recording needs to replay from a
// blank browser: an opening navigate to the recorded page, only replayable
// events, one input step per field, and a presence check linking each step to
// the element the user touched next.
func TestRecorderEmitsReplayableSteps(t *testing.T) {
	ev := func(kind, sel, val string) proxy.LogEntry {
		return proxy.LogEntry{Type: proxy.LogTypeInteraction, Interaction: &proxy.InteractionEvent{
			EventType: kind, Value: val, URL: "http://localhost:41000/cart?tab=2#top",
			Target: proxy.InteractionTarget{Selector: sel},
		}}
	}
	sc := AssembleScenario("x", "http://localhost:3000", []proxy.LogEntry{
		ev("scroll", "body", ""),
		ev("focus", "#qty", ""),
		ev("input", "#qty", "1"),
		ev("input", "#qty", "12"),
		ev("click", "#buy", ""),
	})

	require.Len(t, sc.Steps, 3)
	assert.Equal(t, Step{Index: 0, Kind: StepNavigate, Selector: "/cart?tab=2",
		Assertions: []Assertion{{Selector: "#qty", Type: AssertPresent}}}, sc.Steps[0])
	assert.Equal(t, Step{Index: 1, Kind: StepInput, Selector: "#qty", Value: "12",
		Assertions: []Assertion{{Selector: "#buy", Type: AssertPresent}}}, sc.Steps[1])
	assert.Equal(t, Step{Index: 2, Kind: StepClick, Selector: "#buy"}, sc.Steps[2])
}

func TestRecorderStartPathFallsBackToDocumentThenRoot(t *testing.T) {
	doc := proxy.LogEntry{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{
		Method: "GET", URL: "/orders/9", StatusCode: 200,
		ResponseHeaders: map[string]string{"content-type": "text/html; charset=utf-8"},
	}}
	api := proxy.LogEntry{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{
		Method: "GET", URL: "/api/x", StatusCode: 200,
		ResponseHeaders: map[string]string{"Content-Type": "application/json"},
	}}
	assert.Equal(t, "/orders/9", AssembleScenario("x", "http://h", []proxy.LogEntry{api, doc}).Steps[0].Selector)
	assert.Equal(t, "/", AssembleScenario("x", "http://h", []proxy.LogEntry{api}).Steps[0].Selector)
}
