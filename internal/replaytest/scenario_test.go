package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScenarioRoundTrip(t *testing.T) {
	s := &Scenario{
		Name:    "demo",
		Version: 1,
		BaseURL: "http://localhost:3000",
		Steps: []Step{{
			Index: 0, Kind: StepNavigate, Selector: "a",
			DOMSignature: "blake3:abc",
			Assertions:   []Assertion{{Selector: "h1", Type: AssertText, Expect: "Today"}},
		}},
		Recordings: []Recording{{
			Match:  MatchKey{Method: "GET", Path: "/api/items", QueryKeys: []string{"date"}},
			Status: 200, Headers: map[string]string{"content-type": "application/json"},
			BodyRef: "blob:0", Hits: 3,
		}},
		Blobs: map[string]string{"blob:0": `{"ok":true}`},
	}
	data, err := s.MarshalJSON()
	require.NoError(t, err)
	got, err := UnmarshalScenario(data)
	require.NoError(t, err)
	assert.Equal(t, s.Name, got.Name)
	assert.Equal(t, s.Steps[0].Assertions[0].Expect, got.Steps[0].Assertions[0].Expect)
	assert.Equal(t, 3, got.Recordings[0].Hits)
	assert.Equal(t, `{"ok":true}`, got.Blobs["blob:0"])
	assert.Equal(t, []string{"date"}, got.Recordings[0].Match.QueryKeys)
}
