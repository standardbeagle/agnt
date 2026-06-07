package daemon

import (
	"fmt"
	"strings"
)

// Notice is a surfaced silent-failure: a config-declared resource (proxy,
// script, port) that failed to start and has not since recovered. Notices are a
// reduction over the startup-error log, computed daemon-side and rendered as a
// dismissable banner on the overlay overview. JSON-bridged to the overlay's
// NoticeInfo (mirrors the StartupLogEntry / overlay split — no shared import).
type Notice struct {
	ID          string `json:"id"`          // "<domain>:<process_id>", stable per resource+domain
	Domain      string `json:"domain"`      // "proxy" | "script" | "port"
	Severity    string `json:"severity"`    // "error" | "warning"
	Resource    string `json:"resource"`    // short name, e.g. "dev"
	Summary     string `json:"summary"`     // one-line headline
	Detail      string `json:"detail"`      // full startup-log message
	Remediation string `json:"remediation"` // actionable hint, may be empty
	EventType   string `json:"event_type"`  // originating event_type
	Timestamp   string `json:"timestamp"`   // RFC3339 of the failure entry
}

// noticeRole classifies a startup-log event_type within a domain.
type noticeRole struct {
	domain   string
	failure  bool   // true = failure event, false = success/resolver
	severity string // only meaningful for failures
}

// noticeClassification maps startup-log event_types to a (domain, role).
// Unknown event_types are not notices and are ignored. Failure and success
// events for the same (process_id, domain) pair up: a success at or after the
// latest failure resolves it.
var noticeClassification = map[string]noticeRole{
	// proxy failures
	"proxy_creation_failed":         {domain: "proxy", failure: true, severity: "error"},
	"proxy_failed":                  {domain: "proxy", failure: true, severity: "error"},
	"proxy_skipped":                 {domain: "proxy", failure: true, severity: "warning"},
	"startup_proxy_fallback_failed": {domain: "proxy", failure: true, severity: "warning"},
	"proxy_event_dropped":           {domain: "proxy", failure: true, severity: "warning"},
	// proxy resolvers
	"proxy_started":               {domain: "proxy", failure: false},
	"startup_proxy_fallback_used": {domain: "proxy", failure: false},
	// script failures
	"failed":       {domain: "script", failure: true, severity: "error"},
	"start_failed": {domain: "script", failure: true, severity: "error"},
	// script resolvers
	"started":        {domain: "script", failure: false},
	"script_started": {domain: "script", failure: false},
	// port failures
	"port_conflict":              {domain: "port", failure: true, severity: "warning"},
	"port_conflict_detected":     {domain: "port", failure: true, severity: "warning"},
	"port_conflict_skipped":      {domain: "port", failure: true, severity: "warning"},
	"port_conflict_abort":        {domain: "port", failure: true, severity: "error"},
	"port_conflict_failed":       {domain: "port", failure: true, severity: "error"},
	"proxy_listen_port_conflict": {domain: "port", failure: true, severity: "error"},
	// port resolvers
	"port_conflict_killed": {domain: "port", failure: false},
}

// resourceKey identifies a resource within a domain. Proxy and script events
// share a process_id, so the domain is part of the key.
type resourceKey struct {
	processID string
	domain    string
}

// buildNotices reduces project-scoped startup-log entries into the set of
// active failure notices. For each (process_id, domain) it keeps the latest
// failure that has not been superseded by a success at or after it. Pure: no
// daemon state, no clock.
func buildNotices(entries []*StartupLogEntry) []Notice {
	type tracked struct {
		failure       *StartupLogEntry
		latestSuccess *StartupLogEntry
	}
	groups := map[resourceKey]*tracked{}

	for _, e := range entries {
		if e == nil {
			continue
		}
		role, ok := noticeClassification[e.EventType]
		if !ok {
			continue
		}
		key := resourceKey{processID: e.ProcessID, domain: role.domain}
		g := groups[key]
		if g == nil {
			g = &tracked{}
			groups[key] = g
		}
		if role.failure {
			if g.failure == nil || !e.Timestamp.Before(g.failure.Timestamp) {
				g.failure = e
			}
		} else {
			if g.latestSuccess == nil || e.Timestamp.After(g.latestSuccess.Timestamp) {
				g.latestSuccess = e
			}
		}
	}

	var notices []Notice
	for key, g := range groups {
		if g.failure == nil {
			continue
		}
		// Resolved if a success landed at or after the latest failure.
		if g.latestSuccess != nil && !g.latestSuccess.Timestamp.Before(g.failure.Timestamp) {
			continue
		}
		notices = append(notices, makeNotice(key, g.failure))
	}
	return notices
}

func makeNotice(key resourceKey, f *StartupLogEntry) Notice {
	resource := noticeResourceName(f)
	sev := noticeClassification[f.EventType].severity
	return Notice{
		ID:          key.domain + ":" + key.processID,
		Domain:      key.domain,
		Severity:    sev,
		Resource:    resource,
		Summary:     noticeSummary(key.domain, resource),
		Detail:      f.Message,
		Remediation: noticeRemediation(f),
		EventType:   f.EventType,
		Timestamp:   f.Timestamp.Format(timeRFC3339),
	}
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

// noticeResourceName prefers the prefix-stripped ScriptName; falls back to the
// trailing segment of the ProcessID ("basename-hash:name" -> "name").
func noticeResourceName(e *StartupLogEntry) string {
	if e.ScriptName != "" {
		return e.ScriptName
	}
	if i := strings.LastIndex(e.ProcessID, ":"); i >= 0 && i+1 < len(e.ProcessID) {
		return e.ProcessID[i+1:]
	}
	return e.ProcessID
}

func noticeSummary(domain, resource string) string {
	switch domain {
	case "proxy":
		return fmt.Sprintf("proxy %q not created", resource)
	case "script":
		return fmt.Sprintf("script %q failed to start", resource)
	case "port":
		return fmt.Sprintf("port conflict for %q", resource)
	default:
		return fmt.Sprintf("%s %q failed", domain, resource)
	}
}

// noticeRemediation derives an actionable hint from the failure. Empty when no
// specific hint applies — the banner then shows Detail alone.
func noticeRemediation(e *StartupLogEntry) string {
	if strings.Contains(e.Message, "allow_external") {
		return "Add allow-external true to the proxy block in .agnt.kdl, or change bind to localhost"
	}
	switch e.EventType {
	case "proxy_skipped":
		return "Fix the upstream script that failed, then restart"
	case "port_conflict", "port_conflict_detected", "port_conflict_skipped", "port_conflict_abort", "port_conflict_failed", "proxy_listen_port_conflict":
		if e.Port > 0 {
			return fmt.Sprintf("Free the port or run :kill-port %d", e.Port)
		}
		return "Free the port or run :kill-port <port>"
	}
	return ""
}
