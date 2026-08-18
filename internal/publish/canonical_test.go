package publish

import (
	"bytes"
	"reflect"
	"testing"
)

func sampleWalkthrough() PublishedWalkthrough {
	return PublishedWalkthrough{
		Version:  SchemaV1,
		ID:       "wt-1",
		Title:    "Demo <b> & co",
		Upstream: &UpstreamConfig{URL: "https://demo.example.com/app"},
		Steps: []Step{
			{ID: "s1", Title: "One", Body: "first", Target: ".title", Advance: Advance{Type: "auto", MS: 4000}},
			{ID: "s2", Title: "Two", Body: "second", Advance: Advance{Type: "wait", When: "url-contains", Value: "/done"}},
		},
		VariantSet: &VariantSet{
			Version: SchemaV1, ID: "vs-1", StepID: "s1",
			Variants: []Variant{
				{ID: "a", Ops: []Op{
					{Op: OpSetText, Selector: ".title", Value: "Hi"},
					// §6a raw-content ops must ride the canonical encoding too.
					// addScript is deliberately ABSENT: since the INV-14 operator
					// decision (task 01M09KYHZ0CFAX2NVGMAQJ1WFW) a published
					// walkthrough is refused if it carries any addScript op, so it can
					// never appear in a stored/digested revision — a decode-valid
					// fixture must not include one. The other raw-content ops still
					// exercise the encoding.
					{Op: OpSetHTML, Selector: ".body", HTML: "<b>hi</b> & <i>bye</i>"},
					{Op: OpAddStyle, CSS: ".title{color:red}"},
					{Op: OpApplyStyle, Selector: "#box", Props: map[string]string{
						"color": "red", "margin": "8px", "position": "fixed", "z-index": "9",
					}},
				}},
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

// TestDigestCoversNewFields pins that the fields this slice added are inside the
// digest, not beside it. A field the canonical encoding skips would let two
// materially different revisions — different upstream origin, different injected
// script — share a content digest, and §3a keys a stored revision (and the
// script-src hash set computed from it) by exactly that digest.
func TestDigestCoversNewFields(t *testing.T) {
	base, err := Digest(sampleWalkthrough())
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(w *PublishedWalkthrough){
		"upstream-url":    func(w *PublishedWalkthrough) { w.Upstream.URL = "https://other.example.com/app" },
		"upstream-absent": func(w *PublishedWalkthrough) { w.Upstream = nil },
		"raw-html":        func(w *PublishedWalkthrough) { w.VariantSet.Variants[0].Ops[1].HTML = "<b>other</b>" },
		"raw-css":         func(w *PublishedWalkthrough) { w.VariantSet.Variants[0].Ops[2].CSS = ".title{color:blue}" },
		"applied-style":   func(w *PublishedWalkthrough) { w.VariantSet.Variants[0].Ops[3].Props["color"] = "blue" },
		// addScript Code/Src are no longer digest-covered here: a published
		// walkthrough carrying addScript is refused (INV-14, task
		// 01M09KYHZ0CFAX2NVGMAQJ1WFW), so no stored/digested revision can hold one.
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			w := sampleWalkthrough()
			mutate(&w)
			got, err := Digest(w)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("digest must change when %s changes", name)
			}
		})
	}
}

// TestCanonicalNilUpstreamMatchesAbsent is the trap a new optional pointer field
// sets for a content digest: if a nil Upstream encoded as `"upstream":null` while
// an artifact that never carried one encoded as absent, two identical revisions
// would hash differently and §3a's (filename, content-digest) key would split.
// omitempty makes the two encodings the same bytes; this test is what keeps it
// that way.
func TestCanonicalNilUpstreamMatchesAbsent(t *testing.T) {
	nilUpstream := sampleWalkthrough()
	nilUpstream.Upstream = nil

	enc, err := CanonicalJSON(nilUpstream)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte(`"upstream"`)) {
		t.Fatalf("a nil upstream must not appear in the canonical encoding: %s", enc)
	}
	// Decoding those bytes yields an artifact whose upstream key was never
	// present; it must be byte-identical and digest-identical.
	dec, err := DecodePublishedWalkthrough(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Upstream != nil {
		t.Fatal("absent upstream must decode to nil, not a zero-value struct")
	}
	reenc, err := CanonicalJSON(*dec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, reenc) {
		t.Fatalf("nil upstream and absent upstream must encode identically:\n%s\n%s", enc, reenc)
	}
	d1, _ := Digest(nilUpstream)
	d2, _ := Digest(*dec)
	if d1 != d2 {
		t.Fatalf("nil vs absent upstream digest mismatch: %s != %s", d1, d2)
	}
}

// TestCanonicalStableAcrossMapIteration is the other digest trap: Go randomizes
// map iteration order per range, so a canonical encoder that emitted an
// applyStyle props map in iteration order would produce a different digest on
// every run for the same artifact. The sample carries a four-key props map;
// repeating the encode must be byte-identical every time.
func TestCanonicalStableAcrossMapIteration(t *testing.T) {
	w := sampleWalkthrough()
	first, err := CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		got, err := CanonicalJSON(w)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("map iteration order leaked into the canonical encoding on run %d:\n%s\n%s", i, first, got)
		}
	}
}
