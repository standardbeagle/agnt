package tools

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// profileFixtureRecords are deterministic incident records (zero timestamps so
// no age rendering) shared by the profile tests and the legacy-output pin.
func profileFixtureRecords() []protocol.IncidentRecord {
	return []protocol.IncidentRecord{
		{
			ID: "id-fp-gold-2", Fingerprint: "fp-gold-2", Count: 1,
			Severity: "critical", Source: "process_crash", Category: "panic",
			Summary: "runtime error",
			Remediation: protocol.IncidentRemediation{
				PrimaryTool: "proc", PrimaryArgs: map[string]any{"action": "output"},
				SkillHint: "agnt-process-proxy",
			},
		},
		{
			ID: "id-fp-gold-1", Fingerprint: "fp-gold-1", Count: 5,
			Severity: "error", Source: "browser_js", Category: "TypeError",
			Summary: "Cannot read property",
			Remediation: protocol.IncidentRemediation{
				PrimaryTool: "proc", PrimaryArgs: map[string]any{"action": "output"},
				SkillHint: "agnt-process-proxy",
			},
		},
	}
}

func profileFixtureViews() []incidentView {
	recs := profileFixtureRecords()
	views := make([]incidentView, 0, len(recs))
	for _, rec := range recs {
		views = append(views, recordToView(rec))
	}
	return views
}

func profileFixtureOutput(views []incidentView) GetIncidentsOutput {
	return GetIncidentsOutput{
		Incidents:       views,
		InboxAfter:      inboxStats{Critical: 1, Error: 1, New: 2},
		PipelineEnabled: true,
	}
}

// legacyCompactPin is the byte-exact compact output of the fixture above,
// captured before the finding/profile change. profile=full must reproduce it
// exactly; any other profile is free to differ.
const legacyCompactPin = `=== Incidents (2) === [inbox: crit=1 err=1 warn=0 info=0 new=2]

[critical:process_crash] panic (1x)
  id: fp-gold-2
  runtime error
  next: proc action=output
  skill: agnt-process-proxy

[error:browser_js] TypeError (5x)
  id: fp-gold-1
  Cannot read property
  next: proc action=output
  skill: agnt-process-proxy

`

// TestGetIncidentsProfileFullMatchesLegacyOutput pins the pre-change compact
// output as the golden profile=full must reproduce byte-for-byte. profile=full
// is the hub-side identity projection (incident.ApplyProfile), so the render
// of the unprojected fixture IS the profile=full render.
func TestGetIncidentsProfileFullMatchesLegacyOutput(t *testing.T) {
	t.Parallel()
	views := profileFixtureViews()
	legacy := formatIncidentsCompact(profileFixtureOutput(views))
	if legacy != legacyCompactPin {
		t.Fatalf("legacy render drifted from pin:\n--- want ---\n%s\n--- got ---\n%s", legacyCompactPin, legacy)
	}
	entries := make([]incident.InboxEntry, 0, len(views))
	for _, v := range views {
		entries = append(entries, incident.InboxEntry{Fingerprint: v.Fingerprint, Severity: incident.Severity(v.Severity)})
	}
	full, err := incident.ApplyProfile(entries, "full")
	if err != nil {
		t.Fatalf("profile=full: %v", err)
	}
	if len(full) != len(entries) {
		t.Fatalf("profile=full must be the identity projection: got %d of %d entries", len(full), len(entries))
	}
	for i := range entries {
		if full[i].Fingerprint != entries[i].Fingerprint {
			t.Fatalf("profile=full reordered entry %d", i)
		}
	}
	got := formatIncidentsCompact(profileFixtureOutput(views))
	if got != legacyCompactPin {
		t.Errorf("profile=full must be byte-identical to legacy output:\n--- want ---\n%s\n--- got ---\n%s", legacyCompactPin, got)
	}
}

