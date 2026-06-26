package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// findingID generates a stable 8-char hex ID for a finding.
// It hashes the concatenation of the provided parts with sha256 and
// returns the first 4 bytes encoded as lowercase hex (8 chars).
// The same inputs always produce the same output, making IDs stable
// across runs and suitable for highlight lookups.
func findingID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:4])
}

// alertMapToUnifiedError converts a raw daemon AlertEntry JSON map into a
// unifiedError. Kept separate from collectProcessAlerts so it can be
// tested without a live daemon client.
//
// Process-lifecycle entries (category "process_lifecycle") get special
// handling: the category is rendered as "PROCESS LIFECYCLE" for the
// compact output, and the description is prepended to the message so
// agents see the exit-code summary plus the stderr tail.
func alertMapToUnifiedError(am map[string]interface{}) *unifiedError {
	severity := getString(am, "severity")
	level := "error"
	if severity == "warning" || severity == "info" {
		level = "warning"
	}

	scriptID := getString(am, "script_id")
	source := "process:" + scriptID

	rawCategory := getString(am, "category")
	category := strings.ToUpper(rawCategory)
	if category == "" {
		category = "PROCESS ERROR"
	}
	// Humanise snake_case categories. The daemon uses "process_lifecycle"
	// for exit events; compact output reads cleaner as "PROCESS LIFECYCLE".
	if strings.Contains(category, "_") {
		category = strings.ReplaceAll(category, "_", " ")
	}

	description := getString(am, "description")
	line := getString(am, "line")
	ts := getTime(am, "timestamp")

	// For lifecycle entries the description carries the exit summary
	// ("process X exited (code 1, reason crash, uptime 12m34s)") and the
	// line carries the stderr tail that explains WHY. Combine both so
	// the agent gets the full picture in one entry.
	message := line
	if rawCategory == "process_lifecycle" && description != "" {
		if message == "" || message == description {
			message = description
		} else {
			message = description + " — " + message
		}
	} else if message == "" {
		message = description
	}

	ue := &unifiedError{
		Source:   source,
		Severity: level,
		Category: category,
		Message:  message,
		LastSeen: ts,
		Count:    1,
	}
	ue.ID = findingID(ue.Source, ue.Category, ue.Message, ue.Location)
	return ue
}

// convertStartupLogEntry converts a startup log entry map to a unified error.
// Returns nil for non-error/non-warning entries or entries with empty messages.
func convertStartupLogEntry(em map[string]interface{}) *unifiedError {
	level := getString(em, "level")
	if level != "error" && level != "warning" {
		return nil
	}

	message := getString(em, "message")
	if message == "" {
		return nil
	}

	severity := "error"
	if level == "warning" {
		severity = "warning"
	}

	scriptName := getString(em, "script_name")
	source := "startup:" + scriptName
	if scriptName == "" {
		source = "startup"
	}

	eventType := getString(em, "event_type")
	category := "STARTUP ERROR"
	if eventType != "" {
		category = strings.ToUpper(strings.ReplaceAll(eventType, "_", " "))
	}

	ts := getTime(em, "timestamp")

	ue := &unifiedError{
		Source:   source,
		Severity: severity,
		Category: category,
		Message:  message,
		LastSeen: ts,
		Count:    1,
	}
	ue.ID = findingID(ue.Source, ue.Category, ue.Message, ue.Location)
	return ue
}

// convertProxyEntry converts a proxy log entry (map form from IPC) to zero or
// more unified errors by extracting typed structs and delegating to the Direct
// variants, which contain the single source of truth for conversion logic.
func convertProxyEntry(proxyID, entryType string, em map[string]interface{}) []unifiedError {
	// frame_id lives on the entry envelope (not the typed payload) — see
	// proxy.LogEntry. Carried through for browser-sourced entries.
	frameID := getString(em, "frame_id")
	switch entryType {
	case "error":
		return convertJSErrorDirect(proxyID, extractFrontendError(em), frameID)
	case "http":
		return convertHTTPErrorDirect(proxyID, extractHTTPLogEntry(em))
	case "diagnostic":
		return convertDiagnosticErrorDirect(proxyID, extractProxyDiagnostic(em))
	case "custom":
		return convertCustomErrorDirect(proxyID, extractCustomLog(em), frameID)
	}
	return nil
}

