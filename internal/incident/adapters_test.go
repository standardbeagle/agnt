package incident

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// ── browser ──────────────────────────────────────────────────────────────────

func TestFromFrontendError_ExtractsCategory(t *testing.T) {
	t.Parallel()
	fe := proxy.FrontendError{
		Message: "Cannot read property 'map' of undefined",
		Error:   "TypeError: Cannot read property 'map' of undefined",
		Stack:   "    at ProductList (src/components/List.tsx:42:15)",
		URL:     "http://localhost:3000/dashboard",
	}
	ev := FromFrontendError(fe, "dev")
	if ev.Source != SourceBrowserJS {
		t.Errorf("Source: got %q, want %q", ev.Source, SourceBrowserJS)
	}
	if ev.Severity != SeverityError {
		t.Errorf("Severity: got %q, want error", ev.Severity)
	}
	if ev.Category != "TypeError" {
		t.Errorf("Category: got %q, want TypeError", ev.Category)
	}
	if !strings.Contains(ev.Summary, "Cannot read property") {
		t.Errorf("Summary missing message: %q", ev.Summary)
	}
	if ev.Ctx.ProxyID != "dev" {
		t.Errorf("ProxyID: got %q, want dev", ev.Ctx.ProxyID)
	}
	if ev.Ctx.URL != "http://localhost:3000/dashboard" {
		t.Errorf("URL: got %q", ev.Ctx.URL)
	}
}

func TestFromFrontendError_FallbackCategory(t *testing.T) {
	t.Parallel()
	fe := proxy.FrontendError{Message: "some error without type prefix"}
	ev := FromFrontendError(fe, "")
	if ev.Category != "Error" {
		t.Errorf("Category fallback: got %q, want Error", ev.Category)
	}
}

func TestFromFrontendError_StackAppended(t *testing.T) {
	t.Parallel()
	fe := proxy.FrontendError{
		Message: "oops",
		Stack:   "at foo (bar.js:1:1)",
	}
	ev := FromFrontendError(fe, "")
	if !strings.Contains(ev.Summary, "at foo") {
		t.Errorf("stack not in summary: %q", ev.Summary)
	}
}

// ── http ─────────────────────────────────────────────────────────────────────

func TestFromHTTPEntry_5xxIsError(t *testing.T) {
	t.Parallel()
	he := proxy.HTTPLogEntry{Method: "POST", URL: "/api/foo", StatusCode: 500, Timestamp: time.Now()}
	ev, ok := FromHTTPEntry(he, "dev")
	if !ok {
		t.Fatal("expected incident, got false")
	}
	if ev.Source != SourceHTTP5xx {
		t.Errorf("Source: got %q, want http_5xx", ev.Source)
	}
	if ev.Severity != SeverityError {
		t.Errorf("Severity: got %q, want error", ev.Severity)
	}
	if ev.Category != "5xx" {
		t.Errorf("Category: got %q, want 5xx (storm status-class)", ev.Category)
	}
}

func TestFromHTTPEntry_4xxIsWarning(t *testing.T) {
	t.Parallel()
	he := proxy.HTTPLogEntry{Method: "GET", URL: "/missing", StatusCode: 404, Timestamp: time.Now()}
	ev, ok := FromHTTPEntry(he, "dev")
	if !ok {
		t.Fatal("expected incident")
	}
	if ev.Source != SourceHTTP4xx {
		t.Errorf("Source: got %q, want http_4xx", ev.Source)
	}
	if ev.Severity != SeverityWarning {
		t.Errorf("Severity: got %q, want warning", ev.Severity)
	}
}

func TestFromHTTPEntry_2xxSkipped(t *testing.T) {
	t.Parallel()
	he := proxy.HTTPLogEntry{Method: "GET", URL: "/ok", StatusCode: 200, Timestamp: time.Now()}
	_, ok := FromHTTPEntry(he, "dev")
	if ok {
		t.Error("2xx should not produce an incident")
	}
}

func TestFromHTTPEntry_3xxSkipped(t *testing.T) {
	t.Parallel()
	he := proxy.HTTPLogEntry{Method: "GET", URL: "/redirect", StatusCode: 302, Timestamp: time.Now()}
	_, ok := FromHTTPEntry(he, "dev")
	if ok {
		t.Error("3xx should not produce an incident")
	}
}

// ── proxy diag ───────────────────────────────────────────────────────────────

