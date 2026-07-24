package alert

import (
	"regexp"
	"time"
)

// AlertSeverity indicates the severity of an alert match.
type AlertSeverity string

const (
	AlertSeverityError   AlertSeverity = "error"
	AlertSeverityWarning AlertSeverity = "warning"
	AlertSeverityInfo    AlertSeverity = "info"
)

// AlertPattern defines a regex pattern to match against process output.
type AlertPattern struct {
	ID          string
	Pattern     *regexp.Regexp
	Severity    AlertSeverity
	Category    string // e.g. "dotnet", "webpack", "go", "generic"
	Description string
}

// AlertSource tags where a match originated. Default zero value
// (AlertSourceProcess) covers regex-matched process output. Non-default
// sources route through the same dedup/batch/activity-defer queue but
// carry pre-rendered text in RenderedText so OnAlert can frame them
// without the process-alert wrapper.
type AlertSource string

const (
	// AlertSourceProcess is the default — match came from process stdout/stderr.
	AlertSourceProcess AlertSource = ""
	// AlertSourceBrowser is a browser-JS error injected from a proxy.
	AlertSourceBrowser AlertSource = "browser"
	// AlertSourceHTTP is an HTTP 4xx/5xx response injected from a proxy.
	AlertSourceHTTP AlertSource = "http"
	// AlertSourceUser is content originating from an explicit user action
	// (browser panel message, sketch, design-mode interaction). Such matches
	// set Protected so the overload throttle and dedup never drop them.
	AlertSourceUser AlertSource = "user"
)

// AlertMatch represents a single matched alert from process output.
type AlertMatch struct {
	Pattern   *AlertPattern
	Line      string
	Timestamp time.Time
	ScriptID  string
	// LifetimeToken is opaque caller context captured when the line is
	// produced and carried unchanged through deferred batch delivery.
	LifetimeToken any
	// Source tags origin for OnAlert dispatch. Empty = process-output.
	Source AlertSource
	// RenderedText, when set, is the pre-formatted PTY-ready text for
	// this match. Used by non-process sources whose framing differs from
	// the canonical "[agnt process alert] Script %q detected issues"
	// wrapper. AlertBatch.Format honors RenderedText when every match in
	// a batch carries it, otherwise falls back to the default wrapper.
	RenderedText string
	// Protected marks content from an explicit user action (panel message,
	// sketch, design interaction). Protected matches MUST NEVER be dropped:
	// they bypass dedup (a repeated user action is intentional) and are never
	// evicted by the overload throttle. They still honor activity-deferral so
	// they are not injected mid-response. See the messaging-queue skill.
	Protected bool
}
