package incident

import (
	"strings"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// FromFrontendError converts a browser JS error captured by the proxy into an
// IncidentEvent. The error type name is extracted as the category.
func FromFrontendError(fe proxy.FrontendError, proxyID string) IncidentEvent {
	category := extractErrorType(fe.Error)
	if category == "" {
		category = extractErrorType(fe.Message)
	}
	if category == "" {
		category = "Error"
	}

	msg := fe.Message
	if fe.Stack != "" {
		msg = fe.Message + "\n" + fe.Stack
	}

	return NewIncidentEvent(
		SourceBrowserJS,
		SeverityError,
		category,
		msg,
		Context{ProxyID: proxyID, URL: fe.URL},
		nil,
	)
}

// extractErrorType pulls the "TypeError", "ReferenceError", etc. prefix from
// an error string. Returns empty string if no recognisable type prefix found.
func extractErrorType(s string) string {
	if idx := strings.Index(s, ":"); idx > 0 {
		prefix := strings.TrimSpace(s[:idx])
		// Accept only single-word identifiers (e.g. "TypeError", not "Cannot read …:").
		if !strings.ContainsAny(prefix, " \t\n/\\") && len(prefix) > 0 {
			return prefix
		}
	}
	return ""
}
