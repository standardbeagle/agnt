package proxy

// publish_e2e_test.go is the P10 capstone: the end-to-end SECURITY, RESTART, and
// REVOKE gate for the whole walkthrough-publish stack. It proves the shipped
// stack is safe by driving the REAL mounted public plane — a real
// proxy.PublicHandler constructed over a real publish.Store and a real
// publish.FeedbackStore (both over temp dirs), served on an EPHEMERAL TCP
// listener (httptest.NewServer) — end to end, with anonymous HTTP requests the
// way a public viewer's browser would make them. Nothing here is mocked: the
// verifier is the durable store, the feedback sink is the durable feedback
// store, the bytes travel over a real socket.
//
// Tier A (this file) is pure-Go and always runs, including under -race (the
// concurrency gate). Tier B (publish_browser_e2e_test.go, build-tagged
// `chromee2e`, run via `make test-chrome-e2e` / `make e2e-publish-browser`)
// drives the same served artifact in real Chrome. The split is load-bearing for
// this file's scope: Tier A can only prove the bytes and header STRINGS on the
// wire, so any claim about what a browser actually does with them — notably
// whether a script runs — belongs to Tier B, not here.
//
// The suite is deliberately adversarial: most of it asserts NEGATIVES — the dev
// control surface is unreachable, injection payloads are rejected or stored
// inert and never reflected (since INV-6's retirement, raw publisher-authored
// CSS/HTML lands on the "inert" side: accepted at create, contained by the
// wholesale-replaced CSP), traversal serves no file, an unknown/revoked/rotated
// token is an indistinguishable 404, and a crash mid-write makes the store reload
// FAIL LOUD rather than silently serve an empty store.
//
// One deliberate exception to "inert" post-S3/S4: a publisher-authored
// addScript{code} body DOES run, because serving pins its exact sha256 into
// script-src (authoredScriptCSPHashes). That widening is hash-only — never
// 'unsafe-inline', never 'self' — so publisher script executes while upstream
// and viewer script still cannot. The revisions exercised in this file carry no
// addScript op, so the CSPs asserted below are the un-widened shape (bundle hash
// alone); the widened shape and its EXECUTION consequences are Tier B's
// TestE2E_PublicPlane_RealBrowser_AuthoredScriptExecutionCSP.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	e2eProjectA = "/proj/a"
	e2eProjectB = "/proj/b"
)

// e2ePlane is the real public plane stood up in-test: the durable stores plus the
// live ephemeral HTTP listener that serves the real PublicHandler over them.
type e2ePlane struct {
	srv      *httptest.Server
	store    *publish.Store
	feedback *publish.FeedbackStore
	storeDir string
	fbDir    string
}

func e2eLimits() publish.FeedbackLimits {
	return publish.FeedbackLimits{
		RatePerMinute:   600,
		Burst:           600,
		MaxBodyBytes:    4096,
		MaxRowsPerShare: 1000,
		RetentionDays:   30,
	}
}

// newE2EPlane builds the real stores over fresh temp dirs, wires the REAL
// PublicHandler over them (the feedback store itself is the sink — *FeedbackStore
// satisfies FeedbackSink), and serves it on an ephemeral TCP port.
func newE2EPlane(t *testing.T) *e2ePlane {
	t.Helper()
	storeDir := t.TempDir()
	fbDir := t.TempDir()
	store, err := publish.New(storeDir, nil)
	require.NoError(t, err, "open publish store")
	feedback, err := publish.NewFeedbackStore(fbDir, e2eLimits(), nil)
	require.NoError(t, err, "open feedback store")
	h := NewPublicHandler(store, feedback, int(e2eLimits().MaxBodyBytes))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &e2ePlane{srv: srv, store: store, feedback: feedback, storeDir: storeDir, fbDir: fbDir}
}