func TestFromProxyDiagnostic_InfoSkipped(t *testing.T) {
	t.Parallel()
	d := proxy.ProxyDiagnostic{Level: proxy.DiagnosticInfo, Event: "connect", Message: "ok"}
	_, ok := FromProxyDiagnostic(d, "dev")
	if ok {
		t.Error("info-level diagnostic should be skipped")
	}
}

func TestFromProxyDiagnostic_TransportErrSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		category string
		event    string
	}{
		{"transport", "error"},
		{"connection", "refused"},
		{"connection", "timeout"},
	}
	for _, tc := range cases {
		d := proxy.ProxyDiagnostic{
			Level:    proxy.DiagnosticError,
			Category: tc.category,
			Event:    tc.event,
			Message:  "connection refused",
		}
		ev, ok := FromProxyDiagnostic(d, "dev")
		if !ok {
			t.Errorf("category=%q event=%q: expected incident", tc.category, tc.event)
			continue
		}
		if ev.Source != SourceTransportErr {
			t.Errorf("category=%q event=%q: Source=%q, want transport_err", tc.category, tc.event, ev.Source)
		}
	}
}

func TestFromProxyDiagnostic_ProxyDiagSource(t *testing.T) {
	t.Parallel()
	d := proxy.ProxyDiagnostic{
		Level:    proxy.DiagnosticWarning,
		Category: "proxy",
		Event:    "restart",
		Message:  "proxy restarted",
	}
	ev, ok := FromProxyDiagnostic(d, "dev")
	if !ok {
		t.Fatal("expected incident")
	}
	if ev.Source != SourceProxyDiag {
		t.Errorf("Source: got %q, want proxy_diag", ev.Source)
	}
	if ev.Severity != SeverityWarning {
		t.Errorf("Severity: got %q, want warning", ev.Severity)
	}
}

// ── alert scanner ─────────────────────────────────────────────────────────────

func alertPattern(category, desc string, sev overlay.AlertSeverity) *overlay.AlertPattern {
	return &overlay.AlertPattern{
		ID:          "test-" + category,
		Category:    category,
		Description: desc,
		Severity:    sev,
	}
}

func TestFromAlertMatch_BuildCategories(t *testing.T) {
	t.Parallel()
	for _, cat := range []string{"webpack", "vite", "nextjs", "rebuild"} {
		m := &overlay.AlertMatch{
			Pattern:   alertPattern(cat, "build error", overlay.AlertSeverityError),
			Line:      "ERROR in ./src/index.js",
			Timestamp: time.Now(),
			ScriptID:  "frontend",
		}
		ev := FromAlertMatch(m, "app")
		if ev.Source != SourceBuildFail {
			t.Errorf("category %q: Source=%q, want build_fail", cat, ev.Source)
		}
	}
}

func TestFromAlertMatch_ProcessAlertCategory(t *testing.T) {
	t.Parallel()
	m := &overlay.AlertMatch{
		Pattern:   alertPattern("python", "syntax error", overlay.AlertSeverityError),
		Line:      "SyntaxError: unexpected indent",
		Timestamp: time.Now(),
		ScriptID:  "api",
	}
	ev := FromAlertMatch(m, "api")
	if ev.Source != SourceProcessAlert {
		t.Errorf("Source: got %q, want process_alert", ev.Source)
	}
	if ev.Ctx.ProcessID != "api" {
		t.Errorf("ProcessID: got %q, want api", ev.Ctx.ProcessID)
	}
}

func TestFromAlertMatch_BuildDescriptionFallback(t *testing.T) {
	t.Parallel()
	// "dotnet" category + "build failure" description → SourceBuildFail
	m := &overlay.AlertMatch{
		Pattern:   alertPattern("dotnet", ".NET build failure", overlay.AlertSeverityError),
		Line:      "Build FAILED",
		Timestamp: time.Now(),
	}
	ev := FromAlertMatch(m, "api")
	if ev.Source != SourceBuildFail {
		t.Errorf("description-based detection: Source=%q, want build_fail", ev.Source)
	}
}

func TestFromAlertMatch_SeverityMapped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in  overlay.AlertSeverity
		out Severity
	}{
		{overlay.AlertSeverityError, SeverityError},
		{overlay.AlertSeverityWarning, SeverityWarning},
		{overlay.AlertSeverityInfo, SeverityInfo},
	}
	for _, tc := range cases {
		m := &overlay.AlertMatch{
			Pattern:   alertPattern("generic", "crash", tc.in),
			Line:      "crash",
			Timestamp: time.Now(),
		}
		ev := FromAlertMatch(m, "p")
		if ev.Severity != tc.out {
			t.Errorf("severity %q: got %q, want %q", tc.in, ev.Severity, tc.out)
		}
	}
}

