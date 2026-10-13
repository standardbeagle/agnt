package incident

import (
	"regexp"
	"strings"
)

var (
	// ISO8601 timestamps, e.g. 2024-01-15T10:30:00Z or 2024-01-15 10:30:00.123
	reISO8601 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	// Slash-dated timestamps, e.g. Go's stdlib log prefix 2024/01/15 10:30:00
	// (optionally with microseconds). reISO8601 requires dashes, so without
	// this every line from a stdlib logger carried its own timestamp into the
	// fingerprint and never deduplicated.
	reSlashDate = regexp.MustCompile(`\d{4}/\d{2}/\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?`)
	// Bare time-of-day, e.g. vite's "10:32:15 AM" or tsc's "[10:32:15]". It
	// must start the line or follow whitespace/an opening bracket so the
	// line:col:offset tail of a source position is never mistaken for a clock.
	reTimeOfDay = regexp.MustCompile(`(^|[\s\[(])\d{1,2}:\d{2}:\d{2}(?:[.,]\d+)?(?:\s?[AaPp][Mm]\b)?`)
	// UUID v4/v7 patterns
	reUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	// Memory addresses
	reMemAddr = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	// Line:col in stack frames (e.g. :42:15, :10:5)
	reLineCol = regexp.MustCompile(`:\d+:\d+`)
	// Single line number (e.g. :42)
	reLineNum = regexp.MustCompile(`:\d+`)

	// Frame shapes. A line is a stack frame only when it LOOKS like one; "any
	// line containing :<digits>" also matched host:port endpoints, clock times
	// and compiler diagnostics, so `ECONNREFUSED 127.0.0.1:5432` and `…:6379`
	// collapsed to the same fingerprint.
	//
	//   JS (V8):        at fn (file.js:42:15)   |  at file.js:42:15
	//   JS (Gecko/JSC): fn@http://host/file.js:42:15
	//   Go:             /path/file.go:42 +0x1f  (the whole line is the position)
	//   Python:         File "path.py", line 42, in fn
	reFrameV8     = regexp.MustCompile(`^at\s+\S`)
	reFrameGecko  = regexp.MustCompile(`^\S*@\S+:\d+(?::\d+)?$`)
	reFrameBare   = regexp.MustCompile(`^\S+\.[A-Za-z]{1,5}:\d+(?::\d+)?(?:\s+\+\S+)?$`)
	reFramePython = regexp.MustCompile(`^File "[^"]+", line \d+`)

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
	msg = reSlashDate.ReplaceAllString(msg, "TIMESTAMP")
	// Strip UUIDs before epoch replacement to avoid mangling UUID digits
	msg = reUUID.ReplaceAllString(msg, "UUID")
	// Strip memory addresses
	msg = reMemAddr.ReplaceAllString(msg, "ADDR")

	// Process lines to handle stack frames
	lines := strings.Split(msg, "\n")
	firstAppFrameSeen := false
	for i, line := range lines {
		// Per line, so the ^ anchor sees each line's own clock prefix.
		line = reTimeOfDay.ReplaceAllString(line, "${1}TIME")
		lines[i] = line
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
	line = strings.TrimSpace(line)
	return reFrameV8.MatchString(line) ||
		reFrameGecko.MatchString(line) ||
		reFrameBare.MatchString(line) ||
		reFramePython.MatchString(line)
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
