package replaytest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerBundleEmitsShimAndWorker(t *testing.T) {
	s := &Scenario{
		Name: "demo", BaseURL: "http://x",
		Recordings: []Recording{{Match: MatchKey{Method: "GET", Path: "/api/items"}, Status: 200, BodyRef: "b0", Hits: 1}},
		Blobs:      map[string]string{"b0": `{"ok":true}`},
	}
	js, err := GenerateBundle(s, "empty_array")
	require.NoError(t, err)
	assert.Contains(t, js, "window.fetch")
	assert.Contains(t, js, "XMLHttpRequest")
	assert.Contains(t, js, "__replay_miss")
	assert.Contains(t, js, `"b0"`)
	assert.Contains(t, js, "empty_array")
	assert.Contains(t, js, "Blob(")
	assert.True(t, strings.Count(js, "postMessage") >= 1)
}

func TestWorkerBundleNoPresetIsClean(t *testing.T) {
	s := &Scenario{Name: "d", Recordings: nil, Blobs: map[string]string{}}
	js, err := GenerateBundle(s, "")
	require.NoError(t, err)
	assert.Contains(t, js, `"activePreset":""`)
}
