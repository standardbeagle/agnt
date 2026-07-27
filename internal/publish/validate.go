package publish

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// idRe validates a stable identifier: URL/JSON-safe, non-empty.
var idRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validateID(kind, id string) error {
	if id == "" {
		return errf("%s: id is required", kind)
	}
	if len(id) > MaxIDLength {
		return errf("%s: id length %d exceeds max %d", kind, len(id), MaxIDLength)
	}
	if !idRe.MatchString(id) {
		return errf("%s: id %q has illegal characters", kind, id)
	}
	return nil
}

// strictDecode decodes data into *T rejecting unknown JSON fields and trailing
// data. Unknown-field rejection (INV: "reject unknown fields") applies
// recursively to every struct in the tree.
func strictDecode[T any](data []byte) (*T, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		return nil, errf("decode: %w", err)
	}
	if dec.More() {
		return nil, errf("decode: unexpected trailing data")
	}
	return &v, nil
}

// DecodeVariantSet strictly decodes and fully validates a variant set.
func DecodeVariantSet(data []byte) (*VariantSet, error) {
	vs, err := strictDecode[VariantSet](data)
	if err != nil {
		return nil, err
	}
	if err := vs.Validate(); err != nil {
		return nil, err
	}
	return vs, nil
}

// DecodePublishedWalkthrough strictly decodes and fully validates a published
// walkthrough (including any bound variant set).
func DecodePublishedWalkthrough(data []byte) (*PublishedWalkthrough, error) {
	pw, err := strictDecode[PublishedWalkthrough](data)
	if err != nil {
		return nil, err
	}
	if err := pw.Validate(); err != nil {
		return nil, err
	}
	return pw, nil
}

// Validate checks a variant against the op vocabulary + duplicate-op rule.
func (v *Variant) Validate() error {
	if err := validateID("variant", v.ID); err != nil {
		return err
	}
	if len(v.Label) > MaxTitleLength {
		return errf("variant %q: label too long", v.ID)
	}
	if !noControlChars(v.Label) {
		return errf("variant %q: label has control character", v.ID)
	}
	seen := map[string]bool{}
	styleBytes := 0
	for i := range v.Ops {
		op := &v.Ops[i]
		if err := op.Validate(); err != nil {
			return errf("variant %q op %d: %w", v.ID, i, err)
		}
		sig := op.signature()
		if seen[sig] {
			return errf("variant %q: duplicate op %q", v.ID, sig)
		}
		seen[sig] = true
		styleBytes += op.styleBytes()
	}
	// §5's style-patch cap is per VARIANT, not per op: applyStyle and addStyle
	// both spend from it, so a variant cannot dodge the bound by splitting one
	// oversize patch across several ops.
	if styleBytes > MaxStylePatchBytes {
		return errf("variant %q: style patch %d bytes exceeds max %d", v.ID, styleBytes, MaxStylePatchBytes)
	}
	return nil
}

// Validate checks a variant set: supported version, stable/unique ids, size cap.
func (vs *VariantSet) Validate() error {
	if !isSupportedVersion(vs.Version) {
		return errf("variant set: unsupported version %q", vs.Version)
	}
	if err := validateID("variant set", vs.ID); err != nil {
		return err
	}
	if vs.StepID != "" {
		if err := validateID("variant set stepId", vs.StepID); err != nil {
			return err
		}
	}
	if len(vs.Variants) == 0 {
		return errf("variant set %q: at least one variant required", vs.ID)
	}
	if len(vs.Variants) > MaxVariantsPerSet {
		return errf("variant set %q: %d variants exceeds max %d", vs.ID, len(vs.Variants), MaxVariantsPerSet)
	}
	seen := map[string]bool{}
	scriptBytes := 0
	for i := range vs.Variants {
		v := &vs.Variants[i]
		if seen[v.ID] {
			return errf("variant set %q: duplicate variant id %q", vs.ID, v.ID)
		}
		seen[v.ID] = true
		if err := v.Validate(); err != nil {
			return err
		}
		for j := range v.Ops {
			scriptBytes += len(v.Ops[j].Code)
		}
	}
	// §5's raw-script cap is per authored REVISION, not per op — the variant set
	// is the revision's script-bearing content, and this bound is also what keeps
	// the script-src hash set INV-12 pins from growing without limit. Bodies
	// fetched from an addScript src count against the same budget once inlined,
	// which happens in the publish pipeline, not here.
	if scriptBytes > MaxRawScriptBytes {
		return errf("variant set %q: raw script %d bytes exceeds max %d per revision", vs.ID, scriptBytes, MaxRawScriptBytes)
	}
	return nil
}