// e2eWalkthrough is a rich, VALID published artifact: two steps plus a variant
// set bound to step s1, exercising the full serve path (artifact/variants/
// walkthrough).
func e2eWalkthrough() *publish.PublishedWalkthrough {
	return &publish.PublishedWalkthrough{
		Version: publish.SchemaV1,
		ID:      "wt-e2e",
		Title:   "E2E Walkthrough",
		Steps: []publish.Step{
			{ID: "s1", Title: "Intro", Body: "welcome", Target: "#cta", Advance: publish.Advance{Type: "click-target"}},
			{ID: "s2", Title: "Done", Body: "bye", Advance: publish.Advance{Type: "auto", MS: 500}},
		},
		VariantSet: &publish.VariantSet{
			Version: publish.SchemaV1,
			ID:      "set-e2e",
			StepID:  "s1",
			Variants: []publish.Variant{
				{ID: "a", Label: "A", Ops: []publish.Op{
					{Op: publish.OpSetText, Selector: "#title", Value: "Changed"},
					{Op: publish.OpAddClass, Selector: ".msg", Value: "hl"},
				}},
			},
		},
	}
}

// get performs an anonymous GET over the real listener and returns status, body,
// and headers.
func (p *e2ePlane) get(t *testing.T, path string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(p.srv.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

// postFeedback POSTs an anonymous feedback body over the real listener.
func (p *e2ePlane) postFeedback(t *testing.T, token, contentType, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(p.srv.URL+"/s/"+token+"/feedback", contentType, strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// rawGet writes a LITERAL request line to the real listener so a non-canonical
// path (../, %2e%2e, //) reaches the server's traversal defence unmolested by any
// client-side URL cleaning. Returns the status code.
func rawGet(t *testing.T, addr, rawPath string) int {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: e2e\r\nConnection: close\r\n\r\n", rawPath)
	data, err := io.ReadAll(conn)
	require.NoError(t, err)
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	// "HTTP/1.1 404 Not Found"
	fields := strings.Fields(line)
	require.GreaterOrEqual(t, len(fields), 2, "status line: %q", line)
	var code int
	fmt.Sscanf(fields[1], "%d", &code)
	return code
}

// ownerScopedFeedbackRead replicates the authoritative owner-scoped MCP feedback
// read gate (daemon.hubHandlePublishFeedback): a session scoped to projectPath
// may read a share's feedback ONLY if that project owns the share
// (store.List(projectPath) contains the id — same gate as shareOwned), then reads
// the rows share-scoped from the feedback store. Returns ok=false when the
// project does not own the share (a foreign share is not-found: no cross-project
// leak, INV-1 / spec §2a).
func ownerScopedFeedbackRead(store *publish.Store, feedback *publish.FeedbackStore, projectPath, shareID string) (rows []publish.FeedbackRecord, ok bool) {
	owned := false
	for _, info := range store.List(projectPath) {
		if info.ID == shareID {
			owned = true
			break
		}
	}
	if !owned {
		return nil, false
	}
	r, _, err := feedback.ReadByShare(shareID, "", 0)
	if err != nil {
		return nil, false
	}
	return r, true
}

// TestE2EPublishGate is the single coherent end-to-end journey plus the
// adversarial probes, all against ONE live public plane.
func TestE2EPublishGate(t *testing.T) {
	p := newE2EPlane(t)

	// ---- 1. HAPPY PATH ----------------------------------------------------
	id, token, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
	require.NoError(t, err, "create share")
	require.NotEmpty(t, token)

	assetPath := PublicInstrumentationAssetPath()
	assetHash := PublicInstrumentationAssetCSPHash()

	t.Run("artifact_serves_public_bundle_and_INV12_CSP", func(t *testing.T) {
		code, body, hdr := p.get(t, "/s/"+token)
		require.Equal(t, http.StatusOK, code)
		// The shell loads ONLY the RolePublic bundle, SRI-pinned to its hash so
		// the CSP hash-source authorises it without 'self'.
		assert.Contains(t, body, `<script src="`+assetPath+`" integrity="`+assetHash+`" crossorigin="anonymous">`)
		assert.Contains(t, body, "E2E Walkthrough")
		// INV-12: the served CSP pins the exact bundle hash and NEVER carries
		// 'unsafe-inline' — the public plane sets its CSP wholesale, so no hostile
		// upstream script-src can survive even in principle. This revision has no
		// addScript op, so the bundle hash is the WHOLE of script-src; a revision
		// that does carry one adds a sha256 per authored body and nothing else.
		csp := hdr.Get("Content-Security-Policy")
		require.NotEmpty(t, csp)
		assert.Contains(t, csp, assetHash, "CSP must pin the RolePublic bundle hash")
		// The hash pin is real only without 'self' in script-src (finding 4).
		assert.NotContains(t, csp, "script-src 'self'", "public script-src must drop 'self' so the hash gates")
		assert.NotContains(t, csp, "unsafe-inline", "public CSP must never allow unsafe-inline")
		assert.NotContains(t, csp, "unsafe-eval")
		assert.Equal(t, "DENY", hdr.Get("X-Frame-Options"))
		assert.Equal(t, "no-referrer", hdr.Get("Referrer-Policy"))
		assert.Equal(t, "nosniff", hdr.Get("X-Content-Type-Options"))
		// The token must NEVER appear in any response header (INV-9).
		for k, vals := range hdr {
			for _, v := range vals {
				assert.NotContains(t, v, token, "token leaked in header %s", k)
			}
		}
	})

	t.Run("variants_and_walkthrough_json_are_immutable_artifact", func(t *testing.T) {
		code, body, _ := p.get(t, "/s/"+token+"/variants.json")
		require.Equal(t, http.StatusOK, code)
		assert.Contains(t, body, `"set-e2e"`)
		assert.Contains(t, body, `"setText"`)

		code, body, _ = p.get(t, "/s/"+token+"/walkthrough.json")
		require.Equal(t, http.StatusOK, code)
		assert.Contains(t, body, `"wt-e2e"`)
		assert.Contains(t, body, `"Intro"`)
	})

	t.Run("public_asset_serves_only_public_bundle", func(t *testing.T) {
		code, body, _ := p.get(t, assetPath)
		require.Equal(t, http.StatusOK, code)
		// The public bundle exposes the allowlisted primitives and carries NONE
		// of the dev control surface.
		assert.Contains(t, body, "__walkthroughViewer")
		assert.Contains(t, body, "__variantEngine")
		// ...including the boot glue: without it the primitives above are inert
		// on a served share and the visitor gets a blank page.
		assert.Contains(t, body, "__agntPublicBoot")
		assert.Contains(t, body, "/walkthrough.json")
		assert.NotContains(t, body, "__devtool", "public bundle must not carry the dev control surface")
	})

	t.Run("feedback_accepted_and_owner_scoped_readback", func(t *testing.T) {
		code, _ := p.postFeedback(t, token, "application/json", `{"message":"loved it","rating":5}`)
		require.Equal(t, http.StatusAccepted, code)

		// Readable back via the OWNER-scoped read (project A owns the share)...
		rows, ok := ownerScopedFeedbackRead(p.store, p.feedback, e2eProjectA, id)
		require.True(t, ok, "owner project must read its share feedback")
		require.Len(t, rows, 1)
		assert.Contains(t, rows[0].Body, "loved it")

		// ...and NOT via another project (no cross-project leak, INV-1).
		_, ok = ownerScopedFeedbackRead(p.store, p.feedback, e2eProjectB, id)
		assert.False(t, ok, "foreign project must not read another project's feedback")
	})

	// ---- 5. ADVERSARIAL PROBES (the security core) ------------------------

	t.Run("dev_control_surface_unreachable", func(t *testing.T) {
		// Every dev-control path is forbidden on the public plane; none may 2xx and
		// none may echo a dev marker.
		devProbes := []string{
			"/__devtool_metrics",            // metrics WebSocket upgrade
			"/__devtool/exec",               // proxy exec bridge
			"/__devtool/ws",                 // dev WS channel
			"/__devtool/inject.js",          // dev bundle (non-public path)
			"/__devtool/inject.deadbeef.js", // forged hash → must NOT serve dev bundle
			"/__devtool_axe",                // axe audit asset
		}
		for _, probe := range devProbes {
			code, body, _ := p.get(t, probe)
			assert.Truef(t, code == http.StatusForbidden || code == http.StatusNotFound,
				"%s: got %d, want 403/404", probe, code)
			assert.NotContainsf(t, body, "__devtool", "%s leaked a dev marker", probe)
		}
		// A WS-style upgrade on the metrics path is equally dead.
		req, _ := http.NewRequest(http.MethodGet, p.srv.URL+"/__devtool_metrics", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("injection_payloads_rejected_or_inert_never_reflected", func(t *testing.T) {
		// A control-shaped feedback body (unknown field) is REJECTED at ingest
		// (deny-by-default schema), never stored.
		code, _ := p.postFeedback(t, token, "application/json", `{"onclick":"alert(1)"}`)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "unknown-field body must be 422")

		// A script/js-url payload inside the ALLOWED message field is valid data:
		// stored INERT (202), never executed, and — crucially — never reflected
		// into any served response.
		xss := `<script>alert(1)</script> javascript:evil() :has(a) url(https://evil)`
		code, _ = p.postFeedback(t, token, "application/json",
			`{"message":`+jsonString(xss)+`}`)
		require.Equal(t, http.StatusAccepted, code, "inert message body must be accepted")

		// The payload is stored raw (inert)...
		rows, ok := ownerScopedFeedbackRead(p.store, p.feedback, e2eProjectA, id)
		require.True(t, ok)
		found := false
		for _, r := range rows {
			if strings.Contains(r.Body, "<script>") {
				found = true
			}
		}
		assert.True(t, found, "payload must be stored inert/raw")

		// ...and NEVER reflected into the artifact, variants, or walkthrough.
		for _, sub := range []string{"", "/variants.json", "/walkthrough.json"} {
			_, body, _ := p.get(t, "/s/"+token+sub)
			assert.NotContains(t, body, "<script>alert(1)</script>",
				"feedback payload reflected into /s/{token}%s", sub)
		}

		// ---- INV-6 RETIRED (2026-07-27; spec §5, §5b, §6a) ----------------
		// Raw publisher-authored CSS/HTML is no longer refused at create. The
		// author of a variant is the PUBLISHER holding the dev session, not the
		// anonymous viewer, so the old code-shape string scan guarded the wrong
		// boundary; containment moved to the wholesale-replaced CSP (INV-11/
		// INV-12). §6a is explicit that the scan must NOT come back as
		// "defense in depth". So this subtest's contract shifts from the
		// "rejected" half of its name to the "inert / never reflected" half:
		// the payload is accepted, round-trips UNMODIFIED, and is contained.
		raw := e2eWalkthrough()
		raw.ID = "wt-raw"
		raw.VariantSet.ID = "set-raw"
		raw.VariantSet.Variants[0].Ops = []publish.Op{
			{Op: publish.OpApplyStyle, Selector: ".x", Props: map[string]string{"background": "url(https://evil/x)"}},
			{Op: publish.OpAddStyle, CSS: `@import url(https://evil/i); .y{behavior:url(#z)}`},
			{Op: publish.OpSetHTML, Selector: ".z", HTML: `<img src=x onerror="alert(1)"><a href="javascript:evil()">go</a>`},
		}
		_, rawToken, rerr := p.store.Create(raw, e2eProjectA)
		require.NoError(t, rerr, "INV-6 retired: raw publisher CSS/HTML must be accepted at create")

		// ...and the containment that replaced the scan is asserted here, on the
		// REAL served response. Without these the retirement would be a hole.
		rawCode, rawBody, rawHdr := p.get(t, "/s/"+rawToken)
		require.Equal(t, http.StatusOK, rawCode)
		rawCSP := rawHdr.Get("Content-Security-Policy")
		require.NotEmpty(t, rawCSP)
		// INV-12: the inline on* handler and the javascript: href in the raw
		// fragment are INERT because script-src carries HASH SOURCES ONLY (here,
		// with no addScript op in the revision, the bundle hash alone). Event-
		// handler attributes and javascript: URLs need 'unsafe-inline', which no
		// composition of writeHeaders can emit — so they stay inert even for a
		// revision whose script-src IS widened by an authored-body hash.
		assert.NotContains(t, rawCSP, "unsafe-inline", "raw-content share must still forbid unsafe-inline")
		assert.NotContains(t, rawCSP, "unsafe-eval")
		assert.NotContains(t, rawCSP, "script-src 'self'", "hash pin must not be diluted by 'self'")
		assert.Contains(t, rawCSP, assetHash, "CSP must still pin the RolePublic bundle hash")
		// The retired url()/@import token scan is replaced by fetch-boundary
		// directives, not by nothing: no third-party image beacon, no exfil.
		assert.Contains(t, rawCSP, "img-src 'self' data:", "img-src must stay 'self' data: (no https: beacon)")
		assert.Contains(t, rawCSP, "connect-src 'self'", "connect-src must stay 'self' (no exfil)")
		// INV-1: raw content is public-plane content only. The shell must still
		// load the RolePublic bundle ALONE, must not reference any dev control
		// endpoint, and must not inline the authored fragment into the shell
		// document — raw content reaches the DOM only through the CSP-governed
		// renderer, never as bytes parsed outside it.
		assert.Contains(t, rawBody, `<script src="`+assetPath+`" integrity="`+assetHash+`"`,
			"raw-content share must still load the RolePublic bundle alone")
		for _, devMarker := range []string{"__devtool_metrics", "__devtool/exec", "__devtool/ws", "__devtool."} {
			assert.NotContainsf(t, rawBody, devMarker,
				"raw-content share leaked dev control marker %q into the shell", devMarker)
		}
		assert.NotContains(t, rawBody, "onerror", "authored fragment must not be inlined into the shell document")
		assert.NotContains(t, rawBody, "url(https://evil/x)", "authored CSS must not be inlined into the shell document")

		// The op set round-trips UNMODIFIED — proving the validator neither
		// rejected nor silently stripped the authored content (§6a).
		_, rawVariants, _ := p.get(t, "/s/"+rawToken+"/variants.json")
		assert.Contains(t, rawVariants, "url(https://evil/x)", "authored CSS must round-trip unmodified")
		assert.Contains(t, rawVariants, "onerror", "authored HTML must round-trip unstripped (inert under CSP)")

		// ---- What the retirement did NOT retire ---------------------------
		// The surviving guards are still red-capable at the create boundary:
		// selector grammar (§5a), attribute allowlist (§6), https-only URLs
		// (§5/INV-13), size caps (§5), and the closed op vocabulary (§6).
		surviving := []struct {
			name string
			ops  []publish.Op
		}{
			{"selector grammar :has()", []publish.Op{
				{Op: publish.OpSetText, Selector: ":has(a)", Value: "x"}}},
			{"selector grammar sibling combinator", []publish.Op{
				{Op: publish.OpSetText, Selector: ".a ~ .b", Value: "x"}}},
			{"event-handler attribute", []publish.Op{
				{Op: publish.OpSetAttribute, Selector: ".x", Name: "onclick", Value: "alert(1)"}}},
			{"href via setAttribute", []publish.Op{
				{Op: publish.OpSetAttribute, Selector: ".x", Name: "href", Value: "https://ok/"}}},
			{"javascript: image url", []publish.Op{
				{Op: publish.OpSetImageSrc, Selector: ".x", URL: "javascript:evil()"}}},
			{"plaintext http image url", []publish.Op{
				{Op: publish.OpSetImageSrc, Selector: ".x", URL: "http://evil/x.png"}}},
			// addScript src is refused outright — not for its scheme, but because
			// publish-time fetch-and-inline does not exist, so a src body would
			// never be hashed into script-src and could never run. Only an inline
			// `code` body reaches the pinned-hash execution path.
			{"addScript src (unsupported at any scheme)", []publish.Op{
				{Op: publish.OpAddScript, Src: "http://evil/x.js"}}},
			{"oversize raw CSS", []publish.Op{
				{Op: publish.OpAddStyle, CSS: strings.Repeat("a", 4097)}}},
			{"oversize raw HTML", []publish.Op{
				{Op: publish.OpSetHTML, Selector: ".x", HTML: strings.Repeat("b", 8193)}}},
			{"unknown op type", []publish.Op{
				{Op: publish.OpType("evalScript"), Selector: ".x", Value: "alert(1)"}}},
		}
		for i, sc := range surviving {
			w := e2eWalkthrough()
			w.ID = fmt.Sprintf("wt-guard-%d", i)
			w.VariantSet.ID = fmt.Sprintf("set-guard-%d", i)
			w.VariantSet.Variants[0].Ops = sc.ops
			_, _, gerr := p.store.Create(w, e2eProjectA)
			assert.Errorf(t, gerr, "%s must still be rejected at create", sc.name)
		}
	})

	t.Run("path_traversal_serves_no_file", func(t *testing.T) {
		addr := p.srv.Listener.Addr().String()
		traversals := []string{
			"/s/" + token + "/../server.go",
			"/s/" + token + "/..%2fserver.go",
			"/s/" + token + "/%2e%2e/server.go",
			"//etc/passwd",
			"/s/" + token + "//variants.json",
		}
		for _, tr := range traversals {
			code := rawGet(t, addr, tr)
			assert.Equalf(t, http.StatusNotFound, code, "traversal %q must 404", tr)
		}
	})

	t.Run("token_no_existence_oracle", func(t *testing.T) {
		// An unknown token and a valid-but-wrong sub-route are both plain 404 with
		// an identical body: no oracle distinguishes "no such token" from anything.
		codeUnknown, bodyUnknown, _ := p.get(t, "/s/this-token-does-not-exist")
		require.Equal(t, http.StatusNotFound, codeUnknown)
		codeUnknown2, bodyUnknown2, _ := p.get(t, "/s/another-bogus-token")
		require.Equal(t, http.StatusNotFound, codeUnknown2)
		assert.Equal(t, bodyUnknown, bodyUnknown2, "404 bodies must be identical (no oracle)")
	})

	// ---- 3. REVOKE --------------------------------------------------------
	t.Run("revoke_kills_every_route", func(t *testing.T) {
		require.NoError(t, p.store.Revoke(id))
		for _, sub := range []string{"", "/variants.json", "/walkthrough.json"} {
			code, _, _ := p.get(t, "/s/"+token+sub)
			assert.Equalf(t, http.StatusNotFound, code, "revoked /s/{token}%s must 404", sub)
		}
		// No feedback accepted after revoke: the token no longer verifies, so the
		// route 404s before the sink is ever reached.
		code, _ := p.postFeedback(t, token, "application/json", `{"message":"late"}`)
		assert.Equal(t, http.StatusNotFound, code, "feedback on a revoked token must 404")
	})
}

// TestE2ERotate proves rotation: the old token dies immediately and the new token
// works, on its own fresh plane so the revoke journey above cannot interfere.
func TestE2ERotate(t *testing.T) {
	p := newE2EPlane(t)
	id, oldToken, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
	require.NoError(t, err)

	code, _, _ := p.get(t, "/s/"+oldToken)
	require.Equal(t, http.StatusOK, code, "old token works before rotate")

	newToken, err := p.store.Rotate(id)
	require.NoError(t, err)
	require.NotEqual(t, oldToken, newToken)

	// Old token: dead for read AND feedback.
	code, _, _ = p.get(t, "/s/"+oldToken)
	assert.Equal(t, http.StatusNotFound, code, "old token must 404 after rotate")
	fcode, _ := p.postFeedback(t, oldToken, "application/json", `{"message":"x"}`)
	assert.Equal(t, http.StatusNotFound, fcode, "old token feedback must 404 after rotate")

	// New token: fully live.
	code, body, _ := p.get(t, "/s/"+newToken)
	assert.Equal(t, http.StatusOK, code, "new token must serve after rotate")
	assert.Contains(t, body, "E2E Walkthrough")
	fcode, _ = p.postFeedback(t, newToken, "application/json", `{"message":"via new"}`)
	assert.Equal(t, http.StatusAccepted, fcode, "new token feedback must be accepted")
}

// TestE2ERestartPersistence proves durability: shares + feedback written through
// the live plane survive a simulated daemon restart (reconstruct the stores from
// the SAME dirs), with the token still verifying and the feedback intact.
func TestE2ERestartPersistence(t *testing.T) {
	p := newE2EPlane(t)
	id, token, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
	require.NoError(t, err)
	code, _ := p.postFeedback(t, token, "application/json", `{"message":"survive me"}`)
	require.Equal(t, http.StatusAccepted, code)

	// Simulate a daemon restart: brand-new store objects over the SAME dirs.
	store2, err := publish.New(p.storeDir, nil)
	require.NoError(t, err, "reload publish store from disk")
	feedback2, err := publish.NewFeedbackStore(p.fbDir, e2eLimits(), nil)
	require.NoError(t, err, "reload feedback store from disk")

	// The token still verifies against the reloaded store.
	rev, shareID, ok := store2.VerifyToken(token)
	require.True(t, ok, "token must verify after restart")
	assert.Equal(t, id, shareID)
	require.NotNil(t, rev)
	assert.Equal(t, "E2E Walkthrough", rev.Title)

	// The feedback survived and is readable via the owner-scoped read.
	rows, ok := ownerScopedFeedbackRead(store2, feedback2, e2eProjectA, id)
	require.True(t, ok)
	require.Len(t, rows, 1)
	assert.Contains(t, rows[0].Body, "survive me")
}

// TestE2ECrashWriteRecovery proves the Silent Failure Prohibition end-to-end: a
// crash mid-write (a truncated/partial store file, using files the LIVE path
// actually wrote) makes the reload FAIL LOUD with the store's corrupt sentinel,
// never a silent empty store.
func TestE2ECrashWriteRecovery(t *testing.T) {
	t.Run("share_store_truncated_record_fails_loud", func(t *testing.T) {
		p := newE2EPlane(t)
		id, _, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
		require.NoError(t, err)

		// Truncate the on-disk record the live create path wrote (crash mid-write).
		rec := filepath.Join(p.storeDir, id+".json")
		require.NoError(t, os.WriteFile(rec, []byte(`{"id":"x","token_ha`), 0600))

		_, rerr := publish.New(p.storeDir, nil)
		require.Error(t, rerr, "truncated record must not load silently")
		assert.ErrorIs(t, rerr, publish.ErrCorrupt)
	})

	t.Run("feedback_store_truncated_ring_fails_loud", func(t *testing.T) {
		p := newE2EPlane(t)
		_, token, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
		require.NoError(t, err)
		code, _ := p.postFeedback(t, token, "application/json", `{"message":"one"}`)
		require.Equal(t, http.StatusAccepted, code)

		// Truncate the feedback ring file the live POST path wrote.
		entries, err := os.ReadDir(p.fbDir)
		require.NoError(t, err)
		var ring string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".feedback.json") {
				ring = filepath.Join(p.fbDir, e.Name())
			}
		}
		require.NotEmpty(t, ring, "expected a feedback ring file on disk")
		require.NoError(t, os.WriteFile(ring, []byte(`{"share_id":"x","reco`), 0600))

		_, rerr := publish.NewFeedbackStore(p.fbDir, e2eLimits(), nil)
		require.Error(t, rerr, "truncated feedback ring must not load silently")
		assert.ErrorIs(t, rerr, publish.ErrFeedbackCorrupt)
	})
}

// TestE2EConcurrentFeedback is the concurrency/race gate: many anonymous viewers
// POST feedback at once against the live plane. The rate limiter holds (every
// request is either accepted or 429'd, never a crash), the durable store stays
// consistent, and accepted+dropped accounts for every attempt. Run under -race.
func TestE2EConcurrentFeedback(t *testing.T) {
	p := newE2EPlane(t)
	id, token, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
	require.NoError(t, err)

	const workers = 24
	const perWorker = 8
	var accepted, throttled int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				code, _ := p.postFeedback(t, token, "application/json",
					fmt.Sprintf(`{"message":"c-%d-%d"}`, w, i))
				mu.Lock()
				switch code {
				case http.StatusAccepted:
					accepted++
				case http.StatusTooManyRequests:
					throttled++
				default:
					t.Errorf("unexpected feedback status under load: %d", code)
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	total := int64(workers * perWorker)
	assert.Equal(t, total, accepted+throttled, "every POST must be accepted or throttled")
	assert.Equal(t, int(accepted), p.feedback.Count(id), "stored rows must equal accepted POSTs")
	assert.Equal(t, throttled, p.feedback.Dropped(), "dropped count must equal throttled POSTs")
	assert.Greater(t, accepted, int64(0), "some feedback must succeed under the configured limit")
}

// jsonString renders s as a JSON string literal (quotes + escaping) so an XSS
// payload can be embedded in a feedback body without hand-escaping.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
