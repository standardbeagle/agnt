package incident

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBus_HTTPStorm_CollapsesToOneEntry(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("sess", nil, nil, nil)

	for i := 0; i < 50; i++ {
		ev, ok := FromHTTPEntry(
			httpEntry("GET", fmt.Sprintf("/api/e%d", i), 500), "dev")
		require.True(t, ok)
		bus.Publish(ev)
	}

	require.Eventually(t, func() bool {
		entries, _ := bus.QuerySession("sess", QueryFilter{})
		return len(entries) == 1 && entries[0].Count == 50
	}, 2*time.Second, 10*time.Millisecond,
		"50 distinct-URL 5xx collapse into one entry with Count==50")

	entries, _ := bus.QuerySession("sess", QueryFilter{})
	assert.Equal(t, MessageError, entries[0].Sample.Type)
	assert.Equal(t, 50, entries[0].DistinctURLs)
	assert.Len(t, entries[0].SampleURLs, 10)
}
