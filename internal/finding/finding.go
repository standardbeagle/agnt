package finding

import "unicode/utf16"

// Finding is the shared contract every incident and audit projects into.
type Finding struct {
	ID        string     `json:"id"`
	Category  string     `json:"category,omitempty"`
	Severity  string     `json:"severity"`
	Evidence  string     `json:"evidence,omitempty"`
	Next      NextAction `json:"next"`
	DrillDown string     `json:"drill_down,omitempty"`
	Visual    bool       `json:"visual,omitempty"`
	Producer  Producer   `json:"producer"`
}

// NextAction is a pre-filled tool call with a one-line rationale. Its shape
// is identical to incident.ToolSuggestion, which is a type alias of it.
type NextAction struct {
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Rationale string         `json:"rationale"`
}

// Producer is the exact tool call that regenerates this finding. It is the
// input verify_change uses to re-run the producing check.
type Producer struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

// StableID reproduces the 8-char lowercase-hex FNV-1a 32-bit hash used by
// audit-utils.js auditComputeFindingID (pinned by audit_ids_test.go), over
// kind + "\x00" + selector + "\x00" + message. The kind prefix lets incident
// fingerprints and audit IDs be distinguished without collision.
//
// Like the JS implementation (charCodeAt), the hash runs over UTF-16 code
// units, not UTF-8 bytes: astral characters contribute a surrogate pair.
func StableID(kind, selector, message string) string {
	input := kind + "\x00" + selector + "\x00" + message
	h := uint32(0x811c9dc5)
	for _, unit := range utf16.Encode([]rune(input)) {
		h = h ^ uint32(unit)
		h = h + (h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24)
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = hex[h&0xf]
		h >>= 4
	}
	return string(out)
}
