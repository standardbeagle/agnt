package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// base timestamp generator: t0 + n seconds, so ordering is explicit and
// independent of wall clock.
func at(n int) time.Time {
	return time.Date(2026, 6, 7, 5, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Second)
}

func entry(processID, scriptName, level, eventType, message string, ts time.Time) *StartupLogEntry {
	return &StartupLogEntry{
		ProcessID:  processID,
		ScriptName: scriptName,
		Level:      level,
		EventType:  eventType,
		Message:    message,
		Timestamp:  ts,
	}
}

func noticeByID(notices []Notice, id string) (Notice, bool) {
	for _, n := range notices {
		if n.ID == id {
			return n, true
		}
	}
	return Notice{}, false
}

func TestBuildNotices_ProxyCreationFailureSurfaces(t *testing.T) {
	entries := []*StartupLogEntry{
		entry("space-f4a4:dev", "", "error", "proxy_creation_failed",
			"failed to create proxy space-f4a4:dev:localhost-5173: binding to 0.0.0.0 exposes the proxy to the network; set allow_external: true to confirm",
			at(1)),
	}

	notices := buildNotices(entries)

	require.Len(t, notices, 1)
	n := notices[0]
	assert.Equal(t, "proxy:space-f4a4:dev", n.ID)
	assert.Equal(t, "proxy", n.Domain)
	assert.Equal(t, "error", n.Severity)
	assert.Equal(t, "dev", n.Resource)
	assert.Equal(t, "proxy_creation_failed", n.EventType)
	assert.Contains(t, n.Summary, "dev")
	assert.NotEmpty(t, n.Detail)
	assert.Contains(t, n.Remediation, "allow-external")
}

func TestBuildNotices_ResolvedBySuccessIsDropped(t *testing.T) {
	entries := []*StartupLogEntry{
		entry("space-f4a4:dev", "", "error", "proxy_creation_failed", "boom", at(1)),
		entry("space-f4a4:dev", "dev", "info", "proxy_started", "proxy dev started", at(2)),
	}

	notices := buildNotices(entries)

	assert.Empty(t, notices, "a later proxy_started must resolve the proxy failure")
}

func TestBuildNotices_FallbackUsedResolvesFallbackFailure(t *testing.T) {
	entries := []*StartupLogEntry{
		entry("space-f4a4:dev", "dev", "warning", "startup_proxy_fallback_failed", "fallback failed", at(1)),
		entry("space-f4a4:dev", "dev", "info", "startup_proxy_fallback_used", "fallback proxy created", at(2)),
	}

	assert.Empty(t, buildNotices(entries))
}

func TestBuildNotices_DomainIsolationOnSharedProcessID(t *testing.T) {
	// proxy failed; the *script* later succeeds. Script success must NOT
	// resolve the proxy failure — they share a process_id.
	entries := []*StartupLogEntry{
		entry("space-f4a4:dev", "", "error", "proxy_creation_failed", "boom", at(1)),
		entry("space-f4a4:dev", "dev", "info", "started", "dev started", at(2)),
	}

	notices := buildNotices(entries)

	require.Len(t, notices, 1)
	assert.Equal(t, "proxy:space-f4a4:dev", notices[0].ID)
	assert.Equal(t, "proxy", notices[0].Domain)
}

func TestBuildNotices_ScriptFailureSurfaces(t *testing.T) {
	entries := []*StartupLogEntry{
		entry("proj-ab12:api", "api", "error", "start_failed", "command not found: uvicorn", at(1)),
	}

	notices := buildNotices(entries)

	require.Len(t, notices, 1)
	n := notices[0]
	assert.Equal(t, "script:proj-ab12:api", n.ID)
	assert.Equal(t, "script", n.Domain)
	assert.Equal(t, "error", n.Severity)
	assert.Equal(t, "api", n.Resource)
}

func TestBuildNotices_LatestFailureDedup(t *testing.T) {
	// URL-detection path fails, then the 30s fallback also fails: two failure
	// entries for the same proxy must collapse to one notice (latest wins).
	entries := []*StartupLogEntry{
		entry("space-f4a4:dev", "", "error", "proxy_creation_failed", "first failure", at(1)),
		entry("space-f4a4:dev", "dev", "warning", "startup_proxy_fallback_failed", "second failure", at(2)),
	}

	notices := buildNotices(entries)

	require.Len(t, notices, 1)
	assert.Equal(t, "second failure", notices[0].Detail, "latest failure message wins")
}

func TestBuildNotices_Severity(t *testing.T) {
	cases := map[string]string{
		"proxy_creation_failed":         "error",
		"proxy_failed":                  "error",
		"proxy_skipped":                 "warning",
		"startup_proxy_fallback_failed": "warning",
		"proxy_event_dropped":           "warning",
		"failed":                        "error",
		"start_failed":                  "error",
		"port_conflict":                 "warning",
		"port_conflict_detected":        "warning",
		"port_conflict_skipped":         "warning",
		"port_conflict_abort":           "error",
		"port_conflict_failed":          "error",
		"proxy_listen_port_conflict":    "error",
	}
	for ev, wantSev := range cases {
		entries := []*StartupLogEntry{entry("p-0000:x", "x", wantSev, ev, "msg", at(1))}
		notices := buildNotices(entries)
		require.Len(t, notices, 1, "event %s should yield a notice", ev)
		assert.Equal(t, wantSev, notices[0].Severity, "severity for %s", ev)
	}
}

func TestBuildNotices_Remediation(t *testing.T) {
	allowExt, ok := noticeByID(buildNotices([]*StartupLogEntry{
		entry("p:dev", "dev", "error", "proxy_creation_failed", "set allow_external: true to confirm", at(1)),
	}), "proxy:p:dev")
	require.True(t, ok)
	assert.Contains(t, allowExt.Remediation, "allow-external")

	skipped := buildNotices([]*StartupLogEntry{
		entry("p:web", "web", "warning", "proxy_skipped", "proxy web skipped: depends on failed script api", at(1)),
	})
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Remediation, "upstream")

	portConflict := buildNotices([]*StartupLogEntry{
		{ProcessID: "p:db", ScriptName: "db", Level: "warning", EventType: "port_conflict_detected", Message: "port busy", Port: 5432, Timestamp: at(1)},
	})
	require.Len(t, portConflict, 1)
	assert.Contains(t, portConflict[0].Remediation, "kill-port")

	generic := buildNotices([]*StartupLogEntry{
		entry("p:svc", "svc", "error", "proxy_failed", "connection refused", at(1)),
	})
	require.Len(t, generic, 1)
	assert.Empty(t, generic[0].Remediation, "no hint for generic failures; detail carries the message")
}

func TestBuildNotices_PortConflictKilledResolves(t *testing.T) {
	entries := []*StartupLogEntry{
		{ProcessID: "p:db", ScriptName: "db", Level: "warning", EventType: "port_conflict_detected", Message: "port busy", Port: 5432, Timestamp: at(1)},
		{ProcessID: "p:db", ScriptName: "db", Level: "info", EventType: "port_conflict_killed", Message: "port cleared", Port: 5432, Timestamp: at(2)},
	}

	assert.Empty(t, buildNotices(entries))
}

func TestBuildNotices_UnknownEventTypeIgnored(t *testing.T) {
	entries := []*StartupLogEntry{
		entry("p:dev", "dev", "info", "config_loaded", "1 scripts, 1 proxies", at(1)),
		entry("p:dev", "dev", "info", "starting", "starting dev", at(2)),
	}
	assert.Empty(t, buildNotices(entries))
}

func TestBuildNotices_Empty(t *testing.T) {
	assert.Empty(t, buildNotices(nil))
	assert.Empty(t, buildNotices([]*StartupLogEntry{}))
}

func TestBuildNotices_MultipleResources(t *testing.T) {
	entries := []*StartupLogEntry{
		entry("p:dev", "", "error", "proxy_creation_failed", "boom", at(1)),
		entry("p:api", "api", "error", "start_failed", "nope", at(1)),
		entry("p:web", "web", "info", "started", "web started", at(1)), // healthy, no notice
	}

	notices := buildNotices(entries)

	require.Len(t, notices, 2)
	_, hasProxy := noticeByID(notices, "proxy:p:dev")
	_, hasScript := noticeByID(notices, "script:p:api")
	assert.True(t, hasProxy)
	assert.True(t, hasScript)
}
