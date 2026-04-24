package incident

import (
	"strings"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// transportEvents lists proxy diagnostic event names that map to SourceTransportErr.
var transportEvents = map[string]bool{
	"refused":   true,
	"timeout":   true,
	"error":     true,
	"dial":      true,
	"tls":       true,
	"io_error":  true,
	"net_error": true,
}

// FromProxyDiagnostic converts a proxy diagnostic event into an IncidentEvent.
// Info-level diagnostics are filtered out (returns zero, false). Transport-layer
// events (connection refused/timeout) use SourceTransportErr; all others use
// SourceProxyDiag.
func FromProxyDiagnostic(d proxy.ProxyDiagnostic, proxyID string) (IncidentEvent, bool) {
	if d.Level == proxy.DiagnosticInfo {
		return IncidentEvent{}, false
	}

	src := SourceProxyDiag
	if d.Category == "transport" || transportEvents[strings.ToLower(d.Event)] {
		src = SourceTransportErr
	}

	var sev Severity
	switch d.Level {
	case proxy.DiagnosticError:
		sev = SeverityError
	case proxy.DiagnosticWarning:
		sev = SeverityWarning
	default:
		sev = SeverityInfo
	}

	return NewIncidentEvent(
		src, sev, d.Event, d.Message,
		Context{ProxyID: proxyID, URL: d.URL},
		nil,
	), true
}