// extractFrontendError builds a proxy.FrontendError from a map-form log entry.
func extractFrontendError(em map[string]interface{}) *proxy.FrontendError {
	errData, ok := em["error"].(map[string]interface{})
	if !ok {
		return nil
	}
	message := getString(errData, "message")
	if message == "" {
		return nil
	}
	return &proxy.FrontendError{
		Message:   message,
		Source:    getString(errData, "source"),
		LineNo:    getInt(errData, "lineno"),
		ColNo:     getInt(errData, "colno"),
		Stack:     getString(errData, "stack"),
		URL:       getString(errData, "url"),
		Timestamp: getTime(errData, "timestamp"),
	}
}

// extractHTTPLogEntry builds a proxy.HTTPLogEntry from a map-form log entry.
func extractHTTPLogEntry(em map[string]interface{}) *proxy.HTTPLogEntry {
	httpData, ok := em["http"].(map[string]interface{})
	if !ok {
		return nil
	}
	return &proxy.HTTPLogEntry{
		Method:       getString(httpData, "method"),
		URL:          getString(httpData, "url"),
		StatusCode:   getInt(httpData, "status_code"),
		ResponseBody: getString(httpData, "response_body"),
		Error:        getString(httpData, "error"),
		Timestamp:    getTime(httpData, "timestamp"),
	}
}

// extractProxyDiagnostic builds a proxy.ProxyDiagnostic from a map-form log entry.
func extractProxyDiagnostic(em map[string]interface{}) *proxy.ProxyDiagnostic {
	diagData, ok := em["diagnostic"].(map[string]interface{})
	if !ok {
		return nil
	}
	return &proxy.ProxyDiagnostic{
		Level:     proxy.ProxyDiagnosticLevel(getString(diagData, "level")),
		Event:     getString(diagData, "event"),
		Message:   getString(diagData, "message"),
		Timestamp: getTime(diagData, "timestamp"),
	}
}

// extractCustomLog builds a proxy.CustomLog from a map-form log entry.
func extractCustomLog(em map[string]interface{}) *proxy.CustomLog {
	customData, ok := em["custom"].(map[string]interface{})
	if !ok {
		return nil
	}
	return &proxy.CustomLog{
		Level:     getString(customData, "level"),
		Message:   getString(customData, "message"),
		URL:       getString(customData, "url"),
		Timestamp: getTime(customData, "timestamp"),
	}
}

// parseSince parses a "since" parameter as either RFC3339 or a Go duration string.
// Returns nil if the string is empty or unparseable.
func parseSince(since string) *time.Time {
	if since == "" {
		return nil
	}

	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, since); err == nil {
		return &t
	}

	// Try Go duration (e.g. "5m", "1h", "30s")
	if d, err := time.ParseDuration(since); err == nil {
		t := time.Now().Add(-d)
		return &t
	}

	return nil
}

// convertJSErrorDirect converts a FrontendError struct to a unified error.
// frameID attributes it to the emitting content frame (always-wrap model).
func convertJSErrorDirect(proxyID string, fe *proxy.FrontendError, frameID string) []unifiedError {
	if fe == nil || fe.Message == "" {
		return nil
	}

	message := fe.Message
	category := "JS Error"
	if idx := strings.Index(message, ":"); idx > 0 && idx < 30 {
		category = message[:idx]
		message = strings.TrimSpace(message[idx+1:])
	}

	location := ""
	if fe.Stack != "" {
		location = extractFirstAppFrame(fe.Stack)
	}
	if location == "" && fe.Source != "" && fe.LineNo > 0 {
		location = fmt.Sprintf("%s:%d:%d", fe.Source, fe.LineNo, fe.ColNo)
	}

	ue := unifiedError{
		Source:   "browser:js",
		Severity: "error",
		Category: category,
		Message:  message,
		Location: location,
		Page:     fe.URL,
		FrameID:  frameID,
		LastSeen: fe.Timestamp,
		Count:    1,
	}
	ue.ID = findingID(ue.Source, ue.Category, ue.Message, ue.Location)
	return []unifiedError{ue}
}

