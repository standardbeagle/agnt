package incident

import (
	"strings"
	"testing"
)

func TestFingerprint_CanonicalizesTimestamps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"iso8601_T", "Error at 2024-01-15T10:30:00Z"},
		{"iso8601_space", "Error at 2024-01-15 10:30:00.123"},
		{"iso8601_offset", "Failed 2024-03-01T08:00:00+05:30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Canonicalize(tc.input)
			if strings.Contains(got, "2024") {
				t.Errorf("timestamp not stripped: %q", got)
			}
			if !strings.Contains(got, "TIMESTAMP") {
				t.Errorf("TIMESTAMP placeholder missing: %q", got)
			}
		})
	}
}

func TestFingerprint_CanonicalizesAddresses(t *testing.T) {
	t.Parallel()
	input := "panic: runtime error at 0x4a2f10 +0x1b3\nmain.foo(0xc000012340)"
	got := Canonicalize(input)
	if strings.Contains(got, "0x4a2f10") || strings.Contains(got, "0x1b3") || strings.Contains(got, "0xc000012340") {
		t.Errorf("memory address not stripped: %q", got)
	}
	if !strings.Contains(got, "ADDR") {
		t.Errorf("ADDR placeholder missing: %q", got)
	}
}

// TestFingerprint_IdenticalStacksDedup verifies that two errors differing only
// in timestamps and addresses produce the same fingerprint, and that the first
// app frame location is preserved while runtime frames are collapsed.
func TestFingerprint_IdenticalStacksDedup(t *testing.T) {
	t.Parallel()
	// Two JS errors identical in structure but different timestamps/addresses
	err1 := `TypeError: Cannot read properties of null (reading 'map')
    at ProductList (src/components/List.tsx:42:15)
    at renderWithHooks (node_modules/react-dom/cjs/react-dom.development.js:14985:18)
    at mountIndeterminateComponent (node_modules/react-dom/cjs/react-dom.development.js:17811:13)`
	err2 := `TypeError: Cannot read properties of null (reading 'map')
    at ProductList (src/components/List.tsx:42:15)
    at renderWithHooks (node_modules/react-dom/cjs/react-dom.development.js:14999:22)
    at mountIndeterminateComponent (node_modules/react-dom/cjs/react-dom.development.js:17900:8)`

	c1 := Canonicalize(err1)
	c2 := Canonicalize(err2)

	// 1. Same fingerprint input → same fingerprint
	fp1 := computeFingerprint("browser_js", "TypeError", c1, "http://localhost:3000")
	fp2 := computeFingerprint("browser_js", "TypeError", c2, "http://localhost:3000")
	if fp1 != fp2 {
		t.Errorf("fingerprints differ despite identical structure:\n  fp1=%s\n  fp2=%s\n  c1=%s\n  c2=%s", fp1, fp2, c1, c2)
	}

	// 2. App frame (src/components/List.tsx:42:15) preserved in canonical form
	if !strings.Contains(c1, "src/components/List.tsx") {
		t.Errorf("app frame path stripped from canonical: %q", c1)
	}
	if !strings.Contains(c1, ":42:15") {
		t.Errorf("first app frame line:col stripped: %q", c1)
	}

	// 3. Runtime frames (react-dom) have collapsed line numbers
	if strings.Contains(c1, "14985") || strings.Contains(c1, "17811") {
		t.Errorf("runtime frame line numbers not collapsed: %q", c1)
	}

	// 4. Fingerprint is 16 hex chars
	if len(fp1) != 16 {
		t.Errorf("fingerprint length: got %d, want 16", len(fp1))
	}

	// 5. Different error type → different fingerprint
	fp3 := computeFingerprint("browser_js", "ReferenceError", c1, "http://localhost:3000")
	if fp1 == fp3 {
		t.Error("different category should produce different fingerprint")
	}
}

func TestFingerprint_UUIDStripping(t *testing.T) {
	t.Parallel()
	input := "Request 550e8400-e29b-41d4-a716-446655440000 failed"
	got := Canonicalize(input)
	if strings.Contains(got, "550e8400") {
		t.Errorf("UUID not stripped: %q", got)
	}
	if !strings.Contains(got, "UUID") {
		t.Errorf("UUID placeholder missing: %q", got)
	}
}

func TestEnvelope_SummaryTruncation(t *testing.T) {
	t.Parallel()
	// >200 bytes message
	longMsg := strings.Repeat("x", 250)
	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", longMsg, Context{}, nil)

	if len(ev.Summary) != maxSummaryBytes {
		t.Errorf("Summary len: got %d, want %d", len(ev.Summary), maxSummaryBytes)
	}
	if ev.Summary != longMsg[:maxSummaryBytes] {
		t.Error("Summary not truncated to first 200 bytes")
	}
	// Without a store, PayloadRef is nil regardless of size
	if ev.PayloadRef != nil {
		t.Error("PayloadRef should be nil when no store provided")
	}
}

func TestEnvelope_SummaryTruncation_WithStore(t *testing.T) {
	t.Parallel()
	store := NewBlobStore(0)
	defer store.Close()

	longMsg := strings.Repeat("y", 1200) // >1KB
	ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError", longMsg, Context{}, store)

	if len(ev.Summary) > maxSummaryBytes {
		t.Errorf("Summary too long: %d", len(ev.Summary))
	}
	if ev.PayloadRef == nil {
		t.Fatal("PayloadRef should be set for >1KB payload with store")
	}
	if ev.PayloadRef.Size != len(longMsg) {
		t.Errorf("PayloadRef.Size: got %d, want %d", ev.PayloadRef.Size, len(longMsg))
	}
}
