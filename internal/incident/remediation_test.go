package incident

import (
	"testing"
)

// allSources lists every Source constant for exhaustiveness checks.
var allSources = []Source{
	SourceBrowserJS,
	SourceHTTP5xx,
	SourceHTTP4xx,
	SourceTransportErr,
	SourceProxyDiag,
	SourceProcessAlert,
	SourceProcessOutput,
	SourceProcessCrash,
	SourceBuildFail,
	SourcePortConflict,
	SourceShutdown,
	SourceHookStopFail,
}

// ── exhaustiveness ────────────────────────────────────────────────────────────

func TestRoute_AllSourcesHaveEntry(t *testing.T) {
	t.Parallel()
	for _, src := range allSources {
		ev := NewIncidentEvent(src, SeverityError, "cat", "msg", Context{}, nil)
		r := Resolve(&ev)
		if r.PrimaryTool == "" {
			t.Errorf("Source %q: Resolve returned empty PrimaryTool", src)
		}
		if r.SkillHint == "" {
			t.Errorf("Source %q: Resolve returned empty SkillHint", src)
		}
	}
}

// ── context injection ─────────────────────────────────────────────────────────

func TestRoute_ContextInjection_BrowserJS_IncludesURL(t *testing.T) {
	t.Parallel()
	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "msg",
		Context{URL: "http://localhost:3000/dashboard", ProxyID: "dev"}, nil)
	r := Resolve(&ev)

	if r.PrimaryArgs["url"] != "http://localhost:3000/dashboard" {
		t.Errorf("url not injected: got %v", r.PrimaryArgs["url"])
	}
	if r.PrimaryArgs["proxy_id"] != "dev" {
		t.Errorf("proxy_id not injected: got %v", r.PrimaryArgs["proxy_id"])
	}
}

func TestRoute_ContextInjection_HTTP5xx_IncludesProxyAndProcess(t *testing.T) {
	t.Parallel()
	ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "500", "msg",
		Context{ProxyID: "api", ProcessID: "backend"}, nil)
	r := Resolve(&ev)

	if r.PrimaryArgs["proxy_id"] != "api" {
		t.Errorf("proxy_id not injected: got %v", r.PrimaryArgs["proxy_id"])
	}
	// fallback also gets context
	if r.FallbackArgs["process_id"] != "backend" {
		t.Errorf("fallback process_id not injected: got %v", r.FallbackArgs["process_id"])
	}
}

func TestRoute_ContextInjection_PortConflict_IncludesPort(t *testing.T) {
	t.Parallel()
	ev := FromPortConflict(8080, 12345, "node")
	r := Resolve(&ev)

	if r.PrimaryArgs["port"] != 8080 {
		t.Errorf("port not injected: got %v", r.PrimaryArgs["port"])
	}
}

// ── aggregate ─────────────────────────────────────────────────────────────────

func TestRoute_Aggregate_WeightedSkill(t *testing.T) {
	t.Parallel()
	// 5 browser_js (count=10 each) + 1 process_crash (count=1)
	// → dominant skill = "agnt-browser-debug"
	incidents := make([]IncidentView, 0, 6)
	for i := 0; i < 5; i++ {
		ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", "msg", Context{}, nil)
		incidents = append(incidents, IncidentView{
			Source:      ev.Source,
			Count:       10,
			Remediation: Resolve(&ev),
		})
	}
	crashEv := NewIncidentEvent(SourceProcessCrash, SeverityCritical, "crash", "msg", Context{}, nil)
	incidents = append(incidents, IncidentView{
		Source:      crashEv.Source,
		Count:       1,
		Remediation: Resolve(&crashEv),
	})

	skill, tools := AggregateRemediation(incidents)
	if skill != "agnt-browser-debug" {
		t.Errorf("dominant skill: got %q, want agnt-browser-debug", skill)
	}
	if len(tools) == 0 {
		t.Error("expected at least one tool suggestion")
	}
}

// ── unknown source fallback ───────────────────────────────────────────────────

func TestRoute_UnknownSource_ReturnsGenericFallback(t *testing.T) {
	t.Parallel()
	ev := IncidentEvent{Source: Source("unknown_future_source"), Severity: SeverityError}
	r := Resolve(&ev)

	if r.PrimaryTool != "get_incidents" {
		t.Errorf("unknown source PrimaryTool: got %q, want get_incidents", r.PrimaryTool)
	}
	if r.SkillHint != "systematic-debugging" {
		t.Errorf("unknown source SkillHint: got %q, want systematic-debugging", r.SkillHint)
	}
}
