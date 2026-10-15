package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// TestProtocolInvestigationCommandsRoundTrip verifies the INVESTIGATION verb
// pair (GET / MERGE) and their typed payloads survive a format→parse round
// trip, using the same registry-driven parser path as every other agnt verb.
func TestProtocolInvestigationCommandsRoundTrip(t *testing.T) {
	registry := NewVerbRegistry()
	registry.RegisterVerb(VerbInvestigation)
	registry.RegisterSubVerbForVerb(VerbInvestigation, SubVerbGet, SubVerbMerge)

	// GET round-trip with the typed request payload.
	getPayload, err := json.Marshal(InvestigationGetRequest{SessionCode: "dev-1"})
	if err != nil {
		t.Fatalf("GET marshal failed: %v", err)
	}
	getCmd := &Command{Verb: VerbInvestigation, SubVerb: SubVerbGet, Data: getPayload}
	parser := NewParserWithRegistry(bytes.NewReader(FormatCommand(getCmd)), registry)
	parsed, err := parser.ParseCommand()
	if err != nil {
		t.Fatalf("GET ParseCommand() failed: %v", err)
	}
	if parsed.Verb != VerbInvestigation || parsed.SubVerb != SubVerbGet {
		t.Errorf("GET parsed = %s %s, want %s %s", parsed.Verb, parsed.SubVerb, VerbInvestigation, SubVerbGet)
	}
	var getReq InvestigationGetRequest
	if err := json.Unmarshal(parsed.Data, &getReq); err != nil {
		t.Fatalf("GET payload unmarshal failed: %v", err)
	}
	if getReq.SessionCode != "dev-1" {
		t.Errorf("GET session_code = %q, want dev-1", getReq.SessionCode)
	}

	// MERGE round-trip: sub-verb must lift out of the token stream and the
	// typed patch must survive base64 framing field-for-field.
	cursor := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	mergePayload, err := json.Marshal(InvestigationMergeRequest{
		SessionCode: "dev-1",
		Patch: InvestigationPatch{
			ActiveProxyID:  "px1",
			IncidentCursor: cursor,
			Findings:       []FindingRef{{Fingerprint: "fp1", Summary: "boom"}},
			FailedAreas:    []string{"responsive_audit"},
		},
	})
	if err != nil {
		t.Fatalf("MERGE marshal failed: %v", err)
	}
	mergeCmd := &Command{Verb: VerbInvestigation, SubVerb: SubVerbMerge, Data: mergePayload}
	parser = NewParserWithRegistry(bytes.NewReader(FormatCommand(mergeCmd)), registry)
	parsed, err = parser.ParseCommand()
	if err != nil {
		t.Fatalf("MERGE ParseCommand() failed: %v", err)
	}
	if parsed.Verb != VerbInvestigation || parsed.SubVerb != SubVerbMerge {
		t.Errorf("MERGE parsed = %s %s, want %s %s", parsed.Verb, parsed.SubVerb, VerbInvestigation, SubVerbMerge)
	}
	var mergeReq InvestigationMergeRequest
	if err := json.Unmarshal(parsed.Data, &mergeReq); err != nil {
		t.Fatalf("MERGE payload unmarshal failed: %v", err)
	}
	if mergeReq.SessionCode != "dev-1" || mergeReq.Patch.ActiveProxyID != "px1" {
		t.Errorf("MERGE payload mismatch: %+v", mergeReq)
	}
	if !mergeReq.Patch.IncidentCursor.Equal(cursor) {
		t.Errorf("MERGE incident_cursor = %v, want %v", mergeReq.Patch.IncidentCursor, cursor)
	}
	if len(mergeReq.Patch.Findings) != 1 || mergeReq.Patch.Findings[0].Fingerprint != "fp1" {
		t.Errorf("MERGE findings mismatch: %+v", mergeReq.Patch.Findings)
	}
	if len(mergeReq.Patch.FailedAreas) != 1 || mergeReq.Patch.FailedAreas[0] != "responsive_audit" {
		t.Errorf("MERGE failed_areas mismatch: %+v", mergeReq.Patch.FailedAreas)
	}

	// Result round-trip (the direction the client decodes).
	resultPayload, err := json.Marshal(InvestigationResult{
		SessionCode: "dev-1",
		Found:       true,
		Investigation: &Investigation{
			ActiveProxyID: "px1",
			UpdatedAt:     cursor,
		},
	})
	if err != nil {
		t.Fatalf("result marshal failed: %v", err)
	}
	var result InvestigationResult
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		t.Fatalf("result unmarshal failed: %v", err)
	}
	if !result.Found || result.Investigation == nil || result.Investigation.ActiveProxyID != "px1" {
		t.Errorf("result mismatch: %+v", result)
	}
}

// TestMergeInvestigation_RenamedFailedAreaIsRefreshed pins that naming an
// area again moves it to the most-recent end, so a cap overflow in the same
// merge evicts genuinely older areas instead of the one just re-reported.
func TestMergeInvestigation_RenamedFailedAreaIsRefreshed(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var initial []string
	for i := 0; i < MaxInvestigationFailedAreas; i++ {
		initial = append(initial, string(rune('a'+i)))
	}
	cur := MergeInvestigation(nil, InvestigationPatch{FailedAreas: initial}, now)

	// "a" is the oldest; re-name it alongside one new area.
	next := MergeInvestigation(cur, InvestigationPatch{FailedAreas: []string{"a", "new"}}, now)

	if len(next.FailedAreas) != MaxInvestigationFailedAreas {
		t.Fatalf("len = %d, want %d: %v", len(next.FailedAreas), MaxInvestigationFailedAreas, next.FailedAreas)
	}
	has := func(area string) bool {
		for _, a := range next.FailedAreas {
			if a == area {
				return true
			}
		}
		return false
	}
	if !has("a") {
		t.Errorf("re-named area %q was evicted: %v", "a", next.FailedAreas)
	}
	if has("b") {
		t.Errorf("oldest un-renamed area %q survived the overflow: %v", "b", next.FailedAreas)
	}
	if got := next.FailedAreas[len(next.FailedAreas)-1]; got != "new" {
		t.Errorf("last area = %q, want %q (patch order kept): %v", got, "new", next.FailedAreas)
	}
	if len(cur.FailedAreas) != MaxInvestigationFailedAreas || cur.FailedAreas[0] != "a" {
		t.Errorf("merge wrote through into the previous record: %v", cur.FailedAreas)
	}
}