// convertHTTPErrorDirect converts an HTTPLogEntry struct to a unified error.
func convertHTTPErrorDirect(proxyID string, h *proxy.HTTPLogEntry) []unifiedError {
	if h == nil {
		return nil
	}

	if h.StatusCode < 400 && h.Error == "" {
		return nil
	}

	if isNoiseError(h.URL, h.StatusCode, h.ResponseBody, h.Error) {
		return nil
	}

	category := categorizeHTTPError(h.StatusCode)
	message := fmt.Sprintf("%s %s", h.Method, h.URL)
	if h.Error != "" {
		message += fmt.Sprintf(" → %q", truncate(h.Error, 200))
	} else if h.ResponseBody != "" {
		extracted := extractErrorMessage(h.ResponseBody, 200)
		if extracted != "" {
			message += fmt.Sprintf(" → %q", extracted)
		}
	}

	level := "error"
	if h.StatusCode >= 400 && h.StatusCode < 500 {
		level = "warning"
	}

	ue := unifiedError{
		Source:   "proxy:http",
		Severity: level,
		Category: category,
		Message:  message,
		LastSeen: h.Timestamp,
		Count:    1,
	}
	ue.ID = findingID(ue.Source, ue.Category, ue.Message, ue.Location)
	return []unifiedError{ue}
}

// convertDiagnosticErrorDirect converts a ProxyDiagnostic struct to a unified error.
func convertDiagnosticErrorDirect(proxyID string, d *proxy.ProxyDiagnostic) []unifiedError {
	if d == nil {
		return nil
	}

	level := string(d.Level)
	if level != "error" && level != "warning" {
		return nil
	}

	category := "PROXY DIAGNOSTIC"
	if d.Event != "" {
		category = strings.ToUpper(strings.ReplaceAll(d.Event, "_", " "))
	}

	ue := unifiedError{
		Source:   "proxy:diagnostic",
		Severity: level,
		Category: category,
		Message:  d.Message,
		LastSeen: d.Timestamp,
		Count:    1,
	}
	ue.ID = findingID(ue.Source, ue.Category, ue.Message, ue.Location)
	return []unifiedError{ue}
}

// convertCustomErrorDirect converts a CustomLog struct to a unified error.
// frameID attributes it to the emitting content frame (always-wrap model).
func convertCustomErrorDirect(proxyID string, c *proxy.CustomLog, frameID string) []unifiedError {
	if c == nil {
		return nil
	}

	if c.Level != "error" && c.Level != "warn" {
		return nil
	}

	unifiedLevel := "error"
	if c.Level == "warn" {
		unifiedLevel = "warning"
	}

	ue := unifiedError{
		Source:   "browser:custom",
		Severity: unifiedLevel,
		Category: "CUSTOM ERROR",
		Message:  c.Message,
		Page:     c.URL,
		FrameID:  frameID,
		LastSeen: c.Timestamp,
		Count:    1,
	}
	ue.ID = findingID(ue.Source, ue.Category, ue.Message, ue.Location)
	return []unifiedError{ue}
}

// deduplicateErrors merges errors with the same key, incrementing counts and keeping latest timestamp.
func deduplicateErrors(errors []unifiedError) []unifiedError {
	seen := make(map[string]int) // dedupKey -> index in result
	var result []unifiedError

	for _, e := range errors {
		key := e.dedupKey()
		if idx, ok := seen[key]; ok {
			result[idx].Count++
			if e.LastSeen.After(result[idx].LastSeen) {
				result[idx].LastSeen = e.LastSeen
			}
		} else {
			seen[key] = len(result)
			result = append(result, e)
		}
	}

	return result
}