// TestFormatIncidentsCompact_EveryRowHasIDAndNext renders one incident per
// remediation route and asserts every row carries an id: line and a next:
// line whose arguments are complete — no placeholder tokens a caller would
// have to invent (selector, <...>).
func TestFormatIncidentsCompact_EveryRowHasIDAndNext(t *testing.T) {
	t.Parallel()
	views := make([]incidentView, 0, 12)
	for i, src := range []incident.Source{
		incident.SourceBrowserJS, incident.SourceHTTP5xx, incident.SourceHTTP4xx,
		incident.SourceTransportErr, incident.SourceProxyDiag, incident.SourceProcessAlert,
		incident.SourceProcessOutput, incident.SourceProcessCrash, incident.SourceBuildFail,
		incident.SourcePortConflict, incident.SourceShutdown, incident.SourceHookStopFail,
	} {
		ev := incident.NewIncidentEvent(src, incident.SeverityError, "cat", "msg",
			incident.Context{ProxyID: "dev", ProcessID: "web", Port: 3000, URL: "http://localhost:3000/"}, nil)
		rem := incident.Resolve(&ev)
		fp := strings.Repeat("f", 8) + string(rune('0'+i%10))
		views = append(views, recordToView(protocol.IncidentRecord{
			ID: "id-" + fp, Fingerprint: fp, Count: 1,
			Severity: string(ev.Severity), Source: string(ev.Source),
			Remediation: protocol.IncidentRemediation{
				PrimaryTool: rem.PrimaryTool, PrimaryArgs: rem.PrimaryArgs,
				FallbackTool: rem.FallbackTool, SkillHint: rem.SkillHint,
			},
		}))
	}
	out := formatIncidentsCompact(GetIncidentsOutput{Incidents: views, PipelineEnabled: true})
	if strings.Count(out, "  id: ") != len(views) {
		t.Errorf("every row must render id: — got %d of %d:\n%s", strings.Count(out, "  id: "), len(views), out)
	}
	if strings.Count(out, "  next: ") != len(views) {
		t.Errorf("every row must render next: — got %d of %d:\n%s", strings.Count(out, "  next: "), len(views), out)
	}
	for _, tok := range []string{"selector", "<", ">"} {
		if strings.Contains(out, tok) {
			t.Errorf("compact next: lines carry placeholder token %q:\n%s", tok, out)
		}
	}
}

// TestGetIncidentsProfileBugLimitsToTopFive: bug profile returns at most 5
// incidents ordered critical -> error -> warning -> info. The projection is
// hub-side (incident.ApplyProfile) so mark_read covers exactly these rows.
func TestGetIncidentsProfileBugLimitsToTopFive(t *testing.T) {
	t.Parallel()
	sevs := []incident.Severity{incident.SeverityInfo, incident.SeverityWarning, incident.SeverityError, incident.SeverityCritical}
	entries := make([]incident.InboxEntry, 0, 8)
	for i := 0; i < 8; i++ {
		sev := sevs[i%len(sevs)]
		entries = append(entries, incident.InboxEntry{
			Fingerprint: "fp-p-" + string(sev) + "-" + string(rune('0'+i)),
			Severity:    sev,
		})
	}
	got, err := incident.ApplyProfile(entries, "bug")
	if err != nil {
		t.Fatalf("profile=bug: %v", err)
	}
	if len(got) > 5 {
		t.Fatalf("profile=bug returned %d incidents, want <= 5", len(got))
	}
	rank := map[incident.Severity]int{incident.SeverityCritical: 0, incident.SeverityError: 1, incident.SeverityWarning: 2, incident.SeverityInfo: 3}
	for i := 1; i < len(got); i++ {
		if rank[got[i-1].Severity] > rank[got[i].Severity] {
			t.Errorf("profile=bug not severity-ordered at %d: %s before %s", i, got[i-1].Severity, got[i].Severity)
		}
	}
	if got[0].Severity != incident.SeverityCritical {
		t.Errorf("profile=bug top incident: got severity %q, want critical", got[0].Severity)
	}
}

// TestGetIncidentsProfileRejectsUnknown: an unknown profile is a validation
// error naming the value — never silently treated as the default.
func TestGetIncidentsProfileRejectsUnknown(t *testing.T) {
	t.Parallel()
	if _, err := incident.ApplyProfile(nil, "verbose"); err == nil || !strings.Contains(err.Error(), "verbose") {
		t.Errorf("incident.ApplyProfile must reject unknown profile naming the value, got %v", err)
	}
	if err := validateGetIncidentsInput(GetIncidentsInput{Profile: "verbose"}); err == nil || !strings.Contains(err.Error(), "verbose") {
		t.Errorf("validateGetIncidentsInput must reject unknown profile naming the value, got %v", err)
	}
	if err := validateGetIncidentsInput(GetIncidentsInput{Profile: "bug"}); err != nil {
		t.Errorf("profile=bug must validate, got %v", err)
	}
}
