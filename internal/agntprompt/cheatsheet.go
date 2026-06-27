// Package agntprompt builds the agnt system-prompt fragments that are
// injected into AI coding agents. It is deliberately decoupled from the
// agentadapter package: adapters receive the final prompt string and do
// not know which fragments (base prompt, cheat sheet, runtime state)
// went into it.
//
// The cheat sheet is a compact, hand-curated view of the
// [internal/tools].DevToolAPIDocs catalog. It lists ~15 high-value
// helpers grouped by category so agents reach for `__devtool.*` before
// falling back to raw `document.*` / `window.*` / `getBoundingClientRect`
// calls when writing `proxy exec` snippets.
package agntprompt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/standardbeagle/agnt/internal/tools"
)

// PromotedFunctions is the hand-curated list of `__devtool.*` helper
// names that should appear in the cheat sheet, in the order they are
// grouped by category below.
//
// Rationale for the hardcoded-slice route (rather than a @promote JSDoc
// tag on the JS source): the list is small (~15), stable (driven by
// the category taxonomy, not the generated catalog), and the curation
// decision is prompt-authoring, not API-authoring. Co-locating it with
// the prompt builder keeps the tuning cost one-file instead of
// fifteen JSDoc edits + a regenerate cycle.
//
// Every name in this slice MUST exist in tools.DevToolAPIFunctions;
// BuildCheatSheet skips unknown names silently so the cheat sheet
// degrades gracefully if a helper is renamed or removed in JSDoc, but
// the drift is caught by TestPromotedFunctionsExist.
var PromotedFunctions = []string{
	// logging — how agents report what they saw back to the proxy log.
	"log",
	"screenshot",
	// inspection — cheap reads that replace raw document.querySelector.
	"inspect",
	"getElementInfo",
	"getComputed",
	// CSS layering/positioning — the runtime-only causes (stacking root,
	// fixed-element traps) that source-only reasoning gets wrong.
	"getStacking",
	"getContainer",
	// layout — overflow/stacking/offscreen diagnosis in one call.
	"findOverflows",
	"findStackingContexts",
	"diagnoseLayout",
	// accessibility — a11y checks with structured output.
	"auditAccessibility",
	"getA11yInfo",
	"getContrast",
	// audit — page-wide quality signals.
	"auditPageQuality",
	"auditDOMComplexity",
	// interactions — replay what the user just did.
	"interactions.getLastClickContext",
	// mutations — see recent DOM changes visually.
	"mutations.highlightRecent",
}

// cheatSheetHeader is the fixed rules block that precedes the helper
// list. Kept verbatim so the drift test can pin it.
const cheatSheetHeader = `## Browser debugging helpers
Prefer __devtool.* helpers over raw document.*/window.*/getBoundingClientRect.
Call ` + "`proxy exec search: X`" + ` before writing raw JS.
Use ` + "`proxy exec describe: name`" + ` for full signature + example.
Symptom→helper (the decisive evidence is runtime state, not the source):
- z-index/layering wrong → __devtool.getStacking (stackingRoot + rootTrigger), not a bigger z-index.
- position:fixed scrolls/mispositions → __devtool.getContainer (trappedBy ancestor).
- element clipped/hidden → __devtool.findOverflows / isVisible.
`

// BuildCheatSheet renders the compact helper cheat sheet. Callers pass
// the same apiFunctions slice that is exposed via
// tools.DevToolAPIFunctions — BuildCheatSheet indexes it by Name and
// emits one line per promoted function, grouped by category in the
// order the functions appear in PromotedFunctions. Unknown names
// (catalog drift) are skipped.
//
// Returned string always ends with a trailing newline and is safe to
// concatenate onto an existing system prompt.
func BuildCheatSheet(apiFunctions []tools.APIFunction) string {
	index := make(map[string]tools.APIFunction, len(apiFunctions))
	for _, fn := range apiFunctions {
		index[fn.Name] = fn
	}

	// Group promoted names by category, preserving first-seen order
	// per category so the output is deterministic.
	type group struct {
		category string
		fns      []tools.APIFunction
	}
	var groups []group
	catIndex := map[string]int{}
	for _, name := range PromotedFunctions {
		fn, ok := index[name]
		if !ok {
			continue
		}
		idx, seen := catIndex[fn.Category]
		if !seen {
			catIndex[fn.Category] = len(groups)
			groups = append(groups, group{category: fn.Category, fns: []tools.APIFunction{fn}})
			continue
		}
		groups[idx].fns = append(groups[idx].fns, fn)
	}

	// Stable output: groups keep insertion order, which mirrors the
	// PromotedFunctions list. Within a group, keep list order too.
	var sb strings.Builder
	sb.WriteString(cheatSheetHeader)
	sb.WriteString("\n")
	for _, g := range groups {
		sb.WriteString("### ")
		sb.WriteString(g.category)
		sb.WriteString("\n")
		for _, fn := range g.fns {
			fmt.Fprintf(&sb, "- `%s` — %s\n", fn.Signature, fn.Description)
		}
	}
	return sb.String()
}

// PromotedCategories returns the set of categories referenced by
// PromotedFunctions, sorted alphabetically. Used only by tests to
// validate coverage of the curated taxonomy.
func PromotedCategories(apiFunctions []tools.APIFunction) []string {
	index := make(map[string]tools.APIFunction, len(apiFunctions))
	for _, fn := range apiFunctions {
		index[fn.Name] = fn
	}
	seen := map[string]struct{}{}
	for _, name := range PromotedFunctions {
		if fn, ok := index[name]; ok {
			seen[fn.Category] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