// ── process crash ─────────────────────────────────────────────────────────────

func TestFromProcessExit_Basic(t *testing.T) {
	t.Parallel()
	ev := FromProcessExit("frontend", "crash", 1, "panic: runtime error")
	if ev.Source != SourceProcessCrash {
		t.Errorf("Source: got %q", ev.Source)
	}
	if ev.Severity != SeverityError {
		t.Errorf("Severity: got %q", ev.Severity)
	}
	if ev.Ctx.ProcessID != "frontend" {
		t.Errorf("ProcessID: got %q", ev.Ctx.ProcessID)
	}
	if !strings.Contains(ev.Summary, "panic: runtime error") {
		t.Errorf("Summary missing stderr tail: %q", ev.Summary)
	}
	if !strings.Contains(ev.Summary, "code=1") {
		t.Errorf("Summary missing exit code: %q", ev.Summary)
	}
}

func TestFromProcessExit_EmptyStderr(t *testing.T) {
	t.Parallel()
	ev := FromProcessExit("api", "stopped", 0, "")
	if strings.Contains(ev.Summary, "\n") {
		t.Errorf("no newline expected when stderr empty: %q", ev.Summary)
	}
}

// ── port conflict ─────────────────────────────────────────────────────────────

func TestFromPortConflict_WithName(t *testing.T) {
	t.Parallel()
	ev := FromPortConflict(3000, 42, "node")
	if ev.Source != SourcePortConflict {
		t.Errorf("Source: got %q", ev.Source)
	}
	if ev.Severity != SeverityWarning {
		t.Errorf("Severity: got %q", ev.Severity)
	}
	if ev.Ctx.PID != 42 {
		t.Errorf("PID: got %d", ev.Ctx.PID)
	}
	if !strings.Contains(ev.Summary, "3000") {
		t.Errorf("Summary missing port: %q", ev.Summary)
	}
	if !strings.Contains(ev.Summary, "node") {
		t.Errorf("Summary missing process name: %q", ev.Summary)
	}
}

func TestFromPortConflict_NoName(t *testing.T) {
	t.Parallel()
	ev := FromPortConflict(8080, 99, "")
	if !strings.Contains(ev.Summary, "8080") {
		t.Errorf("Summary missing port: %q", ev.Summary)
	}
}

// ── lifecycle ─────────────────────────────────────────────────────────────────

func TestFromShutdown(t *testing.T) {
	t.Parallel()
	ev := FromShutdown("user requested shutdown")
	if ev.Source != SourceShutdown {
		t.Errorf("Source: got %q", ev.Source)
	}
	if ev.Severity != SeverityInfo {
		t.Errorf("Severity: got %q", ev.Severity)
	}
	if !strings.Contains(ev.Summary, "user requested") {
		t.Errorf("Summary: %q", ev.Summary)
	}
}

func TestFromShutdown_EmptyReason(t *testing.T) {
	t.Parallel()
	ev := FromShutdown("")
	if ev.Summary == "" {
		t.Error("empty reason should use fallback message")
	}
}

// ── hook stop failure ─────────────────────────────────────────────────────────

func TestFromHookStopFailure(t *testing.T) {
	t.Parallel()
	ev := FromHookStopFailure("sess-abc", "API error", "rate limit exceeded")
	if ev.Source != SourceHookStopFail {
		t.Errorf("Source: got %q", ev.Source)
	}
	if ev.Severity != SeverityError {
		t.Errorf("Severity: got %q", ev.Severity)
	}
	if ev.Ctx.SessionID != "sess-abc" {
		t.Errorf("SessionID: got %q", ev.Ctx.SessionID)
	}
	if !strings.Contains(ev.Summary, "rate limit") {
		t.Errorf("Summary missing details: %q", ev.Summary)
	}
}

func TestFromHookStopFailure_NoDetails(t *testing.T) {
	t.Parallel()
	ev := FromHookStopFailure("s", "timeout", "")
	if ev.Summary != "timeout" {
		t.Errorf("Summary: got %q, want timeout", ev.Summary)
	}
}

// ── NopBus ────────────────────────────────────────────────────────────────────

func TestNopBus_Publish(t *testing.T) {
	t.Parallel()
	var b Bus = NopBus{}
	// Must not panic or block.
	b.Publish(NewIncidentEvent(SourceShutdown, SeverityInfo, "test", "test", Context{}, nil))
}
