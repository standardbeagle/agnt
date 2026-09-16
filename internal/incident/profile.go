package incident

import (
	"fmt"
	"sort"

	"github.com/standardbeagle/agnt/internal/finding"
)

// ApplyProfile projects a page of inbox entries by the triage profile the
// caller asked for. It runs hub-side, BEFORE cursor computation and
// mark-read, so a mark_read pull covers exactly the rows the caller sees —
// a client-side projection would mark read rows the profile dropped and
// sweep them past the cursor unseen.
//
//   - "" / full: identity (legacy behavior, byte-compatible output)
//   - bug: severity-ordered (critical first), capped at 5
//   - release: severity-ordered, all rows
//   - changed: unread rows only
//
// Unknown profiles are rejected by name. The input slice is never mutated.
func ApplyProfile(entries []InboxEntry, profile string) ([]InboxEntry, error) {
	switch finding.Profile(profile) {
	case "", finding.ProfileFull:
		return entries, nil
	case finding.ProfileBug:
		sorted := sortEntriesBySeverity(entries)
		if len(sorted) > 5 {
			sorted = sorted[:5]
		}
		return sorted, nil
	case finding.ProfileRelease:
		return sortEntriesBySeverity(entries), nil
	case finding.ProfileChanged:
		out := make([]InboxEntry, 0, len(entries))
		for _, e := range entries {
			if !e.Read {
				out = append(out, e)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown profile %q: want one of bug, changed, release, full", profile)
	}
}

// ProfileActive reports whether the profile projects the page (anything but
// the legacy identity), which is when cursor computation must follow the
// rendered rows rather than the examined page.
func ProfileActive(profile string) bool {
	switch finding.Profile(profile) {
	case "", finding.ProfileFull:
		return false
	default:
		return true
	}
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityError:
		return 1
	case SeverityWarning:
		return 2
	default:
		return 3
	}
}

func sortEntriesBySeverity(entries []InboxEntry) []InboxEntry {
	sorted := make([]InboxEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return severityRank(sorted[i].Severity) < severityRank(sorted[j].Severity)
	})
	return sorted
}
