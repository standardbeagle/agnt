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

// TestFeedbackClientReportsDeliveryOutcome is the always-on source guard for the
// visitor feedback flow. Behaviour (widget states, HTTP and network failures
// reaching the visitor, the exact request sent) is proven against the shipped
// bytes in the vitest + jsdom tier: jstest/public-feedback.test.js (`make
// test-js`). This guard keeps the contract visible to `make test`, which must
// not need node: the client is no longer a fire-and-forget stub, and the widget
// it mounts cannot become a markup or inline-style sink on the public plane.
func TestFeedbackClientReportsDeliveryOutcome(t *testing.T) {
	src := moduleScript["feedback-client"]
	for _, want := range []string{
		"res.ok",                     // a non-2xx response is a failure, not a success
		"'network'",                  // a rejected fetch is reported, not swallowed
		"mount: mount",               // the visitor widget is part of the surface
		"credentials: 'omit'",        // anonymous plane: no cookies ride along
		"'application/json'",         // the only content type the route accepts
		"MAX_BODY_BYTES = 4096",      // mirrors the server's default body cap
		"role', 'status'",            // submission states are announced
		"setAttribute('for', ",       // fields are labelled
		"addEventListener('keydown'", // Escape closes the form
	} {
		if !strings.Contains(src, want) {
			t.Errorf("feedback-client missing %q", want)
		}
	}
	for _, banned := range []string{
		"best-effort", // the stub's swallowed .catch
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
		"setAttribute('style'", "createElement('style')", // refused by the public style-src
	} {
		if strings.Contains(src, banned) {
			t.Errorf("feedback-client must not contain %q", banned)
		}
	}
	for _, tok := range forbiddenPublicTokens {
		if strings.Contains(src, tok) {
			t.Errorf("feedback-client contains forbidden dev-surface token %q", tok)
		}
	}
}
