package tools

import (
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// handleErrorRetentionAction executes the pin/unpin/clear actions of the
// get_errors tool. Pins copy an error out of the ring buffers into the
// daemon's pinned store (survives builds, restarts, and clears); clear
// retires the current unpinned errors, scoped to the caller's project (or
// one process via process_id).
func (dt *DaemonTools) handleErrorRetentionAction(input GetErrorsInput) (*mcp.CallToolResult, GetErrorsOutput, error) {
	switch input.Action {
	case "pin", "unpin":
		if input.ErrorID == "" {
			return fail[GetErrorsOutput](validationError("get_errors", fmt.Errorf("action %q requires error_id (the #id from a prior get_errors result)", input.Action)))
		}
		payload := protocol.AlertPinPayload{ID: input.ErrorID, Tag: input.Tag}
		payload.SessionCode, payload.Directory = dt.sessionScope()

		if input.Action == "unpin" {
			if err := dt.client.AlertUnpin(payload); err != nil {
				return fail[GetErrorsOutput]("unpin failed: " + err.Error())
			}
			return nil, GetErrorsOutput{Summary: fmt.Sprintf("Error %s unpinned — normal retention applies again.", input.ErrorID)}, nil
		}

		result, err := dt.client.AlertPin(payload)
		if err != nil {
			return fail[GetErrorsOutput]("pin failed: " + err.Error())
		}
		msg := getString(result, "message")
		if msg == "" {
			msg = fmt.Sprintf("Error %s pinned.", input.ErrorID)
		}
		return nil, GetErrorsOutput{Summary: msg}, nil

	case "clear":
		filter := protocol.AlertClearFilter{ProcessID: input.ProcessID, Global: input.Global}
		if !globalEnabled(input.Global) {
			filter.SessionCode, filter.Directory = dt.sessionScope()
		}
		result, err := dt.client.AlertClear(filter)
		if err != nil {
			return fail[GetErrorsOutput]("clear failed: " + err.Error())
		}
		msg := getString(result, "message")
		if msg == "" {
			msg = "Alerts cleared (pinned errors kept)."
		}
		return nil, GetErrorsOutput{Summary: msg}, nil
	}
	return fail[GetErrorsOutput](validationError("get_errors", fmt.Errorf("unknown action %q", input.Action)))
}

// collectPinnedErrors fetches the project's pinned errors from the daemon.
// Failures degrade to an empty list — the query path already surfaces its own
// collection warnings, and a missing pin section must not fail the whole view.
func (dt *DaemonTools) collectPinnedErrors(global *bool) []unifiedError {
	filter := protocol.AlertQueryFilter{Limit: 1, Global: global}
	if !globalEnabled(global) {
		filter.SessionCode, filter.Directory = dt.sessionScope()
	}
	result, err := dt.client.AlertQuery(filter)
	if err != nil {
		return nil
	}
	return parsePinnedFromQueryResult(result)
}

// parsePinnedFromQueryResult converts the "pinned" array of an ALERTS QUERY
// response into unified errors. Pure so it is unit-testable without a daemon.
func parsePinnedFromQueryResult(result map[string]interface{}) []unifiedError {
	raw, ok := result["pinned"].([]interface{})
	if !ok {
		return nil
	}
	var out []unifiedError
	for _, p := range raw {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		ue := unifiedError{
			Pinned:   true,
			Tag:      getString(pm, "tag"),
			ID:       getString(pm, "id"),
			Source:   getString(pm, "source"),
			Severity: normalizePinnedSeverity(getString(pm, "severity")),
			Category: getString(pm, "category"),
			Message:  getString(pm, "message"),
			Page:     getString(pm, "page"),
			Count:    1,
			LastSeen: getTime(pm, "first_seen"),
		}
		out = append(out, ue)
	}
	return out
}

// normalizePinnedSeverity folds store severities onto the two-level scale
// get_errors sorts on (critical→error, info→warning).
func normalizePinnedSeverity(sev string) string {
	switch sev {
	case "critical", "error":
		return "error"
	default:
		return "warning"
	}
}

// mergePinned overlays the pinned set onto live results: a live entry whose
// id matches a pin is stamped pinned in place (fresh data wins); pins with no
// live counterpart are appended as their saved copies.
func mergePinned(live []unifiedError, pinned []unifiedError) []unifiedError {
	if len(pinned) == 0 {
		return live
	}
	liveByID := make(map[string]int, len(live))
	for i, e := range live {
		if e.ID != "" {
			liveByID[e.ID] = i
		}
	}
	for _, p := range pinned {
		if idx, ok := liveByID[p.ID]; ok {
			live[idx].Pinned = true
			live[idx].Tag = p.Tag
			continue
		}
		live = append(live, p)
	}
	return live
}
