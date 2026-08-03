package incident

import (
	"fmt"
	"strings"

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

	// The readiness gate's own 503 is not a backend error to chase — it is agnt
	// telling the browser to retry while a dependency finishes binding. Left
	// unfiltered it produces one incident per request for the whole startup
	// race. This filter used to live only in get_errors; it belongs at ingest so
	// every agent-facing surface inherits it.
	if isReadinessSentinel(he.ResponseBody, he.Error) {
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

// isReadinessSentinel reports whether a logged response carries the proxy
// readiness gate's `agnt_proxy_not_ready` marker. The proxy handler writes it
// to both the body and the entry's synthetic Error field, so both are checked.
//
// A substring match rather than a JSON parse: the marker is unique enough that
// a false positive needs an upstream to return the literal deliberately, and
// the body is truncated in the log, so a parse would fail on exactly the large
// responses it would need to handle.
func isReadinessSentinel(responseBody, errField string) bool {
	return strings.Contains(errField, proxy.ReadinessSentinel) ||
		strings.Contains(responseBody, proxy.ReadinessSentinel)
}
