package publish

import "testing"

// FuzzValidate feeds arbitrary bytes through the strict decode + validate path
// for both top-level artifacts. Contract: it must NEVER panic, and it must never
// return a decoded artifact together with a nil error unless that artifact is
// genuinely valid (re-validation is idempotent). Adversarial input is expected
// to be rejected, not to crash.
func FuzzValidate(f *testing.F) {
	seeds := []string{
		happyVariantSet(),
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":".x","value":"y"}]}]}`,
		`{}`, `[]`, `null`, `"x"`, `{"version":"v9"}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"color":"url(x)"}}]}]}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"javascript:1"}]}]}`,
		// §6a raw-content ops widened the accepted input space: these bodies are
		// now ACCEPTED (INV-6 retired), so the fuzzer must exercise the
		// re-validation / canonical / digest path on them, not just the reject
		// path. The mixed and malformed shapes below are the boundaries between
		// the two.
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setHTML","selector":".x","html":"<script>alert(1)</script>"}]}]}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addStyle","css":"@import url(https://x/y); .a{position:fixed}"}]}]}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addScript","code":"alert(1)"}]}]}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addScript","src":"https://cdn.example.com/a.js"}]}]}`,
		// src+code (a 422 per §6a), a selector where none is allowed, and a raw
		// op with an empty body — the shapes most likely to slip past a switch.
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addScript","src":"https://cdn.example.com/a.js","code":"alert(1)"}]}]}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addStyle","selector":".x","css":".a{}"}]}]}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setHTML","selector":".x","html":""}]}]}`,
		`{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setHTML","selector":".x","html":"<b>x</b>"},{"op":"addStyle","css":".a{color:red}"},{"op":"addScript","code":"x=1"}]}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if vs, err := DecodeVariantSet(data); err == nil {
			// A returned set must survive re-validation deterministically.
			if err2 := vs.Validate(); err2 != nil {
				t.Fatalf("accepted set failed re-validation: %v", err2)
			}
			// Canonical encode + digest of an accepted artifact must not panic.
			if _, err := CanonicalJSON(vs); err != nil {
				t.Fatalf("canonical failed on accepted set: %v", err)
			}
			if _, err := Digest(vs); err != nil {
				t.Fatalf("digest failed on accepted set: %v", err)
			}
		}
		// Walkthrough path must also never panic.
		if pw, err := DecodePublishedWalkthrough(data); err == nil {
			if err2 := pw.Validate(); err2 != nil {
				t.Fatalf("accepted walkthrough failed re-validation: %v", err2)
			}
		}
	})
}

// FuzzSelector fans random strings at the selector grammar validator: it must
// never panic and must be self-consistent.
func FuzzSelector(f *testing.F) {
	for _, s := range []string{"div", ".a > .b", "a:has(.x)", "a ~ b", "*", "[data-x=\"v\"]", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sel string) {
		_ = ValidateSelector(sel) // only assertion: no panic
	})
}

// FuzzCanonicalDeterministic asserts that any accepted set encodes identically
// twice (digest stability precondition).
func FuzzCanonicalDeterministic(f *testing.F) {
	f.Add([]byte(happyVariantSet()))
	f.Fuzz(func(t *testing.T, data []byte) {
		vs, err := DecodeVariantSet(data)
		if err != nil {
			return
		}
		a, err := CanonicalJSON(vs)
		if err != nil {
			t.Fatalf("canonical err: %v", err)
		}
		b, err := CanonicalJSON(vs)
		if err != nil {
			t.Fatalf("canonical err: %v", err)
		}
		if string(a) != string(b) {
			t.Fatalf("nondeterministic canonical output")
		}
	})
}
