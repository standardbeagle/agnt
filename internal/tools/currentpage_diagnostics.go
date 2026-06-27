package tools

import (
	"sort"
	"strings"
)

// FrameworkDiagnostic is a recognized framework runtime message lifted out of
// the raw error stream and annotated with the correct remediation direction and
// the common wrong fix to avoid.
//
// The report this serves: agents are "programming with a blindfold on" for
// front-end bugs because the decisive evidence lives in runtime state, not
// source — and they reliably pattern-match to plausible-but-wrong fixes (add a
// dependency to a deps array → infinite loop, suppress hydration warnings, bump
// z-index, add 'use client' everywhere). When a captured console message
// carries a known signature, we name the bug class and steer the fix.
type FrameworkDiagnostic struct {
	Category  string `json:"category"`            // e.g. "hydration-mismatch"
	Framework string `json:"framework,omitempty"` // react, next, vue, svelte, solid
	Count     int    `json:"count"`               // occurrences in this session
	Sample    string `json:"sample,omitempty"`    // the matched message (truncated)
	Fix       string `json:"fix"`                 // correct remediation direction
	Avoid     string `json:"avoid,omitempty"`     // the common wrong fix to steer away from
}

// diagSignature maps a lowercase substring to a diagnostic template. Order
// matters: more specific signatures must precede generic ones so a message is
// classified by its most precise match (e.g. a Vue prop-mutation warning is
// caught before the generic [vue warn] reactivity bucket).
type diagSignature struct {
	needle    string
	category  string
	framework string
	fix       string
	avoid     string
}

var diagSignatures = []diagSignature{
	// React — reactivity loss / lifecycle. These are runtime-only and the
	// report's named sweet spot (stale closures, re-render storms).
	{
		needle:    "maximum update depth exceeded",
		category:  "infinite-render-loop",
		framework: "react",
		fix:       "A state update is firing on every render. Find the setState called unconditionally in render or in an effect whose dependencies change each render; stabilize it (useCallback/useMemo for the dep, or guard the update with a condition).",
		avoid:     "Do not silence it by adding the value to the dependency array or wrapping the update in setTimeout/queueMicrotask — that hides the loop, not its cause.",
	},
	{
		needle:    "change in the order of hooks",
		category:  "hooks-order",
		framework: "react",
		fix:       "A hook is called conditionally (inside an if/loop/early-return). Move every hook to the top level so the call order is identical on every render.",
		avoid:     "Do not disable the rules-of-hooks lint rule — the order must actually be stable.",
	},
	{
		needle:    "rendered fewer hooks than expected",
		category:  "hooks-order",
		framework: "react",
		fix:       "An early return happens before some hooks run. Move all hooks above any conditional return.",
		avoid:     "Do not guard the hooks with conditions — guard the work inside them instead.",
	},
	{
		needle:    "uncontrolled input to be controlled",
		category:  "controlled-uncontrolled",
		framework: "react",
		fix:       "The input's value flips between undefined and a defined value. Initialize the state to '' (or the proper empty value) so value is always defined.",
		avoid:     "Do not remount with a key or switch to defaultValue — keep a single, always-defined controlled value.",
	},
	{
		needle:    "controlled input to be uncontrolled",
		category:  "controlled-uncontrolled",
		framework: "react",
		fix:       "The input's value became undefined after being defined. Keep value always defined (fall back to '').",
		avoid:     "Do not switch to defaultValue — keep the controlled value defined.",
	},
	{
		needle:    "unique \"key\" prop",
		category:  "missing-key",
		framework: "react",
		fix:       "List children need a stable key. Use a stable domain id (not the array index) so React preserves element identity across reorders/filters.",
		avoid:     "Do not use the array index as the key for a dynamic list — it bleeds state across items when the list changes.",
	},
	{
		needle:    "each child in a list should have a unique",
		category:  "missing-key",
		framework: "react",
		fix:       "List children need a stable key. Use a stable domain id (not the array index) so React preserves element identity across reorders/filters.",
		avoid:     "Do not use the array index as the key for a dynamic list — it bleeds state across items when the list changes.",
	},
	{
		needle:    "cannot update a component",
		category:  "cross-render-update",
		framework: "react",
		fix:       "A component sets another component's state during its own render. Move the update into an effect or an event handler.",
		avoid:     "Do not wrap the update in setTimeout to dodge the warning — schedule it in an effect instead.",
	},
	// Hydration / SSR divergence (React, Next, and generic). The report ranks
	// hydration mismatches second; the message text is shared across frameworks.
	{
		needle:    "hydration failed",
		category:  "hydration-mismatch",
		framework: "react",
		fix:       "Server and client rendered different output. Find non-deterministic render input (Date.now, Math.random, window/localStorage, locale/timezone) and defer it to the client (useEffect) or make it deterministic.",
		avoid:     "Do not silence with suppressHydrationWarning or sprinkle 'use client' to make the boundary go away — that hides the divergence instead of fixing it.",
	},
	{
		needle:    "did not match. server",
		category:  "hydration-mismatch",
		framework: "react",
		fix:       "Server and client rendered different text. Find non-deterministic render input (Date.now, Math.random, window/localStorage, locale/timezone) and defer it to the client (useEffect) or make it deterministic.",
		avoid:     "Do not silence with suppressHydrationWarning — that hides the divergence instead of fixing it.",
	},
	{
		needle:    "text content did not match",
		category:  "hydration-mismatch",
		framework: "react",
		fix:       "Server and client rendered different text. Find non-deterministic render input (Date.now, Math.random, window/localStorage, locale/timezone) and defer it to the client (useEffect) or make it deterministic.",
		avoid:     "Do not silence with suppressHydrationWarning — that hides the divergence instead of fixing it.",
	},
	{
		needle:    "an error occurred during hydration",
		category:  "hydration-mismatch",
		framework: "react",
		fix:       "Hydration threw and the tree was rebuilt on the client. Look for invalid HTML nesting or non-deterministic render output, and gate client-only rendering behind useEffect.",
		avoid:     "Do not silence with suppressHydrationWarning — that hides the divergence instead of fixing it.",
	},
	{
		needle:    "text content does not match server-rendered html",
		category:  "hydration-mismatch",
		framework: "react",
		fix:       "Server and client rendered different text. Find non-deterministic render input and defer it to the client (useEffect) or make it deterministic.",
		avoid:     "Do not silence with suppressHydrationWarning — that hides the divergence instead of fixing it.",
	},
	// Vue — reactivity loss. Emitted via console.warn, so only reachable once
	// the browser forwards signature-matched warnings (Part A).
	{
		needle:    "avoid mutating a prop directly",
		category:  "vue-prop-mutation",
		framework: "vue",
		fix:       "A child is mutating a prop. Emit an event to the parent (or use a local copy / v-model) instead of writing the prop.",
		avoid:     "Do not deep-clone the prop into reactive state just to silence it — model the data flow with an emit.",
	},
	{
		needle:    "target is readonly",
		category:  "vue-reactivity",
		framework: "vue",
		fix:       "Writing to a readonly reactive target. The value came from a readonly()/computed without a setter — write through the source ref or give the computed a setter.",
		avoid:     "Do not cast away readonly with toRaw — fix where the write should go.",
	},
	{
		needle:    "but is not defined on instance",
		category:  "vue-reactivity",
		framework: "vue",
		fix:       "A template references a property the component never declared/returned. Return it from setup() or declare it in data().",
		avoid:     "Do not add an optional-chain in the template to hide it — the binding source is missing.",
	},
	{
		needle:    "[vue warn]",
		category:  "vue-reactivity",
		framework: "vue",
		fix:       "Vue flagged a reactivity/lifecycle problem. Read the warning text for the offending ref or component and fix the data flow at its source.",
		avoid:     "Do not suppress Vue warnings globally — they mark real reactivity divergence.",
	},
	// Svelte / Solid — reactivity + hydration.
	{
		needle:    "was created with unknown prop",
		category:  "svelte-unknown-prop",
		framework: "svelte",
		fix:       "A prop is passed that the child never declares with `export let`. Declare it in the child or stop passing it.",
		avoid:     "Do not spread arbitrary props to silence it — declare the contract.",
	},
	{
		needle:    "computations created outside a `createroot`",
		category:  "solid-root-scope",
		framework: "solid",
		fix:       "A reactive computation was created without an owner. Wrap it in createRoot or run it inside a component/effect scope so it disposes correctly.",
		avoid:     "Do not ignore it — the computation leaks and never disposes.",
	},
}

const diagSampleMax = 200

// classifyDiagnostics scans captured errors for known framework runtime
// signatures and returns one entry per recognized category, highest-count
// first. Errors with no signature are left to the generic error rollup.
func classifyDiagnostics(errs []map[string]interface{}) []FrameworkDiagnostic {
	byCat := map[string]*FrameworkDiagnostic{}
	var order []string

	for _, e := range errs {
		hay := diagHaystack(e)
		if hay == "" {
			continue
		}
		sig := matchSignature(hay)
		if sig == nil {
			continue
		}
		if d, ok := byCat[sig.category]; ok {
			d.Count++
			continue
		}
		d := &FrameworkDiagnostic{
			Category:  sig.category,
			Framework: sig.framework,
			Count:     1,
			Sample:    truncate(firstNonEmpty(getString(e, "message"), getString(e, "error")), diagSampleMax),
			Fix:       sig.fix,
			Avoid:     sig.avoid,
		}
		byCat[sig.category] = d
		order = append(order, sig.category)
	}

	out := make([]FrameworkDiagnostic, 0, len(order))
	for _, c := range order {
		out = append(out, *byCat[c])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// diagHaystack builds the lowercase text matched against signatures, drawn from
// both the message and the raw error field (React's text lands in either).
func diagHaystack(e map[string]interface{}) string {
	msg := getString(e, "message")
	raw := getString(e, "error")
	if msg == "" && raw == "" {
		return ""
	}
	return strings.ToLower(msg + " " + raw)
}

// matchSignature returns the first signature whose needle is present in the
// haystack (table order = specificity order).
func matchSignature(hay string) *diagSignature {
	for i := range diagSignatures {
		if strings.Contains(hay, diagSignatures[i].needle) {
			return &diagSignatures[i]
		}
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
