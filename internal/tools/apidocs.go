package tools

//go:generate go run ../../scripts/gen-apidocs.go -scripts ../proxy/scripts -out apidocs_gen.go

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
		{Name: "layout", Description: "Diagnose layout issues (overflow, stacking, offscreen)"},
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
