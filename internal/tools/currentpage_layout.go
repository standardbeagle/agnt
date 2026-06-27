package tools

import (
	"encoding/json"
	"fmt"
)

// LayoutFinding is one cause→symptom layout diagnostic from the browser's
// __devtool.diagnoseLayoutIssues(): the symptom (selector), the offending
// ancestor (cause), and the correct fix plus the common wrong fix to avoid.
type LayoutFinding struct {
	Check         string `json:"check"`
	Severity      string `json:"severity"`
	Selector      string `json:"selector"`
	Cause         string `json:"cause,omitempty"`
	CauseProperty string `json:"cause_property,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Fix           string `json:"fix,omitempty"`
	Avoid         string `json:"avoid,omitempty"`
}

// PageLayoutOutput is the currentpage action:"layout" result — a live CSS/layout
// diagnosis of the open page (stacking/containing-block/clip/click traps).
type PageLayoutOutput struct {
	Findings []LayoutFinding `json:"findings,omitempty"`
	Count    int             `json:"count"`
	Scanned  int             `json:"scanned"`
	Capped   bool            `json:"capped,omitempty"`
	ByCheck  map[string]int  `json:"by_check,omitempty"`
	Hint     string          `json:"hint,omitempty"`
}

// parseLayoutDiagnostics projects the diagnose() JSON onto PageLayoutOutput. A
// payload carrying an `error` (e.g. the instrumentation bundle is absent) is a
// hard failure, not an empty result, so the caller can report it.
func parseLayoutDiagnostics(raw string) (PageLayoutOutput, error) {
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return PageLayoutOutput{}, fmt.Errorf("layout diagnose returned unparseable result: %w", err)
	}
	if probe.Error != "" {
		return PageLayoutOutput{}, fmt.Errorf("layout diagnose failed: %s", probe.Error)
	}

	var out PageLayoutOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return PageLayoutOutput{}, fmt.Errorf("layout diagnose returned unparseable result: %w", err)
	}

	switch {
	case out.Capped:
		out.Hint = fmt.Sprintf("Scanned only the first %d elements (budget cap); the page is larger, so results may be partial.", out.Scanned)
	case out.Count == 0:
		out.Hint = "No containing-block traps, ineffective z-index, click interceptions, or clipped descendants detected on the current page."
	}
	return out, nil
}
