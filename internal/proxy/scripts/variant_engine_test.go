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
//
// It also pins the raw-markup SINK list. §6a sanctioned exactly ONE markup
// primitive — `innerHTML` on the op's matched element — because that is the only
// sink whose blast radius is the selector the publisher named. `outerHTML`
// replaces the matched element itself (it can delete the engine's own anchor and
// escape the intended target), and `insertAdjacentHTML` writes outside the
// element entirely. Neither was sanctioned; both were dropped from this list
// alongside `innerHTML` by mistake. Re-banned here.
func TestVariantEngineNoDynamicCodeCompilation(t *testing.T) {
	forbidden := []string{
		"eval(", "new Function", "Function(",
		".setTimeout(", "document.write",
		"outerHTML", "insertAdjacentHTML",
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

// TestVariantEngineDoesNotResurrectRetiredStyleRule is the flipped half of the
// old forbidden-CSS-token assertion. Those tokens used to be REQUIRED here;
// INV-6's retirement (2026-07-27) removed the §5b property allowlist and the §5
// forbidden-token scan from internal/publish/style.go, and §6a explicitly
// forbids reintroducing the scan as defense-in-depth. The JS mirror kept
// enforcing both, so the Go validator accepted an `applyStyle {background:
// url(...)}` op at publish while the browser silently refused to apply it —
// neither rejected loudly nor honored. The rule must now be absent from BOTH
// mirrors, so the assertion is inverted rather than deleted.
func TestVariantEngineDoesNotResurrectRetiredStyleRule(t *testing.T) {
	// Match the retired CODE shapes, not prose: the file still *documents* the
	// retirement (and the attr allowlist, which survives, reuses the phrase "not
	// on allowlist"), so a bare substring sweep would false-positive on both.
	for _, gone := range []string{
		"FORBIDDEN_CSS_TOKENS", "hasForbiddenCSSToken", "CSS_PROP_NAME_RE",
		"ALLOWED_CSS_EXACT", "ALLOWED_CSS_PREFIX", "allowedCSSProperty(",
		"'expression('", "'-moz-binding'", "'image-set('", "'cross-fade('",
		"has forbidden token in value", "illegal backslash escape",
		"value has illegal character",
	} {
		if strings.Contains(variantEngineJS, gone) {
			t.Errorf("variant-engine.js still carries retired §5b/§5 style rule %q — style.go dropped it and §6a forbids reintroducing it as defense-in-depth", gone)
		}
	}
	// The guard that SURVIVES on the same payload: the size cap.
	if !strings.Contains(variantEngineJS, "MAX_STYLE_PATCH_BYTES") {
		t.Error("variant-engine.js must keep the §5 style-patch size cap (an abuse control, not an injection control)")
	}
}

// TestVariantEngineMirrorsAggregateBudgets pins the direction the JS mirror was
// LOOSER than Go. The engine mirrored only the per-op caps, but §5's real bound
// is an aggregate that a tampered snapshot can trivially dodge by splitting one
// oversize payload across several ops: the style patch is per VARIANT
// (Variant.Validate in internal/publish/validate.go — applyStyle + addStyle
// share one budget). The per-revision SCRIPT budget that used to sit beside it
// is gone on both sides, with the op it budgeted (INV-14).
func TestVariantEngineMirrorsAggregateBudgets(t *testing.T) {
	for _, want := range []string{
		"styleBytesOf",         // applyStyle + addStyle spend one budget
		"variant style budget", // per-variant refusal reason
	} {
		if !strings.Contains(variantEngineJS, want) {
			t.Errorf("variant-engine.js missing aggregate-budget mirror %q (mirror internal/publish/validate.go)", want)
		}
	}
}

// TestVariantEngineRendersRawOps pins the render primitive for each raw-content
// op (spec §6a) — one assertion per op kind:
//
//	setHTML  -> el.innerHTML = op.html            (element-targeting)
//	addStyle -> <style> appended to variant root  (variant-root op)
//
// There is deliberately NO script primitive: see
// TestVariantEngineHasNoScriptPrimitive.
func TestVariantEngineRendersRawOps(t *testing.T) {
	for _, want := range []string{
		"el.innerHTML = op.html",
		"document.createElement('style')",
		"styleEl.textContent = op.css",
		"__variant-engine-root",
	} {
		if !strings.Contains(variantEngineJS, want) {
			t.Errorf("variant-engine.js missing raw-op render primitive %q (spec §6a)", want)
		}
	}
}

// TestVariantEngineHasNoScriptPrimitive is the source guard for INV-14 on the
// renderer: the engine creates no script element, by any spelling, and refuses
// the addScript op by name. Behaviour — that a variant set carrying the op
// leaves no script element in the document and reports the refusal — is proven
// in the jsdom tier (jstest/variant-engine.test.js, "addScript"); this guard
// keeps the property when node is not available.
func TestVariantEngineHasNoScriptPrimitive(t *testing.T) {
	for _, gone := range []string{
		"createElement('script')",
		`createElement("script")`,
		"scriptEl",
		"op.code;",
		"case 'addScript'",
		"MAX_RAW_SCRIPT_BYTES",
		"revision script budget",
		"src must be inlined at publish time",
		".src = op.src",
		"setAttribute('src', op.src",
	} {
		if strings.Contains(variantEngineJS, gone) {
			t.Errorf("variant-engine.js still carries %q — the renderer must have no authored-script path at all (INV-14)", gone)
		}
	}
	// The refusal is explicit and names the rule. Spelled as literals so a
	// reworded or deleted refusal fails here.
	for _, want := range []string{
		"if (op.op === 'addScript') { return { ok: false, reason: ADD_SCRIPT_REFUSED }; }",
		"addScript is not permitted on a published walkthrough",
		"INV-14",
	} {
		if !strings.Contains(variantEngineJS, want) {
			t.Errorf("variant-engine.js missing the explicit addScript refusal %q", want)
		}
	}
	// addScript is not a root op any more: only addStyle appends to the root.
	if !strings.Contains(variantEngineJS, "function isRootOp(op) { return op && op.op === 'addStyle'; }") {
		t.Error("isRootOp must admit addStyle only")
	}
}

// TestVariantEngineRawOpGuards pins the surviving guards for the raw ops: the
// §5 size caps, and §6a's structural rule that addStyle is a variant-root op
// for which a selector is a rejection rather than a hint. There is deliberately
// NO "script-ness" scan of the payloads — §6a forbids reintroducing one.
func TestVariantEngineRawOpGuards(t *testing.T) {
	for _, want := range []string{
		"MAX_RAW_HTML_BYTES = 8192",
		"case 'setHTML'",
		"case 'addStyle'",
		"unexpected field 'selector'",
	} {
		if !strings.Contains(variantEngineJS, want) {
			t.Errorf("variant-engine.js missing raw-op guard %q (mirror internal/publish op.go/schema.go)", want)
		}
	}
}

// TestVariantEngineCapsAreByteDenominated is the source-level guard against the
// cap-UNIT drift that is this file's whole reason for existing. Every §5 size
// limit in internal/publish is a Go `len(...)` — UTF-8 BYTES. JS `.length` is
// UTF-16 CODE UNITS, which for CJK undercounts 3x and for astral-plane 4x, so a
// `.length` comparison silently accepts payloads the Go validator rejects. Each
// cap site must therefore route through utf8Len().
//
// Written as an exact-source assertion per site (not a bare "utf8Len appears
// somewhere") so re-introducing `.length` at ANY ONE of them fails, even while
// the other sites stay converted — the precise way this drifted the first time.
func TestVariantEngineCapsAreByteDenominated(t *testing.T) {
	sites := []struct {
		what   string
		goRef  string
		byteEd string // the required byte-denominated comparison
		unitEd string // the code-unit comparison it replaced
	}{
		{
			what:   "selector length",
			goRef:  "internal/publish/selector.go:37 len(sel) > MaxSelectorLength",
			byteEd: "var selBytes = utf8Len(sel);",
			unitEd: "sel.length > MAX_SELECTOR_LENGTH",
		},
		{
			what:   "URL length",
			goRef:  "internal/publish/url.go:15 len(raw) > MaxURLLength",
			byteEd: "var rawBytes = utf8Len(raw);",
			unitEd: "raw.length > MAX_URL_LENGTH",
		},
		{
			what:   "setAttribute value",
			goRef:  "internal/publish/op.go:93 len(o.Value) > MaxAttrValueLength",
			byteEd: "if (utf8Len(String(op.value || '')) > MAX_ATTR_VALUE_LENGTH)",
			unitEd: "String(op.value || '').length > MAX_ATTR_VALUE_LENGTH",
		},
	}
	for _, s := range sites {
		if !strings.Contains(variantEngineJS, s.byteEd) {
			t.Errorf("variant-engine.js %s cap must compare UTF-8 bytes (%q), mirroring %s", s.what, s.byteEd, s.goRef)
		}
		if strings.Contains(variantEngineJS, s.unitEd) {
			t.Errorf("variant-engine.js %s cap compares UTF-16 code units (%q) against a byte-denominated Go limit (%s) — 3x gap for CJK, 4x for emoji", s.what, s.unitEd, s.goRef)
		}
	}

	// The header's blanket claim is only honest while the above holds; pin the
	// sentence that carries it so the claim and the code are edited together.
	if !strings.Contains(variantEngineJS, "compared in UTF-8 BYTES via utf8Len(), matching Go's len()") {
		t.Error("variant-engine.js MIRROR SCOPE header must state the size limits are byte-denominated — a blanket 'every §5 size limit' claim with no unit is what let the mirrors drift")
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
