package publish

import (
	"bytes"
	"reflect"
	"testing"
)

func sampleWalkthrough() PublishedWalkthrough {
	return PublishedWalkthrough{
		Version: SchemaV1,
		ID:      "wt-1",
		Title:   "Demo <b> & co",
		Steps: []Step{
			{ID: "s1", Title: "One", Body: "first", Target: ".title", Advance: Advance{Type: "auto", MS: 4000}},
			{ID: "s2", Title: "Two", Body: "second", Advance: Advance{Type: "wait", When: "url-contains", Value: "/done"}},
		},
		VariantSet: &VariantSet{
			Version: SchemaV1, ID: "vs-1", StepID: "s1",
			Variants: []Variant{
				{ID: "a", Ops: []Op{{Op: OpSetText, Selector: ".title", Value: "Hi"}}},
			},
		},
	}
}

func TestCanonicalDeterministic(t *testing.T) {
	w := sampleWalkthrough()
	a, err := CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical encoding not deterministic:\n%s\n%s", a, b)
	}
}

func TestCanonicalSortsKeys(t *testing.T) {
	// Object keys must be emitted in sorted order regardless of struct/map order.
	m := map[string]any{"z": 1, "a": 2, "m": 3}
	got, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":2,"m":3,"z":1}`
	if string(got) != want {
		t.Fatalf("want %s got %s", want, got)
	}
}

func TestCanonicalNoHTMLEscape(t *testing.T) {
	got, err := CanonicalJSON("a<b>&c")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"a<b>&c"` {
		t.Fatalf("html should not be escaped, got %s", got)
	}
}

func TestRoundTripIdentity(t *testing.T) {
	w := sampleWalkthrough()
	enc, err := CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodePublishedWalkthrough(enc)
	if err != nil {
		t.Fatalf("decode(encode(x)) failed: %v", err)
	}
	if !reflect.DeepEqual(w, *dec) {
		t.Fatalf("round-trip mismatch:\nwant %#v\ngot  %#v", w, *dec)
	}
}

func TestDigestStableAcrossEncodeDecode(t *testing.T) {
	w := sampleWalkthrough()
	d1, err := Digest(w)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodePublishedWalkthrough(enc)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := Digest(*dec)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("digest changed across encode/decode cycle: %s != %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("expected 64-hex-char sha256, got %d chars", len(d1))
	}
}

func TestDigestSensitiveToChange(t *testing.T) {
	a := sampleWalkthrough()
	b := sampleWalkthrough()
	b.Steps[0].Title = "changed"
	da, _ := Digest(a)
	db, _ := Digest(b)
	if da == db {
		t.Fatal("digest must differ when content differs")
	}
}
