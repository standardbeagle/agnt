package incident

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInbox_StormEntry_AccumulatesBoundedSampleURLs(t *testing.T) {
	inbox := NewInbox("s1")
	fp := "stormfp00000000"
	for i := 0; i < 20; i++ {
		url := fmt.Sprintf("/api/e%d", i)
		ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "5xx",
			"GET "+url+" → 500", Context{ProxyID: "dev", URL: url}, nil)
		ev.Fingerprint = fp
		inbox.Ingest(&InboxEntry{
			Fingerprint: fp,
			LastSeenAt:  ev.ReceivedAt,
			Count:       1,
			Sample:      &ev,
			Severity:    SeverityError,
		})
	}

	results, _ := inbox.Query(QueryFilter{})
	require.Len(t, results, 1, "all 20 collapse into one entry")
	e := results[0]
	assert.Equal(t, 20, e.Count)
	assert.LessOrEqual(t, len(e.SampleURLs), 10, "sample list is capped at 10")
	assert.Greater(t, len(e.SampleURLs), 0)
	assert.Equal(t, 20, e.DistinctURLs, "distinct count tracks all 20 URLs")
}

func TestInbox_SampleURLs_DedupeRepeatedURL(t *testing.T) {
	inbox := NewInbox("s1")
	fp := "stormfp11111111"
	for i := 0; i < 5; i++ {
		ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "5xx",
			"GET /same → 500", Context{ProxyID: "dev", URL: "/same"}, nil)
		ev.Fingerprint = fp
		inbox.Ingest(&InboxEntry{
			Fingerprint: fp, LastSeenAt: ev.ReceivedAt, Count: 1,
			Sample: &ev, Severity: SeverityError,
		})
	}
	results, _ := inbox.Query(QueryFilter{})
	require.Len(t, results, 1)
	assert.Equal(t, []string{"/same"}, results[0].SampleURLs)
	assert.Equal(t, 1, results[0].DistinctURLs)
}
