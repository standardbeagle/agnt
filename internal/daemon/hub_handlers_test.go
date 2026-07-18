package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTimeString_Empty(t *testing.T) {
	t.Parallel()
	result, err := parseTimeString("")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestParseTimeString_RFC3339(t *testing.T) {
	t.Parallel()
	ts := "2025-01-15T10:30:00Z"
	result, err := parseTimeString(ts)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2025, result.Year())
	assert.Equal(t, time.January, result.Month())
	assert.Equal(t, 15, result.Day())
}

func TestParseTimeString_Duration(t *testing.T) {
	t.Parallel()
	before := time.Now()
	result, err := parseTimeString("5m")
	after := time.Now()

	require.NoError(t, err)
	require.NotNil(t, result)
	// Result should be ~5 minutes ago
	assert.True(t, result.After(before.Add(-5*time.Minute-time.Second)))
	assert.True(t, result.Before(after.Add(-5*time.Minute+time.Second)))
}

func TestParseTimeString_DurationHours(t *testing.T) {
	t.Parallel()
	before := time.Now()
	result, err := parseTimeString("1h")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.After(before.Add(-1*time.Hour-time.Second)))
}

func TestParseTimeString_Invalid(t *testing.T) {
	t.Parallel()
	// A non-empty value that is neither RFC3339 nor a Go duration must fail
	// loud, not silently drop the time bound (which would widen the query).
	for _, bad := range []string{"not-a-time", "yesterday", "5 minutes"} {
		result, err := parseTimeString(bad)
		require.Error(t, err, "expected loud error for %q", bad)
		assert.Nil(t, result)
	}
}

func TestConvertLogQueryFilter_InvalidSinceIsLoud(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{Since: "yesterday"}
	data, _ := json.Marshal(pf)
	_, err := convertLogQueryFilter(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "since")
}

func TestConvertLogQueryFilter_InvalidUntilIsLoud(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{Until: "whenever"}
	data, _ := json.Marshal(pf)
	_, err := convertLogQueryFilter(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "until")
}

func TestConvertLogQueryFilter_Empty(t *testing.T) {
	t.Parallel()
	filter, err := convertLogQueryFilter(nil)
	require.NoError(t, err)
	assert.Nil(t, filter.Since)
	assert.Nil(t, filter.Until)
	assert.Nil(t, filter.Types)
	assert.Equal(t, 0, filter.Limit)
}

func TestConvertLogQueryFilter_CarriesErrorsOnlyAndDiagnosticLevels(t *testing.T) {
	t.Parallel()
	// These two fields are the daemon's parity bridge for the legacy proxylog
	// query: proxy.LogFilter already applies them, but they must survive the
	// protocol → proxy.LogFilter conversion or daemon callers silently lose
	// the errors-only / diagnostic-level filtering legacy mode offered.
	pf := protocol.LogQueryFilter{
		ErrorsOnly:       true,
		DiagnosticLevels: []string{"error", "warning"},
	}
	data, _ := json.Marshal(pf)
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	assert.True(t, filter.ErrorsOnly)
	assert.Equal(t, []string{"error", "warning"}, filter.DiagnosticLevels)
}

func TestConvertLogQueryFilter_WithDurationSince(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{
		Since: "5m",
		Limit: 25,
	}
	data, _ := json.Marshal(pf)
	before := time.Now()
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	require.NotNil(t, filter.Since)
	assert.True(t, filter.Since.After(before.Add(-5*time.Minute-time.Second)))
	assert.Equal(t, 25, filter.Limit)
}

func TestConvertLogQueryFilter_WithRFC3339Since(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{
		Since: "2025-06-01T12:00:00Z",
	}
	data, _ := json.Marshal(pf)
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	require.NotNil(t, filter.Since)
	assert.Equal(t, 2025, filter.Since.Year())
	assert.Equal(t, time.June, filter.Since.Month())
}

func TestConvertLogQueryFilter_WithUntil(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{
		Until: "30s",
	}
	data, _ := json.Marshal(pf)
	before := time.Now()
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	require.NotNil(t, filter.Until)
	assert.True(t, filter.Until.After(before.Add(-30*time.Second-time.Second)))
}

func TestConvertLogQueryFilter_WithBothSinceAndUntil(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{
		Since: "1h",
		Until: "5m",
	}
	data, _ := json.Marshal(pf)
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	require.NotNil(t, filter.Since)
	require.NotNil(t, filter.Until)
	assert.True(t, filter.Since.Before(*filter.Until))
}

func TestConvertLogQueryFilter_CarriesExtendedFilters(t *testing.T) {
	t.Parallel()
	// These filters exist on proxy.LogFilter but were previously not carried
	// across the protocol → proxy.LogFilter conversion, so the tool boundary
	// could never reach them. They must survive the round trip.
	pf := protocol.LogQueryFilter{
		InteractionTypes: []string{"click", "scroll"},
		MutationTypes:    []string{"added"},
		Frames:           []string{"frameA"},
		MessagePattern:   "boom",
		MinDurationMs:    250,
	}
	data, _ := json.Marshal(pf)
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	assert.Equal(t, []string{"click", "scroll"}, filter.InteractionTypes)
	assert.Equal(t, []string{"added"}, filter.MutationTypes)
	assert.Equal(t, []string{"frameA"}, filter.Frames)
	assert.Equal(t, "boom", filter.MessagePattern)
	assert.Equal(t, int64(250), filter.MinDurationMs)
}

func TestConvertLogQueryFilter_WithTypes(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{
		Types: []string{"http", "error"},
	}
	data, _ := json.Marshal(pf)
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	require.Len(t, filter.Types, 2)
	assert.Equal(t, "http", string(filter.Types[0]))
	assert.Equal(t, "error", string(filter.Types[1]))
}

func TestConvertLogQueryFilter_AllFields(t *testing.T) {
	t.Parallel()
	pf := protocol.LogQueryFilter{
		Types:       []string{"http"},
		Methods:     []string{"GET", "POST"},
		URLPattern:  "/api/",
		StatusCodes: []int{500, 502},
		Since:       "10m",
		Until:       "1m",
		Limit:       50,
	}
	data, _ := json.Marshal(pf)
	filter, err := convertLogQueryFilter(data)
	require.NoError(t, err)

	assert.Len(t, filter.Types, 1)
	assert.Equal(t, []string{"GET", "POST"}, filter.Methods)
	assert.Equal(t, "/api/", filter.URLPattern)
	assert.Equal(t, []int{500, 502}, filter.StatusCodes)
	require.NotNil(t, filter.Since)
	require.NotNil(t, filter.Until)
	assert.Equal(t, 50, filter.Limit)
}
