package overlay

// AlertBatch grouping and formatting: how a set of matched alerts is
// rendered for delivery to the AI agent. Scanner core lives in alerts.go,
// delivery machinery in alert_delivery.go.

import (
	"fmt"
	"strings"
)

// AlertBatch is a collection of alert matches to be delivered together.
type AlertBatch struct {
	Matches  []*AlertMatch
	ScriptID string
	// Suppressed is the number of alerts dropped by the overload throttle
	// since the last flush (queue exceeded MaxPending while the agent was
	// busy). When > 0, Format appends a one-line summary so the agent knows
	// the stream was throttled rather than silently lossy.
	Suppressed int
}

// ProtectedOnly returns a batch holding only the protected (explicit user
// action) matches, or nil when there are none. Used by delivery callbacks
// that gate auto-generated alerts (forwarding pause) but must never drop
// user content. Suppressed is not carried over — the throttle note belongs
// to the auto-alert stream the caller is gating.
func (b *AlertBatch) ProtectedOnly() *AlertBatch {
	var matches []*AlertMatch
	for _, m := range b.Matches {
		if m.Protected {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	return &AlertBatch{Matches: matches, ScriptID: b.ScriptID}
}

// MaxSeverity returns the highest severity in the batch.
func (b *AlertBatch) MaxSeverity() AlertSeverity {
	hasSeverity := map[AlertSeverity]bool{}
	for _, m := range b.Matches {
		hasSeverity[m.Pattern.Severity] = true
	}
	if hasSeverity[AlertSeverityError] {
		return AlertSeverityError
	}
	if hasSeverity[AlertSeverityWarning] {
		return AlertSeverityWarning
	}
	return AlertSeverityInfo
}

// Format renders the batch as a human-readable message for the AI agent.
func (b *AlertBatch) Format() string {
	if len(b.Matches) == 0 {
		return ""
	}

	// Pre-rendered fast path: when every match in the batch carries
	// RenderedText, emit those joined directly. Used by non-process
	// sources (browser-JS errors, HTTP errors) whose framing is
	// determined upstream and would be wrong under the
	// "Script %q detected issues" wrapper.
	allRendered := true
	for _, m := range b.Matches {
		if m.RenderedText == "" {
			allRendered = false
			break
		}
	}
	if allRendered {
		var sb strings.Builder
		for _, m := range b.Matches {
			sb.WriteString(m.RenderedText)
			if !strings.HasSuffix(m.RenderedText, "\n") {
				sb.WriteByte('\n')
			}
		}
		b.appendSuppressedNote(&sb)
		return sb.String()
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[agnt process alert] Script %q detected issues:\n", b.ScriptID))

	// Group by severity
	bySeverity := map[AlertSeverity][]*AlertMatch{}
	for _, m := range b.Matches {
		bySeverity[m.Pattern.Severity] = append(bySeverity[m.Pattern.Severity], m)
	}

	// Output in severity order: error, warning, info
	for _, sev := range []AlertSeverity{AlertSeverityError, AlertSeverityWarning, AlertSeverityInfo} {
		matches := bySeverity[sev]
		if len(matches) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("\n%ss (%d):\n", capitalize(string(sev)), len(matches)))
		for _, m := range matches {
			// m.Line is raw process output — cap it (117 + "...") on a rune
			// boundary so a truncated multibyte sequence never reaches the agent
			// as invalid UTF-8 (the final sanitize at typeText would otherwise
			// mangle it).
			line := TruncateRunes(m.Line, 117, "...")
			sb.WriteString(fmt.Sprintf("  - %s\n", line))
		}
	}

	if bySeverity[AlertSeverityError] != nil {
		sb.WriteString("\nConsider restarting the dev server.\n")
	}

	b.appendSuppressedNote(&sb)
	return sb.String()
}

// appendSuppressedNote appends the overload-throttle summary line when the
// batch carries dropped alerts. Kept to a single line so a throttled burst
// stays token-cheap while still telling the agent the stream was capped.
func (b *AlertBatch) appendSuppressedNote(sb *strings.Builder) {
	if b.Suppressed <= 0 {
		return
	}
	fmt.Fprintf(sb, "[agnt] %d more alert(s) suppressed (queue full while agent busy)\n", b.Suppressed)
}

// capitalize uppercases the first letter of a string.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
