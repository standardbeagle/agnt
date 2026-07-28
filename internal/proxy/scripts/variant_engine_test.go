package scripts

import (
	"strings"
	"testing"
)

// TestVariantEngineNoDynamicCodeCompilation pins what survives INV-6's
// retirement (spec §6a, 2026-07-27). Raw authored markup/CSS/script are now
// legal op payloads, so `innerHTML` and `<script>` emission are no longer
// contraband. What is STILL contraband is dynamic code compilation: INV-12 pins
// script-src to 'self' plus per-authored-revision sha256 hashes, with no
// 'unsafe-eval' and no 'unsafe-inline'. An eval/new Function path could not run
// under that CSP anyway, and reaching for one would be an attempt to route
// around the hash pinning that IS the containment story.
func TestVariantEngineNoDynamicCodeCompilation(t *testing.T) {
	forbidden := []string{
		"eval(", "new Function", "Function(",
		".setTimeout(", "document.write",
	}
	for _, tok := range forbidden {
		if strings.Contains(variantEngineJS, tok) {
			t.Errorf("variant-engine.js contains forbidden token %q — INV-12 forbids a dynamic-code path (script-src carries no 'unsafe-eval')", tok)
		}
	}

	// The declarative apply primitives are unchanged by §6a.
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

// TestVariantEngineRendersRawOps pins the render primitive for each raw-content
// op (spec §6a) — one assertion per op kind:
//
//	setHTML   -> el.innerHTML = op.html            (element-targeting)
//	addStyle  -> <style> appended to variant root  (variant-root op)
//	addScript -> inline <script> appended to root  (variant-root op)
//
// The script primitive is deliberately an ELEMENT whose body is the authored
// text, never a compiled function: the browser runs it only when its sha256 is
// present in the served revision's script-src (INV-12), which is what makes
// publisher script executable and byte-identical upstream script inert.
func TestVariantEngineRendersRawOps(t *testing.T) {
	for _, want := range []string{
		"el.innerHTML = op.html",
		"document.createElement('style')",
		"styleEl.textContent = op.css",
		"document.createElement('script')",
		"scriptEl.textContent = op.code",
		"__variant-engine-root",
	} {
		if !strings.Contains(variantEngineJS, want) {
			t.Errorf("variant-engine.js missing raw-op render primitive %q (spec §6a)", want)
		}
	}
	// A served revision must contain no <script src> (§6a): the publish pipeline
	// inlines src at publish time, so the renderer never sets that attribute.
	if strings.Contains(variantEngineJS, "scriptEl.src") || strings.Contains(variantEngineJS, "setAttribute('src', op.src") {
		t.Error("variant-engine.js must never emit a <script src> — src is a publish-time fetch input (§6a)")
	}
}

// TestVariantEngineRawOpGuards pins the surviving guards for the raw ops: the
// §5 size caps, and §6a's structural rule that addStyle/addScript are
// variant-root ops for which a selector is a rejection rather than a hint.
// There is deliberately NO "script-ness" scan of the payloads — §6a forbids
// reintroducing one.
func TestVariantEngineRawOpGuards(t *testing.T) {
	for _, want := range []string{
		"MAX_RAW_HTML_BYTES = 8192",
		"MAX_RAW_SCRIPT_BYTES = 16384",
		"case 'setHTML'",
		"case 'addStyle'",
		"case 'addScript'",
		"unexpected field 'selector'",
	} {
		if !strings.Contains(variantEngineJS, want) {
			t.Errorf("variant-engine.js missing raw-op guard %q (mirror internal/publish op.go/schema.go)", want)
		}
	}
	// An addScript still carrying src at RENDER time is refused: reaching the
	// browser with an unresolved src means the publish-time fetch did not run,
	// and the renderer must fail closed rather than invent a runtime fetch.
	if !strings.Contains(variantEngineJS, "src must be inlined at publish time") {
		t.Error("variant-engine.js must refuse an unresolved addScript src at render time (§6a)")
	}
}

func TestDesignVariantSetBridgeIsExported(t *testing.T) {
	for _, want := range []string{
		"importVariantSet: importVariantSet",
		"exportVariantSet: exportVariantSet",
		"applyVariant: applyVariant",
		"revertVariant: revertVariant",
	} {
		if !strings.Contains(designJS, want) {
			t.Errorf("design.js missing variant bridge export %q", want)
		}
	}
}
