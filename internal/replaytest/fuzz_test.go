package replaytest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFuzzPresetsTransformAndPreserveInput(t *testing.T) {
	orig := `{"items":[{"id":1,"name":"a"}],"count":1}`
	for _, name := range PresetNames() {
		p, ok := Preset(name)
		require.True(t, ok, name)
		in := orig
		out := p.Apply(200, in)
		assert.Equal(t, orig, in, "preset %s mutated input", name)
		switch name {
		case "empty_array":
			assert.Contains(t, out.Body, `[]`)
		case "http_error":
			assert.GreaterOrEqual(t, out.Status, 500)
		case "truncated_json":
			var v any
			assert.Error(t, json.Unmarshal([]byte(out.Body), &v), "should be invalid json")
		case "null_fields":
			assert.Contains(t, out.Body, `null`)
		}
	}
}

func TestFuzzUnknownPreset(t *testing.T) {
	_, ok := Preset("does_not_exist")
	assert.False(t, ok)
}
