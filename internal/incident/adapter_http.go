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

	switch {
	case he.StatusCode >= 500:
		src = SourceHTTP5xx
		sev = SeverityError
	case he.StatusCode >= 400:
		src = SourceHTTP4xx
		sev = SeverityWarning
	default:
		return IncidentEvent{}, false
	}

	category := fmt.Sprintf("%d", he.StatusCode)
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

	return NewIncidentEvent(
		src, sev, category, msg,
		Context{ProxyID: proxyID, URL: he.URL},
		nil,
	), true
}
