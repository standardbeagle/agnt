package publish

import "testing"

func TestNormalizeStepDefaults(t *testing.T) {
	got := NormalizeStep(Step{ID: "s1", Title: "t"})
	if got.Advance.Type != "auto" {
		t.Fatalf("expected default advance type auto, got %q", got.Advance.Type)
	}
	if got.Advance.MS != defaultAutoMS {
		t.Fatalf("expected default ms %d, got %d", defaultAutoMS, got.Advance.MS)
	}
}

func TestNormalizeStepIsPure(t *testing.T) {
	in := Step{ID: "s1", Title: "t"}
	_ = NormalizeStep(in)
	if in.Advance.Type != "" {
		t.Fatal("NormalizeStep mutated its input")
	}
}

func TestNormalizeWalkthroughDoesNotMutateInput(t *testing.T) {
	in := PublishedWalkthrough{
		Version: SchemaV1, ID: "w", Title: "t",
		Steps: []Step{{ID: "s1", Title: "a"}},
	}
	out := NormalizeWalkthrough(in)
	if in.Steps[0].Advance.Type != "" {
		t.Fatal("NormalizeWalkthrough mutated input steps")
	}
	if out.Steps[0].Advance.Type != "auto" {
		t.Fatal("output step not normalized")
	}
}

func TestBindVariantSet(t *testing.T) {
	pw := &PublishedWalkthrough{
		Version: SchemaV1, ID: "w", Title: "t",
		Steps: []Step{{ID: "s1", Title: "a", Advance: Advance{Type: "auto", MS: 1000}}},
	}
	vs := &VariantSet{
		Version: SchemaV1, ID: "vs",
		Variants: []Variant{{ID: "a", Ops: []Op{{Op: OpSetText, Selector: ".x", Value: "hi"}}}},
	}
	if err := BindVariantSet(pw, "s1", vs); err != nil {
		t.Fatalf("bind to existing step must succeed: %v", err)
	}
	if pw.VariantSet == nil || pw.VariantSet.StepID != "s1" {
		t.Fatal("variant set not bound to step s1")
	}
	if vs.StepID != "" {
		t.Fatal("BindVariantSet must not mutate the caller's variant set")
	}
}

func TestBindVariantSetUnknownStep(t *testing.T) {
	pw := &PublishedWalkthrough{
		Version: SchemaV1, ID: "w", Title: "t",
		Steps: []Step{{ID: "s1", Title: "a", Advance: Advance{Type: "auto", MS: 1000}}},
	}
	vs := &VariantSet{
		Version: SchemaV1, ID: "vs",
		Variants: []Variant{{ID: "a", Ops: []Op{{Op: OpSetText, Selector: ".x", Value: "hi"}}}},
	}
	if err := BindVariantSet(pw, "nope", vs); err == nil {
		t.Fatal("binding to a nonexistent step must fail")
	}
}