// formatCompactErrors renders the compact text output.
func formatCompactErrors(errors []unifiedError, totalErrors, totalWarnings int) string {
	if len(errors) == 0 {
		return "No errors found."
	}

	var b strings.Builder

	// Split into errors and warnings
	var errs, warns []unifiedError
	for _, e := range errors {
		if e.Severity == "error" {
			errs = append(errs, e)
		} else {
			warns = append(warns, e)
		}
	}

	if len(errs) > 0 {
		fmt.Fprintf(&b, "=== Errors (%d) ===\n", totalErrors)
		for _, e := range errs {
			b.WriteString("\n")
			formatSingleError(&b, e)
		}
	}

	if len(warns) > 0 {
		if len(errs) > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "=== Warnings (%d) ===\n", totalWarnings)
		for _, e := range warns {
			b.WriteString("\n")
			formatSingleError(&b, e)
		}
	}

	return b.String()
}

// formatSingleError writes one error entry in compact format.
func formatSingleError(b *strings.Builder, e unifiedError) {
	ago := formatTimeAgo(e.LastSeen)
	countStr := ""
	if e.Count > 1 {
		countStr = fmt.Sprintf("%dx, latest ", e.Count)
	} else {
		countStr = "1x, "
	}

	idStr := ""
	if e.ID != "" {
		idStr = fmt.Sprintf(" #%s", e.ID)
	}
	fmt.Fprintf(b, "[%s] %s (%s%s)%s\n", e.Source, e.Category, countStr, ago, idStr)
	fmt.Fprintf(b, "  %s\n", truncate(e.Message, 200))

	if e.Location != "" {
		fmt.Fprintf(b, "  → %s\n", e.Location)
	}
	if e.Page != "" {
		fmt.Fprintf(b, "  page: %s\n", truncate(e.Page, 120))
	}
}

// extractFirstAppFrame parses a JS/Go/Python stack trace and returns the first app-code frame.
// Skips node_modules, internal frames, and Go runtime frames.
func extractFirstAppFrame(stack string) string {
	lines := strings.Split(stack, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip common framework/internal frames
		if strings.Contains(line, "node_modules/") ||
			strings.Contains(line, "node:internal/") ||
			strings.Contains(line, "<anonymous>") ||
			strings.Contains(line, "webpack/") ||
			strings.Contains(line, "webpack-internal") {
			continue
		}

		// JS stack trace: "at functionName (file:line:col)" or "at file:line:col"
		if strings.HasPrefix(line, "at ") {
			// Extract file:line:col from parentheses
			if lparen := strings.LastIndex(line, "("); lparen != -1 {
				rparen := strings.LastIndex(line, ")")
				if rparen > lparen {
					return line[lparen+1 : rparen]
				}
			}
			// No parentheses: "at file:line:col"
			return strings.TrimPrefix(line, "at ")
		}

		// Go stack trace: "    /path/to/file.go:line +0xNN"
		if strings.HasSuffix(line, ".go") || strings.Contains(line, ".go:") {
			// Skip Go runtime frames
			if strings.Contains(line, "runtime/") || strings.Contains(line, "runtime.") {
				continue
			}
			// Strip offset
			if plusIdx := strings.LastIndex(line, " +0x"); plusIdx > 0 {
				return line[:plusIdx]
			}
			return line
		}

		// Python: "  File \"path\", line N, in func"
		if strings.HasPrefix(line, "File ") || strings.Contains(line, "File \"") {
			// Extract path and line number
			line = strings.ReplaceAll(line, "\"", "")
			line = strings.TrimPrefix(line, "File ")
			parts := strings.Split(line, ", ")
			if len(parts) >= 2 {
				path := strings.TrimSpace(parts[0])
				lineNum := strings.TrimPrefix(strings.TrimSpace(parts[1]), "line ")
				return path + ":" + lineNum
			}
		}
	}

	return ""
}

