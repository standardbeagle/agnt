package proxy

// public_routes.go is the network-facing security boundary of the walkthrough-
// publish epic (P7). It exposes ONLY the token-gated public routes of the P1
// endpoint matrix (§2b) and denies everything else by default. It is a DISTINCT
// http.Handler from the dev reverse proxy (server.go's mux): the dev control
// surface — metrics WebSocket, __devtool control paths, proxy exec, axe, the
// full instrumentation bundle — is simply NOT REGISTERED here, so it is
// structurally unreachable on the public plane rather than merely guarded
// (INV-1 / INV-2). The public plane carries no dev session scope: a share token
// maps to an immutable published revision, never to a SessionCode/Directory,
// so it can never satisfy resolveProjectScope (INV-1 / Deviations #3).
//
// Header policy (spec §4) is applied wholesale on every public response: the
// upstream Content-Security-Policy AND Content-Security-Policy-Report-Only are
// DELETED and agnt's own CSP is SET — never merged (INV-11 / INV-12). This is
// the opposite of the strip-merge stripFrameDenyHeaders path, which would
// preserve a hostile upstream's script-src 'unsafe-inline'.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/standardbeagle/agnt/internal/publish"
)

const (
	// sharePrefix is the public artifact route prefix; the token is the next
	// path segment (spec §2b — token in path, never query, so it does not leak
	// via Referer or proxy logs).
	sharePrefix = "/s/"
	// maxFeedbackBody caps a feedback POST body (spec §5: 4096 bytes). Excess is
	// rejected (413), never silently truncated.
	maxFeedbackBody = 4096
)

// PublicTokenVerifier is the constant-time token gate the public plane depends
// on. It is satisfied by *publishstore.Store (VerifyToken). Kept as a narrow
// interface so the boundary is testable without a real on-disk store and so the
// public handler cannot reach any control-plane method (create/rotate/revoke).
type PublicTokenVerifier interface {
	// VerifyToken constant-time verifies token and returns the immutable
	// published revision plus the viewer-safe share id. A revoked/unknown token
	// returns ok=false.
	VerifyToken(token string) (rev *publish.PublishedWalkthrough, shareID string, ok bool)
}

// FeedbackSink accepts a validated, size-capped, anonymous feedback body and
// hands it off for persistence. P7 owns the route-level guards (method,
// content-type, body cap); P8 fills the durable, rate-limited, retention-bounded
// sink. A nil sink makes the feedback route a SAFE stub: it still enforces every
// P7 guard and accepts the body, then drops it (no persistence yet).
//
// The signature carries the data the P8 sink needs WITHOUT reaching into the
// daemon: shareID + revisionID come from the already-verified share the handler
// holds (revoked/unknown tokens 404 in serveShare before Accept is ever called,
// so the sink only ever sees writes for a live share — INV-4). remoteAddr is the
// real connection peer (r.RemoteAddr) the sink keys its per-IP limiter on; the
// handler deliberately does NOT pass any client X-Forwarded-For value.
// *publish.FeedbackStore satisfies this interface.
type FeedbackSink interface {
	// Accept is handed the share id, the immutable revision id, the real remote
	// address, and the raw (already size-capped) feedback body. It must treat the
	// body as inert data, never a command (INV-7). A rate-limit rejection returns
	// publish.ErrFeedbackRateLimited (mapped to 429); an oversize/invalid body
	// returns the publish feedback error family (mapped to 413/422).
	Accept(shareID, revisionID, remoteAddr string, body []byte) error
}

// PublicHandler serves the anonymous-viewer public plane. It is deny-by-default:
// any route absent from the §2b matrix returns 404 (unknown) or 403 (a known
// dev-control surface). Safe for concurrent use — it holds only immutable
// references.
type PublicHandler struct {
	verifier PublicTokenVerifier
	feedback FeedbackSink // nil = safe accept-and-drop stub (P8 fills persistence)

	assetPath string // content-addressed RolePublic bundle path
	cspHash   string // "sha256-<b64>" of the RolePublic bundle, pinned in CSP
}

// NewPublicHandler builds the public plane over a token verifier and an optional
// feedback sink. A nil sink yields the safe stub described on FeedbackSink.
func NewPublicHandler(verifier PublicTokenVerifier, feedback FeedbackSink) *PublicHandler {
	return &PublicHandler{
		verifier:  verifier,
		feedback:  feedback,
		assetPath: PublicInstrumentationAssetPath(),
		cspHash:   PublicInstrumentationAssetCSPHash(),
	}
}

// responseKind selects the cache policy for a public response (spec §4).
type responseKind int

const (
	kindArtifact responseKind = iota // artifact/variants/walkthrough: revalidate always
	kindFeedback                     // feedback POST response: no-store
)

// ServeHTTP is the deny-by-default router. Order matters: path canonicalisation
// and the forbidden-control prefix are checked BEFORE any route match so a
// traversal or a dev-control probe can never reach a served handler.
func (h *PublicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path

	// Path-traversal / non-canonical defence (INV-2). net/http has already
	// percent-decoded r.URL.Path, so an encoded "%2e%2e%2f" arrives here as
	// "../"; reject anything that is not already its own cleaned form or that
	// contains a ".." segment. Blocks ../, encoded traversal, and //double
	// slashes that could confuse downstream matching.
	if p == "" || strings.Contains(p, "..") || p != path.Clean(p) {
		http.NotFound(w, r)
		return
	}

	// Forbidden dev-control surface: EVERY /__devtool* path is a control-plane
	// path and forbidden on the public plane (metrics WS, exec, axe, the dev
	// instrumentation bundle) — with the SOLE exception of the content-addressed
	// RolePublic asset. A known-but-forbidden control path is 403 (INV-1/INV-2);
	// it is served by handlePublicInstrumentationAsset which can ONLY ever emit
	// the public bundle (never the dev-bundle fallback).
	if strings.HasPrefix(p, "/__devtool") {
		if p == h.assetPath {
			handlePublicInstrumentationAsset(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Share routes: /s/{token}[/sub].
	if strings.HasPrefix(p, sharePrefix) {
		h.serveShare(w, r, p)
		return
	}

	// Deny by default: nothing else exists on the public plane.
	http.NotFound(w, r)
}

// serveShare handles the /s/{token} route family. The token is verified FIRST
// (constant-time, via the store); an invalid/revoked/unknown token yields 404
// for every sub-route so there is no existence oracle and revoke kills all
// routes at once (INV-4). Only after a valid token is the sub-route + method
// dispatched — so a wrong-method probe can never distinguish a valid token from
// an invalid one during enumeration (every guess is 404 until the 2^256 token
// is actually held).
func (h *PublicHandler) serveShare(w http.ResponseWriter, r *http.Request, p string) {
	rest := strings.TrimPrefix(p, sharePrefix)
	token := rest
	sub := ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		token = rest[:i]
		sub = rest[i:] // includes leading '/'
	}
	if token == "" {
		http.NotFound(w, r)
		return
	}

	rev, shareID, ok := h.verifier.VerifyToken(token)
	if !ok {
		// Revoked / rotated / unknown: no oracle, no timing leak beyond the
		// constant-time compare inside VerifyToken (INV-3/INV-4).
		http.NotFound(w, r)
		return
	}

	switch sub {
	case "":
		h.serveArtifact(w, r, rev)
	case "/variants.json":
		h.serveVariants(w, r, rev)
	case "/walkthrough.json":
		h.serveWalkthrough(w, r, rev)
	case "/feedback":
		h.serveFeedback(w, r, rev, shareID)
	default:
		// A valid token but an unknown sub-route: still deny-by-default.
		http.NotFound(w, r)
	}
}

// serveArtifact serves the artifact HTML shell that loads ONLY the RolePublic
// bundle (spec §2b). The published walkthrough is a self-contained artifact
// (steps + variant set); it does NOT proxy a live upstream, so the public plane
// has no SSRF surface and copies NO upstream headers — the response headers are
// built wholesale here (INV-11/INV-12). The bundle derives the share token from
// window.location, so no secret is inlined into the shell.
func (h *PublicHandler) serveArtifact(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	nonce := newNonce()
	h.writeHeaders(w.Header(), kindArtifact, nonce)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	title := "Walkthrough"
	if rev != nil && rev.Title != "" {
		title = rev.Title
	}
	// html.EscapeString neutralises any markup smuggled into the (already
	// validated) title before it lands in the HTML shell — defence in depth.
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title>")
	b.WriteString("<style nonce=\"" + nonce + "\">html,body{margin:0;height:100%}</style>")
	b.WriteString("<script src=\"" + h.assetPath + "\"></script>")
	b.WriteString("</head><body></body></html>")
	body := b.String()

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	io.WriteString(w, body)
}

// serveVariants serves the immutable published variant-set snapshot as JSON.
func (h *PublicHandler) serveVariants(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	var vs any
	if rev != nil && rev.VariantSet != nil {
		vs = rev.VariantSet
	} else {
		// No bound variant set: serve an empty, well-formed set rather than
		// "null" so the cycler always parses a stable shape.
		vs = publish.VariantSet{Version: publish.SchemaV1, Variants: []publish.Variant{}}
	}
	h.writeJSON(w, r, vs)
}

// serveWalkthrough serves the immutable published walkthrough script as JSON.
func (h *PublicHandler) serveWalkthrough(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	h.writeJSON(w, r, rev)
}

// serveFeedback is the anonymous feedback WRITE route (spec §2b/§7). P7 enforces
// the route-level guards: POST only, application/json only, 4096-byte cap. The
// body is handed to the sink (or dropped by the safe stub). Persistence, rate
// limiting, and retention are P8. The body is never read back into the public
// artifact (INV-7 — no reflection).
func (h *PublicHandler) serveFeedback(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough, shareID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	// Content-type allowlist: application/json (optionally with parameters like
	// "; charset=utf-8"). Anything else is 415 — no form posts, no octet-stream.
	ct := r.Header.Get("Content-Type")
	if mediaType := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]); !strings.EqualFold(mediaType, "application/json") {
		h.writeHeaders(w.Header(), kindFeedback, "")
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	// Body cap: MaxBytesReader rejects an over-cap body instead of buffering it.
	r.Body = http.MaxBytesReader(w, r.Body, maxFeedbackBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeHeaders(w.Header(), kindFeedback, "")
		http.Error(w, "feedback too large", http.StatusRequestEntityTooLarge)
		return
	}
	if h.feedback != nil {
		// revisionID is the immutable revision digest threaded from the verified
		// share (no daemon reach). r.RemoteAddr is the REAL peer the limiter keys
		// on — never a client X-Forwarded-For header (INV-7 anti-spoof).
		revisionID := revisionDigest(rev)
		if err := h.feedback.Accept(shareID, revisionID, r.RemoteAddr, body); err != nil {
			h.writeHeaders(w.Header(), kindFeedback, "")
			http.Error(w, feedbackErrorMessage(err), feedbackErrorStatus(err))
			return
		}
	}
	h.writeHeaders(w.Header(), kindFeedback, "")
	w.WriteHeader(http.StatusAccepted)
}

// revisionDigest returns the stable identity of the immutable published revision
// this feedback is keyed to. A digest error (should not happen for an
// already-validated revision) yields an empty key rather than failing the write.
func revisionDigest(rev *publish.PublishedWalkthrough) string {
	if rev == nil {
		return ""
	}
	d, err := publish.Digest(rev)
	if err != nil {
		return ""
	}
	return d
}

// feedbackErrorStatus maps a sink error to its HTTP status: rate-limit → 429,
// oversize → 413, everything else (malformed/invalid) → 422. Deny-by-default:
// an unrecognized error is treated as a client validation error, never a 5xx
// that would leak an internal detail.
func feedbackErrorStatus(err error) int {
	switch {
	case errors.Is(err, publish.ErrFeedbackRateLimited):
		return http.StatusTooManyRequests
	case errors.Is(err, publish.ErrFeedbackTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusUnprocessableEntity
	}
}

// feedbackErrorMessage returns a terse, non-reflecting status text. It never
// echoes the request body (INV-7 — no reflection surface).
func feedbackErrorMessage(err error) string {
	switch {
	case errors.Is(err, publish.ErrFeedbackRateLimited):
		return "rate limited"
	case errors.Is(err, publish.ErrFeedbackTooLarge):
		return "feedback too large"
	default:
		return "feedback rejected"
	}
}

// writeJSON encodes v with the public header policy applied. Artifact-kind
// caching (revalidate) so a revoke takes effect immediately (INV-4).
func (h *PublicHandler) writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	h.writeHeaders(w.Header(), kindArtifact, "")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(data)
}

// writeHeaders applies the wholesale public-plane header policy (spec §4). This
// is the INV-11/INV-12 chokepoint: the upstream Content-Security-Policy AND
// Content-Security-Policy-Report-Only are DELETED and agnt's own CSP is SET —
// never merged. The public plane copies no upstream headers, but the Del is kept
// unconditionally as defence in depth: if any future upstream-copy path ever
// seeds a hostile CSP into this header set, it dies here. nonce, when non-empty,
// authorises the shell's single inline <style>.
func (h *PublicHandler) writeHeaders(hdr http.Header, kind responseKind, nonce string) {
	// INV-12: wholesale delete of any upstream CSP, then set agnt's. NEVER the
	// stripFrameDenyHeaders strip-merge path (which preserves upstream script-src).
	hdr.Del("Content-Security-Policy")
	hdr.Del("Content-Security-Policy-Report-Only")
	styleSrc := "'self'"
	if nonce != "" {
		styleSrc = "'self' 'nonce-" + nonce + "'"
	}
	hdr.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' '" + h.cspHash + "'",
		"style-src " + styleSrc,
		"img-src 'self' data:",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"object-src 'none'",
	}, "; "))

	// The artifact is the top document; it is not embeddable elsewhere.
	hdr.Set("X-Frame-Options", "DENY")
	// Token in path must never leak to upstream subresource hosts (INV-9).
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("X-Content-Type-Options", "nosniff")
	// No sessions / auth cookies on the public plane (spec §4). Strip any that a
	// copy path might have seeded.
	hdr.Del("Set-Cookie")
	// No credentialed CORS: deliberately do NOT set Access-Control-Allow-Origin.
	hdr.Del("Access-Control-Allow-Origin")
	hdr.Del("Access-Control-Allow-Credentials")

	switch kind {
	case kindFeedback:
		hdr.Set("Cache-Control", "no-store")
	default: // kindArtifact
		// Revoke must take effect immediately — no stale artifact past revoke.
		hdr.Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
}

// methodNotAllowed writes a 405 with an Allow header for a known route hit with
// the wrong method. It appears only once a VALID token is held, so it is not an
// enumeration oracle.
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// newNonce returns a fresh base64 CSP nonce for a single response's inline
// <style>. CSPRNG so it is unpredictable per response.
func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fail closed: an empty nonce means the CSP omits the nonce source and
		// the inline style is refused by the browser, rather than emitting a
		// predictable nonce.
		return ""
	}
	return base64.RawStdEncoding.EncodeToString(b)
}
