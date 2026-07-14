package scripts

import (
	"strings"
	"testing"
)

// TestVariantEngineNoCodeOrMarkupInjection is the static half of the INV-6
// guarantee: the variant engine's source must never contain a code path that
// evaluates author-supplied strings as code or markup. This is a source-level
// pin that goes red the moment someone reaches for innerHTML/eval/etc., which a
// browser test could miss on a branch it did not exercise.
func TestVariantEngineNoCodeOrMarkupInjection(t *testing.T) {
	// Forbidden identifiers must not appear anywhere in the module — not even
	// guarded, because the engine renders ONLY through textContent/setAttribute/
	// classList/style.setProperty (spec §6).
	forbidden := []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML",
		"eval(", "new Function", "Function(",
		".setTimeout(", "document.write",
	}
	for _, tok := range forbidden {
		if strings.Contains(variantEngineJS, tok) {
			t.Errorf("variant-engine.js contains forbidden token %q — INV-6 requires no code/markup evaluation path", tok)
		}
	}

	// The safe apply primitives must be present.
	for _, want := range []string{
		"el.textContent = op.value",
		"el.setAttribute(op.name, op.value)",
		"el.classList.add(op.value)",
		"el.style.setProperty(",
		"window.__variantEngine",
	} {
		if !strings.Contains(variantEngineJS, want) {
			t.Errorf("variant-engine.js missing expected safe primitive %q", want)
		}
	}
}

// TestVariantEngineMirrorsP2Allowlists pins that the client-side allowlists match
// the P2 Go validators (op.go/style.go/url.go/limits.go). Defense in depth only
// works if the two stay in lockstep; a drift here is a client-side hole.
func TestVariantEngineMirrorsP2Allowlists(t *testing.T) {
	// setAttribute allowlist EXCLUDES on*/href/src/srcdoc/formaction (op.go).
	for _, forbiddenAttr := range []string{"'href'", "'src'", "'srcdoc'", "'formaction'"} {
		if !strings.Contains(variantEngineJS, forbiddenAttr) {
			t.Errorf("variant-engine.js should name %s in its forbidden-attr switch (mirror op.go)", forbiddenAttr)
		}
	}
	// Forbidden CSS tokens (style.go forbiddenCSSTokens).
	for _, tok := range []string{"url(", "expression(", "@import", "javascript:", "image-set(", "cross-fade("} {
		if !strings.Contains(variantEngineJS, "'"+tok+"'") {
			t.Errorf("variant-engine.js missing forbidden CSS token %q (mirror style.go)", tok)
		}
	}
	// The concrete limits (limits.go) must be encoded verbatim.
	for _, lim := range []string{"256", "4096", "2048"} {
		if !strings.Contains(variantEngineJS, lim) {
			t.Errorf("variant-engine.js missing limit constant %q (mirror limits.go)", lim)
		}
	}
	// https-only URL scheme check (url.go).
	if !strings.Contains(variantEngineJS, "'https:'") {
		t.Error("variant-engine.js must enforce https-only URLs (mirror url.go)")
	}
}
