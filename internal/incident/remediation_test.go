package incident

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSkillHints_AreShipped pins every skill hint the routing table can emit so
// a typo (the historic "systematic-debugging" / "agnt-process-manager" /
// "agnt-browser-debug" that pointed at non-existent skills) fails CI. Each
// route's skill must be in ValidSkillHints, and every in-repo skill named there
// must exist on disk under .claude/skills/<name>.
func TestSkillHints_AreShipped(t *testing.T) {
	t.Parallel()

	check := func(name, skill string) {
		if skill == "" {
			t.Errorf("%s: empty skill hint", name)
			return
		}
		if !ValidSkillHints[skill] {
			t.Errorf("%s: skill %q not in ValidSkillHints (uninvokable skill hint)", name, skill)
		}
	}
	for src, r := range routes {
		check(string(src), r.skill)
	}
	check("genericFallback", genericFallback.skill)

	// In-repo skills must resolve to a real .claude/skills/<name>/SKILL.md.
	root := repoRoot(t)
	for _, name := range inRepoSkillHints {
		if !ValidSkillHints[name] {
			t.Errorf("inRepoSkillHints entry %q missing from ValidSkillHints", name)
		}
		path := filepath.Join(root, ".claude", "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("in-repo skill %q not found on disk: %s", name, path)
		}
	}
}

// repoRoot walks up from the test's working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from test dir")
		}
		dir = parent
	}
}

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
	// → dominant skill = "agnt:browser-debug"
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
	if skill != "agnt:browser-debug" {
		t.Errorf("dominant skill: got %q, want agnt:browser-debug", skill)
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
	if r.SkillHint != "daemon-debug" {
		t.Errorf("unknown source SkillHint: got %q, want daemon-debug", r.SkillHint)
	}
}
