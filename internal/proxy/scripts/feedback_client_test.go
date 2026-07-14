package scripts

import (
	"strings"
	"testing"
)

// TestFeedbackClientRejectsProtocolRelativeEndpoint pins the P4-advisory fix: the
// public feedback client's endpoint guard must reject a PROTOCOL-RELATIVE target
// like "//evil.com/collect" (which a browser resolves cross-origin, defeating
// connect-src 'self' / INV-7), not just non-slash strings.
//
// No JS engine is vendored, so this is a source-shape assertion over the embedded
// module (the sanctioned scripts-test form): it proves the hardened guard is
// present and the naive charAt(0)-only accept is gone.
func TestFeedbackClientRejectsProtocolRelativeEndpoint(t *testing.T) {
	src := moduleScript["feedback-client"]
	if src == "" {
		t.Fatal("feedback-client module source not embedded")
	}

	// The vulnerable guard accepted any string starting with '/', so "//evil.com"
	// passed. It must be gone.
	if strings.Contains(src, "opts.endpoint.charAt(0) === '/')") {
		t.Fatal("feedback-client still uses the naive charAt(0)-only endpoint guard; " +
			"protocol-relative //evil.com would be accepted")
	}

	// The hardened guard inspects the SECOND character to reject "//host" and
	// backslash-smuggled "/\\host".
	if !strings.Contains(src, "sameOriginPath") {
		t.Fatal("feedback-client missing the sameOriginPath guard")
	}
	if !strings.Contains(src, "charAt(1)") {
		t.Fatal("hardened guard must inspect the second char to reject protocol-relative endpoints")
	}
	// It must reject a scheme marker so "javascript:" / "https://evil" cannot slip
	// through as an endpoint.
	if !strings.Contains(src, "'://'") {
		t.Fatal("hardened guard must reject scheme-bearing endpoints ('://')")
	}
}
