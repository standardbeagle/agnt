package proxy

// demo_indicator_disable_fuzz_test.go is INV-14's "fuzz the publish inputs for a
// disable path -> none exists" clause, at Tier A (pure Go, always runs, no
// browser). The real-Chrome half — a hostile variant losing to the badge on a
// live page — is demo_indicator_tamper_e2e_test.go.
//
// WHAT IS ACTUALLY BEING PROVEN, and why it is a byte comparison. INV-14 says the
// disclosure depends on NO input. The strongest available statement of that is not
// "the badge still appears" (which a helpful assertion could accidentally satisfy)
// but: across every publisher-reachable input this plane accepts, the bytes that
// carry the disclosure are IDENTICAL to the baseline bytes. A response that
// differs is where a conditional would have to live, so byte-equality is the
// property, and any future disable knob — a config key, an op, a query parameter,
// a feedback side effect — fails it by construction rather than by the test
// having anticipated that particular knob.
//
// The four fuzzed axes are the four INV-14 names: config, ops, query params, and
// the feedback payload. A fifth (the PROXIED path's arbitrary upstream document)
// is covered through injectPublicBundle, because "every public artifact response"
// includes the response whose body an attacker-adjacent upstream wrote.
//
// A REJECTED input is not a finding. Several fuzzed ops are refused at publish
// time; that is a loud refusal, not a disable path, so the assertion for those is
// simply that nothing was published. The interesting population is the ACCEPTED
// one — since INV-6's retirement most hostile ops publish successfully and are
// contained at runtime instead.

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// demoIndicatorBundleMarkers are the module's identity inside the served bundle:
// its public surface, the host id a viewer's DOM carries, and the disclosure text
// itself. All three must survive every input below.
var demoIndicatorBundleMarkers = []string{
	"__agntDemoIndicator",
	"agnt-demo-indicator",
	"Demo walkthrough — not the live site.",
}

// disableWords are the vocabulary a disable path would plausibly be spelled in.
// They are used as fuzz INPUT (query keys, config strings, feedback bodies), not
// as a source scan — the point is that feeding them in changes nothing.
var disableWords = []string{
	"indicator", "demo", "badge", "disclosure", "watermark", "banner", "brand",
	"hide", "hidden", "disable", "disabled", "off", "no", "none", "suppress",
	"nobadge", "no_indicator", "demo_indicator", "agnt", "__agntDemoIndicator",
	"agnt-demo-indicator", "chrome", "clean", "bare", "raw", "embed", "kiosk",
}

// servedArtifact fetches the artifact document and the bundle asset for a token.
func servedArtifact(t *testing.T, p *e2ePlane, path string) (string, http.Header) {
	t.Helper()
	code, body, hdr := p.get(t, path)
	require.Equal(t, http.StatusOK, code, "GET %s", path)
	return body, hdr
}

// assertCarriesDisclosure is the single verdict: this artifact response loads the
// bundle under its integrity pin, and the bundle it names still contains the
// disclosure module.
func assertCarriesDisclosure(t *testing.T, p *e2ePlane, doc, label string) {
	t.Helper()
	assetPath := PublicInstrumentationAssetPath()
	hash := PublicInstrumentationAssetCSPHash()
	assert.Contains(t, doc, `<script src="`+assetPath+`" integrity="`+hash+`"`,
		"%s: the artifact must load the pinned RolePublic bundle", label)

	asset, hdr := servedArtifact(t, p, assetPath)
	for _, marker := range demoIndicatorBundleMarkers {
		assert.Contains(t, asset, marker, "%s: the served bundle lost demo-indicator marker %q", label, marker)
	}
	assert.NotContains(t, hdr.Get("Content-Security-Policy"), "unsafe-inline",
		"%s: no input may widen the CSP that contains a hostile page", label)
}

// nonceRe matches the per-response CSP nonce in the self-contained shell.
var nonceRe = regexp.MustCompile(`nonce="[A-Za-z0-9+/_=-]+"`)

// normalizeNonce is the ONE exemption from the byte comparison, and it is an
// exemption in the safe direction. The shell's inline-style nonce is fresh per
// response BY DESIGN (newNonce fails closed rather than reuse a predictable
// value), so a literal byte comparison would fail on every input including the
// benign ones — proving nothing about the disclosure. Normalizing it keeps the
// comparison exact over every other byte, and the assertion below still requires
// a non-empty nonce to be present, so "the nonce vanished" is a failure rather
// than something this helper hides.
func normalizeNonce(t *testing.T, doc, label string) string {
	t.Helper()
	require.Regexp(t, nonceRe, doc, "%s: the shell must still carry a per-response CSP nonce", label)
	return nonceRe.ReplaceAllString(doc, `nonce="<per-response>"`)
}

