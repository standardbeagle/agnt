package daemon

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// TestIncidentHydrationFailureIsReportedNotSwallowed pins the property that
// replaced get_errors' collection_warnings: a detail:"full" pull whose payload
// could not be read must say so. Returning the record with no payload and no
// warning is indistinguishable from an incident that never had one, which is
// exactly the partial-view-reads-as-clean failure the field exists to prevent.
func TestIncidentHydrationFailureIsReportedNotSwallowed(t *testing.T) {
	t.Parallel()

	now := time.Now()
	entry := incident.InboxEntry{
		Fingerprint: "fp-hydrate",
		FirstSeenAt: now,
		LastSeenAt:  now,
		Count:       1,
		Severity:    incident.SeverityError,
		Sample: &incident.IncidentEvent{
			ID:         "id-hydrate",
			Source:     incident.SourceBrowserJS,
			Category:   "TypeError",
			Summary:    "boom",
			PayloadRef: &incident.BlobRef{Hash: "deadbeef", Size: 4096, MIME: "text/plain"},
		},
	}
	entries := []incident.InboxEntry{entry}
	filter := protocol.IncidentQueryFilter{Detail: "full"}

	failing := func(string) ([]byte, error) { return nil, errors.New("blob evicted") }
	res := buildIncidentQueryResultWithHydrator(entries, incident.Stats{}, filter, failing)

	if len(res.Incidents) != 1 {
		t.Fatalf("want the record itself preserved, got %d incidents", len(res.Incidents))
	}
	if res.Incidents[0].Payload != nil {
		t.Error("a failed hydration must not fabricate a payload")
	}
	if len(res.CollectionWarnings) != 1 {
		t.Fatalf("want exactly one collection warning, got %v", res.CollectionWarnings)
	}
	// The warning has to name the incident and the cause, or a caller cannot act
	// on it or tell which of several records is incomplete.
	w := res.CollectionWarnings[0]
	for _, want := range []string{"fp-hydrate", "blob evicted"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not name %q", w, want)
		}
	}

	// The success path must stay silent: a warning on every full pull would be
	// noise the agent learns to ignore.
	ok := func(string) ([]byte, error) { return []byte("full payload"), nil }
	res = buildIncidentQueryResultWithHydrator(entries, incident.Stats{}, filter, ok)
	if len(res.CollectionWarnings) != 0 {
		t.Errorf("successful hydration must warn about nothing, got %v", res.CollectionWarnings)
	}
	if res.Incidents[0].Payload == nil || *res.Incidents[0].Payload != "full payload" {
		t.Error("successful hydration did not attach the payload")
	}

	// detail:"summary" never hydrates, so it can neither warn nor attach.
	res = buildIncidentQueryResultWithHydrator(entries, incident.Stats{}, protocol.IncidentQueryFilter{}, failing)
	if len(res.CollectionWarnings) != 0 || res.Incidents[0].Payload != nil {
		t.Errorf("summary detail must not hydrate: payload=%v warnings=%v", res.Incidents[0].Payload, res.CollectionWarnings)
	}
}
