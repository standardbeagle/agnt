package publish

// Concrete limits from the P1 security spec
// (docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md §5).
// P2's validators encode these verbatim; anything exceeding a limit is rejected
// (never silently truncated).
const (
	// MaxVariantsPerSet caps a variant set (§5: "Max variants per set = 12").
	MaxVariantsPerSet = 12
	// MaxStepsPerWalkthrough caps a walkthrough (§5: "Max steps per walkthrough = 50").
	MaxStepsPerWalkthrough = 50
	// MaxSelectorLength caps a single selector string (§5: "Max selector length = 256").
	MaxSelectorLength = 256
	// MaxSelectorDepth caps the number of compound selectors joined by
	// combinators (§5a grammar annotation "# max depth 6"). The spec annotates
	// the repetition as "max depth 6"; it is ambiguous whether that counts
	// compounds or combinators — we choose the stricter reading (at most 6
	// compound selectors) as befits a deny-by-default security allowlist.
	MaxSelectorDepth = 6
	// MaxStylePatchBytes caps one variant's applyStyle payload (§5: "Max
	// style-patch size = 4096 bytes (per variant)").
	MaxStylePatchBytes = 4096
	// MaxTextBytes caps a setText value in UTF-8 bytes (§5: "Max text length
	// (setText) = 2048 bytes UTF-8"; §6 setText guard "value ≤2048 B").
	MaxTextBytes = 2048
	// MaxURLLength caps any op-supplied URL (§5: "Max URL length = 2048 chars").
	MaxURLLength = 2048
	// MaxFeedbackBytes caps a feedback body (§5: "Max feedback body = 4096
	// bytes"). Declared here for completeness; feedback is owned by P8.
	MaxFeedbackBytes = 4096

	// MaxIDLength / MaxTitleLength / MaxBodyLength bound stable identifiers and
	// narration text. The spec fixes text/selector/url limits but is silent on
	// id/title/body; we pick conservative defaults so no field is unbounded
	// (spec §5 principle: "Reject anything exceeding a limit; never truncate").
	MaxIDLength    = 128
	MaxTitleLength = 256
	MaxBodyLength  = 4096
	// MaxGestureLabelLength bounds a step's gesture_label. The label renders as
	// a single white-space:nowrap pill under the affordance, so longer text
	// runs off the viewport instead of reading as an instruction. Mirrored
	// client-side by MAX_GESTURE_LABEL in walkthrough.js and
	// walkthrough-viewer.js — keep the three in lockstep.
	MaxGestureLabelLength = 64
	// MaxAttrValueLength bounds a setAttribute value; reuses the text cap.
	MaxAttrValueLength = MaxTextBytes
)
