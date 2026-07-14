// Package publish defines the versioned, security-hardened schemas and
// validators for the public walkthrough-publish artifacts (P2 of the
// walkthrough-publish epic). It encodes the numbers, grammar, and allowlists of
// the P1 security spec
// (docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md) as
// the single source of truth: a Variant/VariantSet/PublishedWalkthrough with
// stable ids, strict deny-by-default validators, a closed declarative op
// vocabulary (no arbitrary HTML/JS representable in the type), and a
// deterministic canonical JSON encoder + sha256 digest.
package publish

import "fmt"

// SchemaVersion identifies the wire schema of a published artifact. It is an
// explicit field on every top-level artifact so that unsupported versions are
// rejected rather than silently misinterpreted.
type SchemaVersion string

// SchemaV1 is the initial published-artifact schema.
const SchemaV1 SchemaVersion = "v1"

// supportedVersions is the closed set of versions this build understands.
// Anything else is rejected by the validators.
var supportedVersions = map[SchemaVersion]bool{
	SchemaV1: true,
}

// isSupportedVersion reports whether v is a version this build can serve.
func isSupportedVersion(v SchemaVersion) bool { return supportedVersions[v] }

// OpType is the discriminant of the closed declarative op vocabulary (spec §6).
// The renderer is a switch over these; there is deliberately NO op type that
// carries a code or markup string (INV-6).
type OpType string

const (
	OpSetText      OpType = "setText"
	OpSetAttribute OpType = "setAttribute"
	OpReplaceClass OpType = "replaceClass"
	OpAddClass     OpType = "addClass"
	OpRemoveClass  OpType = "removeClass"
	OpApplyStyle   OpType = "applyStyle"
	OpSetImageSrc  OpType = "setImageSrc"
)

// Op is one declarative DOM/CSS mutation (spec §6). It is a single struct rather
// than a set of subtypes so the type is closed: every field is a plain
// string/string-map — none can hold arbitrary HTML or JS. Which fields are
// meaningful depends on Op; Validate enforces that only the fields legal for the
// op type are populated and that each is within the spec's allowlists/limits.
type Op struct {
	Op       OpType            `json:"op"`
	Selector string            `json:"selector"`
	Value    string            `json:"value,omitempty"` // setText, addClass, removeClass, setAttribute
	Name     string            `json:"name,omitempty"`  // setAttribute
	From     string            `json:"from,omitempty"`  // replaceClass
	To       string            `json:"to,omitempty"`    // replaceClass
	URL      string            `json:"url,omitempty"`   // setImageSrc
	Props    map[string]string `json:"props,omitempty"` // applyStyle
}

// Variant is one alternative rendering the cycler can switch to. Ops are applied
// declaratively to the proxied page.
type Variant struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Ops   []Op   `json:"ops"`
}

// VariantSet is a versioned, immutable snapshot of the variants for a published
// walkthrough. StepID, when set, records the step this set is bound to
// (see BindVariantSet).
type VariantSet struct {
	Version  SchemaVersion `json:"version"`
	ID       string        `json:"id"`
	StepID   string        `json:"stepId,omitempty"`
	Variants []Variant     `json:"variants"`
}

// Advance describes how a walkthrough step advances. Mirrors the browser-side
// walkthrough script model (internal/tools/walkthrough_tools.go): auto {ms},
// click-target, or wait {when,value}.
type Advance struct {
	Type  string `json:"type"`           // auto | click-target | wait
	MS    int    `json:"ms,omitempty"`   // auto
	When  string `json:"when,omitempty"` // wait: url-contains|element-present|element-visible
	Value string `json:"value,omitempty"`
}

// Step is one narration step with an optional highlight target selector.
type Step struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Body    string  `json:"body"`
	Target  string  `json:"target,omitempty"` // selector, validated by the selector grammar
	Advance Advance `json:"advance"`
}

// PublishedWalkthrough is the top-level published artifact: an ordered list of
// steps plus an optional bound variant set.
type PublishedWalkthrough struct {
	Version    SchemaVersion `json:"version"`
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Steps      []Step        `json:"steps"`
	VariantSet *VariantSet   `json:"variantSet,omitempty"`
}

// validAdvanceTypes / validWaitWhen mirror the walkthrough script model.
var (
	validAdvanceTypes = map[string]bool{"auto": true, "click-target": true, "wait": true}
	validWaitWhen     = map[string]bool{"url-contains": true, "element-present": true, "element-visible": true}
)

// errf is a tiny helper to keep validator error sites terse.
func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }
