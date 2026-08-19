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

// OpType is the discriminant of the closed op vocabulary (spec §6). The renderer
// is a switch over these and an unknown op is a rejection, never a passthrough.
//
// INV-6 RETIRED 2026-07-27: the vocabulary now also carries raw-content ops
// (§6a) holding publisher-authored CSS/HTML/script strings. INV-6 defended
// against author-supplied strings, but the author here is the publisher — a
// trusted actor holding the dev session (§0) — so it guarded the wrong boundary
// while making the visual variants publishing exists to demo inexpressible.
// Containment moved to CSP: publisher script runs only because its sha256 is
// pinned in the served revision's script-src (INV-11/INV-12), so upstream- and
// viewer-injected script still cannot execute.
type OpType string

const (
	OpSetText      OpType = "setText"
	OpSetAttribute OpType = "setAttribute"
	OpReplaceClass OpType = "replaceClass"
	OpAddClass     OpType = "addClass"
	OpRemoveClass  OpType = "removeClass"
	OpApplyStyle   OpType = "applyStyle"
	OpSetImageSrc  OpType = "setImageSrc"

	// Raw-content ops (§6a), admitted by INV-6's retirement.
	OpSetHTML   OpType = "setHTML"
	OpAddStyle  OpType = "addStyle"
	OpAddScript OpType = "addScript"
)

// Op is one DOM/CSS mutation (spec §6/§6a). It is a single struct rather than a
// set of subtypes so the vocabulary stays closed. Which fields are meaningful
// depends on Op; Validate enforces that only the fields legal for the op type
// are populated and that each is within the spec's surviving limits.
type Op struct {
	Op OpType `json:"op"`
	// Selector is required by every element-targeting op and forbidden on the
	// variant-root ops (addStyle, addScript).
	Selector string            `json:"selector,omitempty"`
	Value    string            `json:"value,omitempty"` // setText, addClass, removeClass, setAttribute
	Name     string            `json:"name,omitempty"`  // setAttribute
	From     string            `json:"from,omitempty"`  // replaceClass
	To       string            `json:"to,omitempty"`    // replaceClass
	URL      string            `json:"url,omitempty"`   // setImageSrc
	Props    map[string]string `json:"props,omitempty"` // applyStyle
	HTML     string            `json:"html,omitempty"`  // setHTML: raw fragment, assigned via innerHTML
	CSS      string            `json:"css,omitempty"`   // addStyle: raw CSS text for a <style> on the variant root
	Code     string            `json:"code,omitempty"`  // addScript: inline script body
	// Src is addScript's alternative body source and is a PUBLISH-TIME FETCH
	// INPUT, never a runtime attribute (§6a): the daemon fetches it once at
	// publish time under §4a/INV-13 and inlines the bytes, so no <script src>
	// ever reaches the served DOM and script-src is never widened to a host
	// source (INV-12). Src and Code are alternatives; supplying both is a
	// rejection. The fetch itself belongs to the publish pipeline, not to this
	// package — here Src is validated as an https URL only.
	Src string `json:"src,omitempty"`
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
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Target  string `json:"target,omitempty"`  // selector, validated by the selector grammar
	Gesture string `json:"gesture,omitempty"` // hover | click | scroll | drag: animated affordance near the highlight
	// GestureLabel names the concrete action for this step ("Click to open
	// your cart"), replacing the player's generic verb phrase. Requires
	// Gesture. Rendered as text only; never parsed as markup.
	GestureLabel string  `json:"gesture_label,omitempty"`
	Advance      Advance `json:"advance"`
}

// UpstreamConfig names the live third-party origin a published walkthrough is
// proxied against (§0, §4a). The origin is publisher-supplied and therefore
// hostile to us (INV-11/INV-12): the proxy replaces its CSP wholesale rather
// than merging with it.
//
// This package enforces only the scheme gate — https, bounded, control-char
// free — through the one existing ValidateURL. The §4a deny-list on the
// *resolved* address (loopback, RFC1918, link-local, cloud metadata), the
// resolve-pinned dial, and the per-hop redirect re-check (INV-13) need a
// resolver and a dialer and belong to the publish pipeline, not to a pure
// schema validator.
type UpstreamConfig struct {
	URL string `json:"url"`
}

// Validate enforces the https-only scheme gate. Nil-safe: an absent upstream is
// legal (a self-contained artifact names no live origin).
func (u *UpstreamConfig) Validate() error {
	if u == nil {
		return nil
	}
	if err := ValidateURL(u.URL); err != nil {
		return errf("upstream: %w", err)
	}
	return nil
}

// PublishedWalkthrough is the top-level published artifact: an ordered list of
// steps, an optional bound variant set, and the optional live upstream origin
// the steps are demonstrated against.
type PublishedWalkthrough struct {
	Version    SchemaVersion   `json:"version"`
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Upstream   *UpstreamConfig `json:"upstream,omitempty"`
	Steps      []Step          `json:"steps"`
	VariantSet *VariantSet     `json:"variantSet,omitempty"`
}

// validAdvanceTypes / validWaitWhen mirror the walkthrough script model.
var (
	validAdvanceTypes = map[string]bool{"auto": true, "click-target": true, "wait": true}
	validWaitWhen     = map[string]bool{"url-contains": true, "element-present": true, "element-visible": true}
	// validGestures is the closed set of animated affordances a step may
	// request next to its highlight. Empty means no affordance.
	validGestures = map[string]bool{"hover": true, "click": true, "scroll": true, "drag": true}
)

// errf is a tiny helper to keep validator error sites terse.
func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }
