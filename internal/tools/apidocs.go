package tools

import (
	"sort"
	"strings"
)

//go:generate go run ../../scripts/gen-apidocs.go -scripts ../proxy/scripts -out apidocs_gen.go

// APISearchMatch is a compact entry returned by SearchAPIFunctions — just
// enough for the caller to decide which function to describe next without
// blowing the response budget on full parameter lists and examples.
type APISearchMatch struct {
	Name        string `json:"name"`
	Signature   string `json:"signature"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// APISearchResult is the response shape for the proxy exec search action.
type APISearchResult struct {
	Matches   []APISearchMatch `json:"matches"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated"`
}

// maxAPISearchResults caps the compact response so the AI agent never has
// to scroll past 10 hits. If the user's query is that ambiguous, they can
// refine with category or a more specific substring.
const maxAPISearchResults = 10

// matchTier ranks a single function against the lowercased query — lower
// is better. Exact name match wins, then name-prefix, then name-contains,
// then description/signature contains. This matters because substring
// matching alone produces noisy ordering (e.g. a `click` query returning
// `onInteraction` before `getLastClick` because it hit the description).
func matchTier(fn APIFunction, q string) int {
	name := strings.ToLower(fn.Name)
	switch {
	case name == q:
		return 0
	case strings.HasPrefix(name, q):
		return 1
	case strings.Contains(name, q):
		return 2
	case strings.Contains(strings.ToLower(fn.Description), q),
		strings.Contains(strings.ToLower(fn.Signature), q):
		return 3
	}
	return -1
}

// SearchAPIFunctions filters DevToolAPIFunctions by a case-insensitive
// substring query across name, description, and signature, with an
// optional category filter (exact, case-insensitive). Results are ranked
// by match tier (exact > prefix > substring-in-name > substring-elsewhere)
// then alphabetically by name, and capped at maxAPISearchResults. An
// empty query with a category returns everything in that category (still
// capped). Returns an empty Matches slice (not nil) when nothing matches.
func SearchAPIFunctions(query, category string) APISearchResult {
	q := strings.ToLower(strings.TrimSpace(query))
	cat := strings.ToLower(strings.TrimSpace(category))

	type scored struct {
		fn   APIFunction
		tier int
	}
	var hits []scored
	for _, fn := range DevToolAPIFunctions {
		if cat != "" && !strings.EqualFold(fn.Category, cat) {
			continue
		}
		tier := 0
		if q != "" {
			tier = matchTier(fn, q)
			if tier < 0 {
				continue
			}
		}
		hits = append(hits, scored{fn: fn, tier: tier})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].tier != hits[j].tier {
			return hits[i].tier < hits[j].tier
		}
		return hits[i].fn.Name < hits[j].fn.Name
	})

	total := len(hits)
	truncated := false
	if total > maxAPISearchResults {
		hits = hits[:maxAPISearchResults]
		truncated = true
	}

	matches := make([]APISearchMatch, len(hits))
	for i, h := range hits {
		matches[i] = APISearchMatch{
			Name:        h.fn.Name,
			Signature:   h.fn.Signature,
			Category:    h.fn.Category,
			Description: truncateDescription(h.fn.Description),
		}
	}
	return APISearchResult{
		Matches:   matches,
		Count:     len(matches),
		Truncated: truncated,
	}
}

// truncateDescription trims a description to its first sentence or 120
// runes, whichever comes first. Keeps the compact response readable when
// the generator picked up a multi-sentence JSDoc summary.
func truncateDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	if idx := strings.Index(desc, ". "); idx >= 0 && idx < 120 {
		return desc[:idx+1]
	}
	if len(desc) > 120 {
		return desc[:117] + "..."
	}
	return desc
}

// APIFunction describes a single function in the __devtool API.
type APIFunction struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Signature   string   `json:"signature"`
	Parameters  []string `json:"parameters,omitempty"`
	Returns     string   `json:"returns"`
	Example     string   `json:"example"`
}

// APICategory describes a category of functions.
type APICategory struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// devToolAPIDocs is the shape exposed to callers. The Functions slice is
// populated from DevToolAPIFunctions (see apidocs_gen.go) which is the
// code-generated catalog sourced from JSDoc in internal/proxy/scripts/*.js.
// Categories remain hand-curated here — they are a small, stable taxonomy
// that describes the groupings the generator tags functions with.
type devToolAPIDocs struct {
	Categories []APICategory
	Functions  []APIFunction
}