// Validate checks an advance directive against the walkthrough model.
func (a *Advance) Validate() error {
	if !validAdvanceTypes[a.Type] {
		return errf("advance: unknown type %q", a.Type)
	}
	if a.MS < 0 {
		return errf("advance: negative ms")
	}
	switch a.Type {
	case "wait":
		if !validWaitWhen[a.When] {
			return errf("advance: wait requires when in {url-contains,element-present,element-visible}")
		}
		if a.When == "url-contains" {
			if a.Value == "" || len(a.Value) > MaxURLLength {
				return errf("advance: wait url-contains value length invalid")
			}
		} else if a.Value != "" {
			if err := ValidateSelector(a.Value); err != nil {
				return errf("advance: wait value must be a valid selector: %w", err)
			}
		}
	default:
		if a.When != "" || a.Value != "" {
			return errf("advance: when/value only valid for wait")
		}
	}
	return nil
}

// Validate checks a step: stable id, bounded text, valid target selector +
// advance.
func (s *Step) Validate() error {
	if err := validateID("step", s.ID); err != nil {
		return err
	}
	if s.Title == "" || len(s.Title) > MaxTitleLength {
		return errf("step %q: title length invalid", s.ID)
	}
	if len(s.Body) > MaxBodyLength {
		return errf("step %q: body too long", s.ID)
	}
	if !noControlChars(s.Title) {
		return errf("step %q: title has control character", s.ID)
	}
	if s.Target != "" {
		if err := ValidateSelector(s.Target); err != nil {
			return errf("step %q: %w", s.ID, err)
		}
	}
	if s.Gesture != "" && !validGestures[s.Gesture] {
		return errf("step %q: unknown gesture %q (hover|click|scroll|drag)", s.ID, s.Gesture)
	}
	if s.Gesture != "" && s.Target == "" {
		return errf("step %q: gesture requires a target", s.ID)
	}
	if s.GestureLabel != "" {
		if s.Gesture == "" {
			return errf("step %q: gesture_label requires a gesture", s.ID)
		}
		if len(s.GestureLabel) > MaxGestureLabelLength {
			return errf("step %q: gesture_label exceeds %d bytes", s.ID, MaxGestureLabelLength)
		}
		if !noControlChars(s.GestureLabel) {
			return errf("step %q: gesture_label has control character", s.ID)
		}
	}
	return s.Advance.Validate()
}

// Validate checks a published walkthrough end-to-end, including the bound
// variant set and its step binding.
func (pw *PublishedWalkthrough) Validate() error {
	if !isSupportedVersion(pw.Version) {
		return errf("walkthrough: unsupported version %q", pw.Version)
	}
	if err := validateID("walkthrough", pw.ID); err != nil {
		return err
	}
	if pw.Title == "" || len(pw.Title) > MaxTitleLength {
		return errf("walkthrough %q: title length invalid", pw.ID)
	}
	// Nil-safe: an artifact with no live origin is legal.
	if err := pw.Upstream.Validate(); err != nil {
		return errf("walkthrough %q: %w", pw.ID, err)
	}
	if len(pw.Steps) == 0 {
		return errf("walkthrough %q: at least one step required", pw.ID)
	}
	if len(pw.Steps) > MaxStepsPerWalkthrough {
		return errf("walkthrough %q: %d steps exceeds max %d", pw.ID, len(pw.Steps), MaxStepsPerWalkthrough)
	}
	seen := map[string]bool{}
	for i := range pw.Steps {
		s := &pw.Steps[i]
		if seen[s.ID] {
			return errf("walkthrough %q: duplicate step id %q", pw.ID, s.ID)
		}
		seen[s.ID] = true
		if err := s.Validate(); err != nil {
			return err
		}
	}
	if pw.VariantSet != nil {
		if err := pw.VariantSet.Validate(); err != nil {
			return err
		}
		if sid := pw.VariantSet.StepID; sid != "" && !seen[sid] {
			return errf("walkthrough %q: variant set bound to unknown step %q", pw.ID, sid)
		}
	}
	return nil
}
