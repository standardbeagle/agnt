package incident

import (
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// FromFrontendError converts a browser JS error captured by the proxy into an
// IncidentEvent. The error type name is extracted as the category.
//
// frameID names the content frame that raised the error. It is carried on the
// context AND folded into the fingerprint, because under the always-wrap model
// each content frame is a distinct failing surface: the same TypeError in two
// frames is two failures. Callers that genuinely have no frame attribution pass
// "" and fingerprint exactly as before.
func FromFrontendError(fe proxy.FrontendError, proxyID, frameID string) IncidentEvent {
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
		Context{
			ProxyID:  proxyID,
			URL:      fe.URL,
			Location: FrontendErrorLocation(fe),
			FrameID:  frameID,
		},
		nil,
	)
}

// FrontendErrorLocation resolves the source position a frontend error points
// at: the first app-code stack frame, falling back to the reported
// source/line/col when the browser sent no stack.
//
// This is extracted at ingest rather than left to be read back out of the
// summary text. The summary caps at 200 bytes and framework frames routinely
// precede the app frame, so the position that identifies the bug is the part
// most likely to be truncated away.
func FrontendErrorLocation(fe proxy.FrontendError) string {
	if loc := firstAppFrameLocation(fe.Stack); loc != "" {
		return loc
	}
	if fe.Source != "" && fe.LineNo > 0 {
		return fmt.Sprintf("%s:%d:%d", fe.Source, fe.LineNo, fe.ColNo)
	}
	return ""
}

// firstAppFrameLocation returns the file:line:col of the first app-code frame
// in a JS/Go/Python stack trace, skipping runtime and vendor frames. Frame
// classification reuses isStackFrame/isAppFrame so the location this reports
// and the frame Canonicalize preserves are chosen by the same rules.
func firstAppFrameLocation(stack string) string {
	if stack == "" {
		return ""
	}
	for _, raw := range strings.Split(stack, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !isStackFrame(line) || !isAppFrame(line) {
			continue
		}

		// JS: "at fn (file:line:col)" or "at file:line:col".
		if rest, ok := strings.CutPrefix(line, "at "); ok {
			if lparen := strings.LastIndex(line, "("); lparen != -1 {
				if rparen := strings.LastIndex(line, ")"); rparen > lparen {
					return line[lparen+1 : rparen]
				}
			}
			return strings.TrimSpace(rest)
		}

		// Python: `File "path", line N, in fn`.
		if strings.Contains(line, "File \"") {
			cleaned := strings.TrimPrefix(strings.ReplaceAll(line, "\"", ""), "File ")
			parts := strings.Split(cleaned, ", ")
			if len(parts) >= 2 {
				return strings.TrimSpace(parts[0]) + ":" + strings.TrimPrefix(strings.TrimSpace(parts[1]), "line ")
			}
			continue
		}

		// Go: "/path/file.go:42 +0x1f" — drop the instruction offset.
		if plus := strings.LastIndex(line, " +0x"); plus > 0 {
			return strings.TrimSpace(line[:plus])
		}
		return line
	}
	return ""
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
