package publish

// defaultAutoMS mirrors the browser-side walkthrough default (advance auto shows
// for 5000ms before advancing; see internal/tools/walkthrough_tools.go doc:
// "auto {ms} show ms then advance (default 5000)"). The normalization the live
// walkthrough performs lives browser-side in JS; there is no Go-side normalizer
// to import, so this is a faithful pure-Go port of that defaulting.
const defaultAutoMS = 5000

// NormalizeStep returns a copy of s with defaults filled in the same way the
// live walkthrough player does: a missing advance type becomes "auto", and an
// auto advance with ms<=0 gets the default dwell. It is pure (does not mutate s).
func NormalizeStep(s Step) Step {
	out := s
	if out.Advance.Type == "" {
		out.Advance.Type = "auto"
	}
	if out.Advance.Type == "auto" && out.Advance.MS <= 0 {
		out.Advance.MS = defaultAutoMS
	}
	return out
}

// NormalizeWalkthrough returns a copy of pw with every step normalized. Pure:
// the input slice is not mutated (a fresh steps slice is allocated).
func NormalizeWalkthrough(pw PublishedWalkthrough) PublishedWalkthrough {
	out := pw
	out.Steps = make([]Step, len(pw.Steps))
	for i, s := range pw.Steps {
		out.Steps[i] = NormalizeStep(s)
	}
	return out
}

// BindVariantSet binds vs to the step identified by stepID within pw. It
// validates that the step exists, that vs is itself valid, records the binding
// on the (copied) set's StepID, and attaches it to pw. Returns an error rather
// than binding to a nonexistent step (fail fast, no silent no-op).
func BindVariantSet(pw *PublishedWalkthrough, stepID string, vs *VariantSet) error {
	found := false
	for i := range pw.Steps {
		if pw.Steps[i].ID == stepID {
			found = true
			break
		}
	}
	if !found {
		return errf("bind: no step with id %q", stepID)
	}
	bound := *vs
	bound.StepID = stepID
	if err := bound.Validate(); err != nil {
		return err
	}
	pw.VariantSet = &bound
	return nil
}
