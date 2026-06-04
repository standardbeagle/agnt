package incident

import (
	"fmt"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// FromHTTPEntry converts a logged HTTP request/response into an IncidentEvent.
// Returns (event, true) for 4xx/5xx responses; returns (zero, false) for 2xx/3xx
// which do not warrant incidents.
func FromHTTPEntry(he proxy.HTTPLogEntry, proxyID string) (IncidentEvent, bool) {
	var src Source
	var sev Severity
	var statusClass string

	switch {
	case he.StatusCode >= 500:
		src = SourceHTTP5xx
		sev = SeverityError
		statusClass = "5xx"
	case he.StatusCode >= 400:
		src = SourceHTTP4xx
		sev = SeverityWarning
		statusClass = "4xx"
	default:
		return IncidentEvent{}, false
	}

	msg := fmt.Sprintf("%s %s → %d", he.Method, he.URL, he.StatusCode)
	if he.Error != "" {
		msg += "\n" + he.Error
	} else if he.ResponseBody != "" {
		body := he.ResponseBody
		if len(body) > 500 {
			body = body[:500]
		}
		msg += "\n" + body
	}

	ev := NewIncidentEvent(
		src, sev, statusClass, msg,
		Context{ProxyID: proxyID, URL: he.URL},
		nil,
	)
	// Collapse the storm: one fingerprint per (source, status-class, proxy),
	// independent of the URL that NewIncidentEvent folded in.
	ev.Fingerprint = computeStormFingerprint(string(src), statusClass, proxyID)
	return ev, true
}
