package incident

import (
	"regexp"
	"strings"
)

var (
	// ISO8601 timestamps, e.g. 2024-01-15T10:30:00Z or 2024-01-15 10:30:00.123
	reISO8601 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	// UUID v4/v7 patterns
	reUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	// Memory addresses
	reMemAddr = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	// Line:col in stack frames (e.g. :42:15, :10:5)
	reLineCol = regexp.MustCompile(`:\d+:\d+`)
	// Single line number (e.g. :42)
	reLineNum = regexp.MustCompile(`:\d+`)

	// Runtime prefixes — frames from these paths are not "app frames"
	runtimePrefixes = []string{
		"node_modules/", "node_modules\\",
		"webpack/", "webpack\\",
		"vendor/", "vendor\\",
		"runtime/", "runtime\\",
		"<anonymous>",
		"internal/",
	}
)

// Canonicalize strips volatile parts of msg (timestamps, addresses, UUIDs,
// line numbers on non-app frames) so that two occurrences of the same logical
// error produce the same fingerprint regardless of when or where they fired.
func Canonicalize(msg string) string {
	// Strip ISO8601 timestamps
	msg = reISO8601.ReplaceAllString(msg, "TIMESTAMP")
	// Strip UUIDs before epoch replacement to avoid mangling UUID digits
	msg = reUUID.ReplaceAllString(msg, "UUID")
	// Strip memory addresses
	msg = reMemAddr.ReplaceAllString(msg, "ADDR")

	// Process lines to handle stack frames
	lines := strings.Split(msg, "\n")
	firstAppFrameSeen := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isStackFrame(trimmed) {
			if !firstAppFrameSeen && isAppFrame(trimmed) {
				firstAppFrameSeen = true
				// Keep first app frame location intact for identification
				continue
			}
			// Subsequent frames (runtime or non-first app): collapse line:col
			lines[i] = reLineCol.ReplaceAllString(line, ":L:C")
			lines[i] = reLineNum.ReplaceAllString(lines[i], ":L")
		}
	}
	return strings.Join(lines, "\n")
}

// isStackFrame returns true if the line looks like a stack trace entry.
func isStackFrame(line string) bool {
	// Go: "\t/path/to/file.go:42 +0x..."
	// JS: "at Function (file.js:42:15)" or "at file.js:42:15"
	return strings.HasPrefix(line, "\t") ||
		strings.HasPrefix(line, "    at ") ||
		strings.HasPrefix(line, "at ") ||
		reLineCol.MatchString(line) ||
		reLineNum.MatchString(line)
}

// isAppFrame returns true if the frame belongs to app code (not runtime/vendor).
func isAppFrame(line string) bool {
	lower := strings.ToLower(line)
	for _, prefix := range runtimePrefixes {
		if strings.Contains(lower, strings.ToLower(prefix)) {
			return false
		}
	}
	// Must reference a source file to be a frame at all
	return strings.Contains(line, ".go:") ||
		strings.Contains(line, ".js:") ||
		strings.Contains(line, ".jsx:") ||
		strings.Contains(line, ".ts:") ||
		strings.Contains(line, ".tsx:") ||
		strings.Contains(line, ".py:") ||
		strings.Contains(line, ".rb:") ||
		strings.Contains(line, ".rs:") ||
		strings.Contains(line, ".java:")
}