// DevToolAPIDocs contains the full API documentation. Functions is sourced
// from the generated DevToolAPIFunctions; Categories is authored here.
var DevToolAPIDocs = devToolAPIDocs{
	Categories: []APICategory{
		{Name: "logging", Description: "Send custom log messages to the proxy server"},
		{Name: "screenshot", Description: "Capture screenshots of the page or elements"},
		{Name: "inspection", Description: "Get detailed information about DOM elements"},
		{Name: "tree", Description: "Walk and navigate the DOM tree"},
		{Name: "visual", Description: "Check visibility and viewport state"},
		{Name: "layout", Description: "Diagnose layout issues (overflow, stacking, offscreen; diagnoseLayoutIssues = cause→symptom traps)"},
		{Name: "overlay", Description: "Highlight elements visually on the page"},
		{Name: "interactive", Description: "Interactive element selection and measurement"},
		{Name: "capture", Description: "Capture page state, styles, and network info"},
		{Name: "accessibility", Description: "Accessibility auditing and information"},
		{Name: "audit", Description: "Page quality audits (DOM complexity, CSS, security)"},
		{Name: "interactions", Description: "Track and query user interactions (clicks, keyboard, scroll)"},
		{Name: "mutations", Description: "Track and query DOM mutations (added, removed, modified)"},
		{Name: "indicator", Description: "Control the floating indicator bug"},
		{Name: "sketch", Description: "Wireframing and annotation mode"},
		{Name: "content", Description: "Content extraction, navigation, sitemaps, and markdown conversion"},
		{Name: "connection", Description: "WebSocket connection status"},
	},
	Functions: DevToolAPIFunctions,
}

// GetAPIOverview returns a high-level overview of all API categories and functions.
func GetAPIOverview() string {
	overview := "# __devtool API Reference\n\n"
	overview += "The proxy injects a `window.__devtool` object with diagnostic functions.\n\n"

	overview += "## Categories\n\n"
	for _, cat := range DevToolAPIDocs.Categories {
		overview += "- **" + cat.Name + "**: " + cat.Description + "\n"
	}

	// Emit the quick reference grouped by the Categories order. Functions
	// generated into DevToolAPIFunctions are sorted alphabetically by
	// (category, name) — we iterate Categories to present them in the
	// hand-curated taxonomy order, then append any uncategorized entries.
	overview += "\n## Quick Reference\n\n"
	printedCat := make(map[string]bool)
	for _, cat := range DevToolAPIDocs.Categories {
		var hits []APIFunction
		for _, fn := range DevToolAPIDocs.Functions {
			if fn.Category == cat.Name {
				hits = append(hits, fn)
			}
		}
		if len(hits) == 0 {
			continue
		}
		overview += "\n### " + cat.Name + "\n"
		for _, fn := range hits {
			overview += "- `" + fn.Signature + "` - " + fn.Description + "\n"
		}
		printedCat[cat.Name] = true
	}
	// Any functions whose category isn't in the curated list still deserve
	// surfacing — don't silently drop them.
	var orphans []APIFunction
	for _, fn := range DevToolAPIDocs.Functions {
		if !printedCat[fn.Category] {
			orphans = append(orphans, fn)
		}
	}
	if len(orphans) > 0 {
		overview += "\n### other\n"
		for _, fn := range orphans {
			overview += "- `" + fn.Signature + "` - " + fn.Description + "\n"
		}
	}

	overview += "\n## Common Examples\n\n"
	overview += "```javascript\n"
	overview += "// Take a screenshot\n"
	overview += "await __devtool.screenshot(\"homepage\")\n\n"
	overview += "// Log a message\n"
	overview += "__devtool.log(\"User clicked\", \"info\", {target: \"button\"})\n\n"
	overview += "// Get last click with mouse trail\n"
	overview += "__devtool.interactions.getLastClickContext()\n\n"
	overview += "// Highlight recent DOM changes\n"
	overview += "__devtool.mutations.highlightRecent(5000)\n\n"
	overview += "// Inspect an element\n"
	overview += "__devtool.inspect(\"#submit-btn\")\n\n"
	overview += "// Run accessibility audit\n"
	overview += "__devtool.auditAccessibility()\n"
	overview += "```\n\n"

	overview += "Use `proxy {action: \"exec\", id: \"...\", describe: \"functionName\"}` for detailed function docs.\n"

	return overview
}

// GetFunctionDescription returns detailed documentation for a specific function.
func GetFunctionDescription(name string) (string, bool) {
	for _, fn := range DevToolAPIDocs.Functions {
		if fn.Name == name {
			doc := "# " + fn.Name + "\n\n"
			doc += fn.Description + "\n\n"
			doc += "**Category:** " + fn.Category + "\n\n"
			doc += "**Signature:**\n```javascript\n" + fn.Signature + "\n```\n\n"

			if len(fn.Parameters) > 0 {
				doc += "**Parameters:**\n"
				for _, p := range fn.Parameters {
					doc += "- " + p + "\n"
				}
				doc += "\n"
			}

			doc += "**Returns:** " + fn.Returns + "\n\n"
			doc += "**Example:**\n```javascript\n" + fn.Example + "\n```\n"

			return doc, true
		}
	}
	return "", false
}

// ListFunctionNames returns all function names for auto-completion.
func ListFunctionNames() []string {
	names := make([]string, len(DevToolAPIDocs.Functions))
	for i, fn := range DevToolAPIDocs.Functions {
		names[i] = fn.Name
	}
	return names
}