// extractErrorMessage extracts the error message from a response body.
// Tries JSON fields (message, error, detail), strips HTML tags, or returns plain text.
func extractErrorMessage(body string, maxLen int) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}

	// Try JSON
	if body[0] == '{' {
		var obj map[string]interface{}
		if json.Unmarshal([]byte(body), &obj) == nil {
			for _, key := range []string{"message", "error", "detail", "error_description"} {
				if v, ok := obj[key]; ok {
					switch val := v.(type) {
					case string:
						return truncate(val, maxLen)
					case map[string]interface{}:
						if msg, ok := val["message"].(string); ok {
							return truncate(msg, maxLen)
						}
					}
				}
			}
		}
	}

	// Strip HTML tags if it looks like HTML
	if strings.Contains(body, "<") && strings.Contains(body, ">") {
		stripped := stripHTMLTags(body)
		stripped = strings.Join(strings.Fields(stripped), " ")
		return truncate(stripped, maxLen)
	}

	return truncate(body, maxLen)
}

// stripHTMLTags removes HTML tags from a string.
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			b.WriteByte(' ')
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isNoiseError returns true for HTTP errors that should be filtered out.
//
// The responseBody and errField parameters let callers pass whatever
// the log entry carries as a body / synthetic error; either may be
// empty. The body is checked for the `agnt_proxy_not_ready` sentinel
// so proxy readiness-gate 503s never reach the AI agent. Matching on
// the body (not the status code alone) keeps genuine upstream 5xx
// responses visible.
func isNoiseError(url string, statusCode int, responseBody, errField string) bool {
	// Proxy readiness-gate 503s — filter by sentinel, not by status.
	// These are generated by the agnt proxy when a `wait-for`
	// dependency is still pending (see internal/proxy/readiness.go).
	// Genuine 5xx responses from the upstream still flow through.
	if isProxyReadinessSentinel(responseBody, errField) {
		return true
	}

	// Skip redirects and not-modified
	if statusCode == 301 || statusCode == 302 || statusCode == 304 {
		return true
	}

	// Skip 404s for common noise files
	if statusCode == 404 {
		lower := strings.ToLower(url)
		noisePatterns := []string{
			".map",
			"favicon",
			".hot-update.",
			"__webpack_hmr",
			"sockjs-node",
			"ws://",
			"wss://",
		}
		for _, p := range noisePatterns {
			if strings.Contains(lower, p) {
				return true
			}
		}
	}

	return false
}

// isProxyReadinessSentinel returns true when either the response
// body or the synthetic Error field carries the well-known
// `agnt_proxy_not_ready` marker from the proxy readiness gate.
//
// Keyed off a substring match rather than full JSON parsing: the
// sentinel is unique enough that a false-positive would require an
// upstream to deliberately return the same literal. Both the body
// and the log entry's Error field are checked because the proxy
// handler writes the sentinel to both when it short-circuits a
// request (see proxy_handler.go handleProxy).
func isProxyReadinessSentinel(responseBody, errField string) bool {
	const sentinel = "agnt_proxy_not_ready"
	if errField != "" && strings.Contains(errField, sentinel) {
		return true
	}
	if responseBody != "" && strings.Contains(responseBody, sentinel) {
		return true
	}
	return false
}

// formatTimeAgo formats a duration since a timestamp as a human-readable string.
func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

// categorizeHTTPError returns a human-readable category for an HTTP status code.
func categorizeHTTPError(statusCode int) string {
	text := http.StatusText(statusCode)
	if text == "" {
		return fmt.Sprintf("%d Error", statusCode)
	}
	return fmt.Sprintf("%d %s", statusCode, text)
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