// TestDemoIndicatorNoDisablePathViaPublishInputs fuzzes the four publisher- and
// viewer-reachable input surfaces and asserts none of them can remove, blank, or
// even perturb the disclosure the plane serves.
func TestDemoIndicatorNoDisablePathViaPublishInputs(t *testing.T) {
	p := newE2EPlane(t)

	// BASELINE: a benign share, and the exact bytes it serves.
	_, baseToken, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
	require.NoError(t, err)
	rawBaseDoc, _ := servedArtifact(t, p, "/s/"+baseToken)
	baseDoc := normalizeNonce(t, rawBaseDoc, "baseline")
	baseAsset, _ := servedArtifact(t, p, PublicInstrumentationAssetPath())
	assertCarriesDisclosure(t, p, rawBaseDoc, "baseline")

	// Premise: the baseline really does carry the module. If this ever stops
	// holding, every "unchanged" assertion below would be vacuously green.
	require.Contains(t, baseAsset, "__agntDemoIndicator",
		"premise broken: the baseline bundle does not contain the disclosure module")

	t.Run("config_axis_handler_construction_carries_no_disable_knob", func(t *testing.T) {
		// The public handler's ONLY constructor inputs are the verifier, the
		// feedback sink, and a body cap. Sweeping the one numeric knob across its
		// whole range (including the invalid values that fall back to the default)
		// must not change a byte of what is served.
		for _, maxBody := range []int{-1, 0, 1, 64, 4096, 1 << 20} {
			h := NewPublicHandler(p.store, p.feedback, maxBody)
			req, _ := http.NewRequest(http.MethodGet, "/s/"+baseToken, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, "maxBody=%d", maxBody)
			assert.Equal(t, baseDoc, normalizeNonce(t, rec.Body.String(), fmt.Sprintf("maxBody=%d", maxBody)),
				"maxBody=%d changed the served artifact — the plane has no disclosure knob and must have none", maxBody)
		}
	})

	t.Run("query_param_axis_no_key_or_value_alters_the_response", func(t *testing.T) {
		// Every disable word as a key, every disable word as a value, plus the
		// truthy/falsy spellings a knob would accept.
		values := append([]string{"0", "1", "true", "false", "yes", "no", "off", "on", ""}, disableWords...)
		for _, key := range disableWords {
			for _, val := range values {
				q := url.Values{key: []string{val}}.Encode()
				raw, hdr := servedArtifact(t, p, "/s/"+baseToken+"?"+q)
				require.Equal(t, baseDoc, normalizeNonce(t, raw, q),
					"query %s=%s changed the served artifact — a viewer-supplied parameter must not reach the disclosure", key, val)
				assert.NotContains(t, hdr.Get("Content-Security-Policy"), "unsafe-inline",
					"query %s=%s widened the CSP", key, val)
			}
		}
	})

	t.Run("op_axis_hostile_ops_publish_but_never_change_what_is_served", func(t *testing.T) {
		host := "#agnt-demo-indicator"
		hostile := [][]publish.Op{
			{{Op: publish.OpApplyStyle, Selector: host, Props: map[string]string{"display": "none"}}},
			{{Op: publish.OpApplyStyle, Selector: host, Props: map[string]string{"opacity": "0", "visibility": "hidden"}}},
			{{Op: publish.OpSetText, Selector: host, Value: ""}},
			{{Op: publish.OpSetHTML, Selector: host, HTML: "<span></span>"}},
			{{Op: publish.OpSetHTML, Selector: "body", HTML: "<div>wiped</div>"}},
			{{Op: publish.OpAddClass, Selector: host, Value: "hidden"}},
			{{Op: publish.OpRemoveClass, Selector: host, Value: "badge"}},
			{{Op: publish.OpSetAttribute, Selector: host, Name: "class", Value: "gone"}},
			{{Op: publish.OpAddStyle, CSS: "#agnt-demo-indicator{display:none!important}"}},
			{{Op: publish.OpAddScript, Code: "document.getElementById('agnt-demo-indicator').remove();"}},
			{{Op: publish.OpAddStyle, CSS: ":root{--agnt-demo-indicator:none}"}},
		}
		for i, ops := range hostile {
			wt := e2eWalkthrough()
			wt.ID = fmt.Sprintf("wt-hostile-%d", i)
			wt.VariantSet.ID = fmt.Sprintf("set-hostile-%d", i)
			wt.VariantSet.Variants[0].Ops = ops
			_, token, cerr := p.store.Create(wt, e2eProjectA)
			if cerr != nil {
				// A loud refusal is not a disable path. Nothing was published, so
				// there is nothing to serve.
				continue
			}
			doc, _ := servedArtifact(t, p, "/s/"+token)
			assertCarriesDisclosure(t, p, doc, fmt.Sprintf("hostile ops #%d (%s)", i, ops[0].Op))
			asset, _ := servedArtifact(t, p, PublicInstrumentationAssetPath())
			assert.Equal(t, baseAsset, asset,
				"hostile ops #%d changed the served BUNDLE — no op may reach the module bytes", i)
		}
	})

	t.Run("op_axis_randomized_sweep", func(t *testing.T) {
		// A deterministic pseudo-random sweep over op shapes aimed at the badge:
		// the same property, asserted over combinations no table enumerates.
		rng := rand.New(rand.NewSource(114))
		opTypes := []publish.OpType{
			publish.OpSetText, publish.OpSetAttribute, publish.OpAddClass, publish.OpRemoveClass,
			publish.OpApplyStyle, publish.OpSetHTML, publish.OpAddStyle, publish.OpAddScript,
		}
		selectors := []string{"#agnt-demo-indicator", "body", "div", "*", "#app > div", "[id]"}
		published := 0
		for i := 0; i < 150; i++ {
			word := disableWords[rng.Intn(len(disableWords))]
			op := publish.Op{Op: opTypes[rng.Intn(len(opTypes))]}
			switch op.Op {
			case publish.OpAddStyle:
				op.CSS = "#agnt-demo-indicator{" + word + ":none!important}"
			case publish.OpAddScript:
				op.Code = "window." + "__x" + fmt.Sprint(i) + "=" + strconv.Quote(word) + ";"
			case publish.OpApplyStyle:
				op.Selector = selectors[rng.Intn(len(selectors))]
				op.Props = map[string]string{word: "none"}
			case publish.OpSetHTML:
				op.Selector = selectors[rng.Intn(len(selectors))]
				op.HTML = "<i>" + word + "</i>"
			case publish.OpSetAttribute:
				op.Selector = selectors[rng.Intn(len(selectors))]
				op.Name = "data-" + "x"
				op.Value = word
			default:
				op.Selector = selectors[rng.Intn(len(selectors))]
				op.Value = word
			}
			wt := e2eWalkthrough()
			wt.ID = fmt.Sprintf("wt-rand-%d", i)
			wt.VariantSet.ID = fmt.Sprintf("set-rand-%d", i)
			wt.VariantSet.Variants[0].Ops = []publish.Op{op}
			_, token, cerr := p.store.Create(wt, e2eProjectA)
			if cerr != nil {
				continue
			}
			published++
			doc, _ := servedArtifact(t, p, "/s/"+token)
			require.Contains(t, doc, `integrity="`+PublicInstrumentationAssetCSPHash()+`"`,
				"random op #%d (%s) changed the artifact's bundle pin", i, op.Op)
		}
		// Premise: the sweep must actually have published things, or it asserted
		// nothing at all.
		require.Greater(t, published, 20,
			"premise broken: almost every randomized op was rejected, so the accepted-input space went untested")
	})

	t.Run("feedback_axis_viewer_payloads_have_no_side_effect_on_the_disclosure", func(t *testing.T) {
		for _, word := range disableWords {
			body := `{"message":` + jsonString(word+" indicator=off demo-indicator:none") + `,"rating":1}`
			code, _ := p.postFeedback(t, baseToken, "application/json", body)
			require.NotEqual(t, http.StatusInternalServerError, code, "feedback %q crashed the plane", word)
			raw, _ := servedArtifact(t, p, "/s/"+baseToken)
			require.Equal(t, baseDoc, normalizeNonce(t, raw, word),
				"feedback payload %q changed the served artifact — an anonymous viewer must not reach the disclosure", word)
		}
		asset, _ := servedArtifact(t, p, PublicInstrumentationAssetPath())
		assert.Equal(t, baseAsset, asset, "feedback traffic must not reach the bundle bytes")
	})

	t.Run("upstream_document_axis_proxied_injection_always_lands", func(t *testing.T) {
		// The PROXIED path's body is written by the upstream, not by us. Whatever
		// shape it arrives in — including shapes designed to make an injector give
		// up — the bundle reference must still be emitted, because the disclosure
		// ships inside that bundle.
		assetPath := PublicInstrumentationAssetPath()
		hash := PublicInstrumentationAssetCSPHash()
		docs := []string{
			"<!DOCTYPE html><html><head></head><body>hi</body></html>",
			"<html><body>no head</body></html>",
			"plain text, no markup at all",
			"",
			"<!-- <head> --><body>commented head</body>",
			"<HTML><HEAD><TITLE>caps</TITLE></HEAD><BODY></BODY></HTML>",
			"<html><head><meta charset=\"utf-8\"><script>var x=1;</script></head><body></body></html>",
			strings.Repeat("<div>a</div>", 5000),
			"<html><head>" + strings.Repeat("<!--x-->", 2000) + "</head><body></body></html>",
		}
		for i, in := range docs {
			out := string(injectPublicBundle([]byte(in), assetPath, hash))
			assert.Contains(t, out, assetPath,
				"upstream document #%d: the proxied artifact must still load the bundle carrying the disclosure", i)
			assert.Contains(t, out, hash,
				"upstream document #%d: the bundle reference must stay integrity-pinned", i)
		}
	})
}
