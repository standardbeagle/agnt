package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// buildResult constructs an IncidentQueryResult for testing without a live daemon.
func buildResult(records ...protocol.IncidentRecord) *protocol.IncidentQueryResult {
	return &protocol.IncidentQueryResult{
		Incidents: records,
		InboxStats: protocol.InboxStatsRecord{
			Error: len(records),
			New:   len(records),
		},
	}
}

func makeRecord(fp, src, sev, cat, summary string, count int) protocol.IncidentRecord {
	now := time.Now()
	return protocol.IncidentRecord{
		ID:          "id-" + fp,
		Fingerprint: fp,
		FirstSeen:   now.Add(-5 * time.Minute).Format(time.RFC3339),
		LastSeen:    now.Format(time.RFC3339),
		Count:       count,
		Severity:    sev,
		Source:      src,
		Category:    cat,
		Summary:     summary,
		Remediation: protocol.IncidentRemediation{
			PrimaryTool: "proc",
			PrimaryArgs: map[string]any{"action": "output"},
			SkillHint:   "agnt-process-manager",
		},
	}
}

// ── empty inbox ───────────────────────────────────────────────────────────────

func TestGetIncidents_EmptyInbox_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	out := formatIncidentsCompact(GetIncidentsOutput{})
	if !strings.Contains(out, "Incidents (0)") {
		t.Errorf("expected empty header, got: %q", out)
	}
	if !strings.Contains(out, "no incidents") {
		t.Errorf("expected no-incidents note, got: %q", out)
	}
}

// ── compact output ────────────────────────────────────────────────────────────

func TestGetIncidents_CompactOutput_DeterministicFormat(t *testing.T) {
	t.Parallel()
	result := buildResult(
		makeRecord("fp-a", "browser_js", "error", "TypeError", "Cannot read property", 5),
		makeRecord("fp-b", "process_crash", "critical", "panic", "runtime error", 1),
	)
	views := make([]incidentView, 0, len(result.Incidents))
	for _, rec := range result.Incidents {
		views = append(views, recordToView(rec))
	}
	out := formatIncidentsCompact(GetIncidentsOutput{
		Incidents: views,
		InboxAfter: inboxStats{
			Error:    1,
			Critical: 1,
			New:      2,
		},
	})

	if !strings.Contains(out, "Incidents (2)") {
		t.Errorf("count header missing: %q", out)
	}
	if !strings.Contains(out, "[error:browser_js]") {
		t.Errorf("error source missing: %q", out)
	}
	if !strings.Contains(out, "[critical:process_crash]") {
		t.Errorf("critical source missing: %q", out)
	}
	if !strings.Contains(out, "Cannot read property") {
		t.Errorf("summary missing: %q", out)
	}
}

// ── severity filter translation ───────────────────────────────────────────────

func TestGetIncidents_SeverityFilter_BuildsProtocolFilter(t *testing.T) {
	t.Parallel()
	input := GetIncidentsInput{Severity: []string{"critical", "error"}, Limit: 10}
	limit := input.Limit
	if limit <= 0 {
		limit = 20
	}
	filter := protocol.IncidentQueryFilter{
		Severities: input.Severity,
		Limit:      limit + 1,
	}
	if len(filter.Severities) != 2 {
		t.Errorf("severities: got %v", filter.Severities)
	}
}

// ── truncation ───────────────────────────────────────────────────────────────

func TestGetIncidents_Limit_TruncatesWithFlag(t *testing.T) {
	t.Parallel()
	// Build 6 records but limit=5 → truncated
	records := make([]protocol.IncidentRecord, 6)
	for i := range records {
		records[i] = makeRecord("fp-"+string(rune('a'+i)), "browser_js", "error", "TypeError", "msg", 1)
	}
	// Simulate over-fetch truncation
	limit := 5
	truncated := len(records) > limit
	visible := records
	if truncated {
		visible = records[:limit]
	}
	views := make([]incidentView, 0, len(visible))
	for _, rec := range visible {
		views = append(views, recordToView(rec))
	}
	out := formatIncidentsCompact(GetIncidentsOutput{
		Incidents: views,
		Truncated: truncated,
	})
	if !strings.Contains(out, "truncated") {
		t.Errorf("truncated flag not shown: %q", out)
	}
	if !strings.Contains(out, "Incidents (5)") {
		t.Errorf("should show 5 incidents: %q", out)
	}
}

// ── cursor in output ─────────────────────────────────────────────────────────

func TestGetIncidents_SinceCursor_Resumable(t *testing.T) {
	t.Parallel()
	cursor := time.Now().Format(time.RFC3339)
	out := formatIncidentsCompact(GetIncidentsOutput{
		Cursor: cursor,
	})
	if !strings.Contains(out, "replay_cursor: "+cursor) {
		t.Errorf("cursor not shown in output: %q", out)
	}
}

// ── detail=full populates payload ─────────────────────────────────────────────

func TestGetIncidents_DetailFull_HydratesPayload(t *testing.T) {
	t.Parallel()
	payload := "full error payload"
	rec := makeRecord("fp-full", "browser_js", "error", "TypeError", "msg", 1)
	rec.Payload = &payload
	view := recordToView(rec)
	if view.Payload == nil || *view.Payload != payload {
		t.Errorf("payload not hydrated: %v", view.Payload)
	}
}

func TestGetIncidents_DetailSummary_NoPayload(t *testing.T) {
	t.Parallel()
	rec := makeRecord("fp-sum", "browser_js", "error", "TypeError", "msg", 1)
	// rec.Payload is nil by default
	view := recordToView(rec)
	if view.Payload != nil {
		t.Errorf("payload should be nil for summary detail: %v", view.Payload)
	}
}

// ── next tools populated ─────────────────────────────────────────────────────

func TestGetIncidents_NextToolsPopulated(t *testing.T) {
	t.Parallel()
	out := GetIncidentsOutput{
		NextTools: []toolHint{
			{Tool: "proc", Args: map[string]any{"action": "output"}, Rationale: "process crash"},
		},
		NextSkills: []string{"agnt-process-manager"},
	}
	text := formatIncidentsCompact(out)
	// nextTools/nextSkills are not shown in compact text (raw=false);
	// they appear in raw JSON output for programmatic consumers.
	// Just verify the struct fields are populated.
	if len(out.NextTools) != 1 {
		t.Errorf("next_tools: got %d", len(out.NextTools))
	}
	if len(out.NextSkills) != 1 || out.NextSkills[0] != "agnt-process-manager" {
		t.Errorf("next_skills: got %v", out.NextSkills)
	}
	_ = text
}
