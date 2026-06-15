package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportRollup(t *testing.T) {
	r := NewReport("demo")
	r.AddSeedResult("baseline", true, nil)
	r.AddSeedResult("empty_array", false, []string{"h1 expected 'Today' got ''"})
	r.AddCrash("/log", "button.add", "TypeError: x is undefined")
	assert.False(t, r.Passed())
	assert.Equal(t, 1, r.CrashCount())

	data, err := r.JSON()
	require.NoError(t, err)
	assert.Contains(t, string(data), "empty_array")
	assert.Contains(t, string(data), "TypeError")

	clean := NewReport("ok")
	clean.AddSeedResult("baseline", true, nil)
	assert.True(t, clean.Passed())
}
