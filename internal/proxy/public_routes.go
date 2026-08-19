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
//
// The artifact route has two shapes. A share whose revision names a live upstream
// origin is served by proxying that origin through the INV-13 guard
// (guardedUpstreamFetcher) and splicing the RolePublic bundle into the fetched
// document; a share naming none is served the self-contained shell and performs
// no outbound fetch at all. The upstream is untrusted in both directions —
// nothing it returns is trusted, and nothing about the viewer (cookies, Referer,
// the share token) is forwarded to it.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/publish"
)

const (
	// sharePrefix is the public artifact route prefix; the token is the next
	// path segment (spec §2b — token in path, never query, so it does not leak
	// via Referer or proxy logs).
	sharePrefix = "/s/"
	// defaultMaxFeedbackBody is the spec §5 default feedback POST cap (4096 bytes)
	// used when the handler is constructed without a configured MaxBodyBytes.
	// Excess is rejected (413), never silently truncated.
	defaultMaxFeedbackBody = 4096

	// maxPublicUpstreamBytes caps the upstream document the public plane will
	// buffer. Injection is a whole-body operation, so the buffer is the bound.
	// It is far tighter than the dev plane's maxInjectBodyBytes (16 MiB) because
	// this path is driven by ANONYMOUS requests: the dev cap protects a
	// developer's own proxy from their own app, whereas this one is a remote
	// memory-amplification bound. An over-cap document is REFUSED, never
	// truncated — a half document with our bundle spliced into it is a broken
	// demo presented as a real one.
	maxPublicUpstreamBytes = 4 << 20

	// maxUpstreamRedirects caps the redirect chain. §4a requires the origin guard
	// to re-run on every hop, so an unbounded chain is an unbounded number of
	// guarded fetches; exceeding the cap FAILS CLOSED (refuse), never "serve the
	// last thing we got".
	maxUpstreamRedirects = 5

	// upstreamFetchTimeout bounds the whole fetch, redirect chain included. A
	// public request must not be able to pin a daemon goroutine on a slow or
	// black-holing origin (see .claude/rules/lessons-liveness-probes.md).
	upstreamFetchTimeout = 15 * time.Second

	// upstreamDialTimeout bounds one TCP connect within that budget.
	upstreamDialTimeout = 5 * time.Second
)

// PublicTokenVerifier is the constant-time token gate the public plane depends
// on. It is satisfied by *publish.Store (VerifyToken). Kept as a narrow
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
	// Accept is handed the share id, the immutable revision CONTENT digest, the
	// real remote address, and the raw (already size-capped) feedback body. The
	// revision parameter is a publish.RevisionDigest (content identity), never a
	// minted Share.RevisionID (a publish-event id) — the distinct type stops that
	// cross at compile time. It must treat the body as inert data, never a command
	// (INV-7). A rate-limit rejection returns publish.ErrFeedbackRateLimited
	// (mapped to 429); an oversize/invalid body returns the publish feedback error
	// family (mapped to 413/422).
	Accept(shareID string, revisionDigest publish.RevisionDigest, remoteAddr string, body []byte) error
}

// upstreamDocFetcher fetches the live upstream document a published share is a
// demo of. It is a narrow interface so the public handler can be exercised
// without a network, and so the ONE production implementation
// (guardedUpstreamFetcher) is the only thing that ever opens a socket.
type upstreamDocFetcher interface {
	// fetchDocument returns the upstream HTML for rawURL, or a loud error. It
	// must never return partial bytes alongside an error, and must never fall
	// back to an unguarded fetch.
	fetchDocument(ctx context.Context, rawURL string) ([]byte, error)

	// fetchSubresource returns one allowlisted subresource body plus its
	// canonical media type, under the SAME guard obligations as fetchDocument
	// (INV-16): origin and every redirect hop re-checked, fail-closed cap,
	// pin-and-dial. A body whose media type is not on the INV-17 allowlist is an
	// error, never a relayed body.
	fetchSubresource(ctx context.Context, rawURL string) ([]byte, string, error)
}

// PublicHandler serves the anonymous-viewer public plane. It is deny-by-default:
// any route absent from the §2b matrix returns 404 (unknown) or 403 (a known
// dev-control surface). Safe for concurrent use — it holds only immutable
// references.
type PublicHandler struct {
	verifier PublicTokenVerifier
	feedback FeedbackSink // nil = safe accept-and-drop stub (P8 fills persistence)

	maxFeedbackBody int64 // configured feedback POST cap (config.FeedbackConfig.MaxBodyBytes)

	assetPath string // content-addressed RolePublic bundle path
	cspHash   string // "sha256-<b64>" of the RolePublic bundle; CSP script-src hash + <script> SRI integrity

	// upstream fetches the live origin for a share that names one. Never nil in
	// production (NewPublicHandler installs the guarded fetcher); a nil value
	// refuses every upstream-bearing share rather than fetching unguarded.
	upstream upstreamDocFetcher

	// artifactLimiter throttles inbound artifact GETs per (share, client IP). It
	// bounds a single client flooding one share. Never nil (NewPublicHandler
	// installs a default; WithRateLimits overrides for config/tests).
	artifactLimiter *publish.RateLimiter

	// outboundLimiter throttles guarded upstream fetches per upstream ORIGIN,
	// across every share that names it. This is the amplification bound the
	// inbound cap cannot provide: N distinct shares pointed at one origin would
	// otherwise turn an anonymous GET flood into an outbound flood at a third
	// party (INV-13 bounds which origins may be fetched, not how often). Never
	// nil in production.
	outboundLimiter *publish.RateLimiter

	// signer binds every subresource reference to (share, url, depth) so the
	// subresource route serves only references THIS daemon minted from a guarded
	// fetch of that share's publisher-named upstream (INV-16). A nil signer fails
	// closed in both directions: no reference is rewritten and the route refuses
	// everything, so the plane degrades to the document-only behaviour rather
	// than becoming an unbound relay.
	signer *publish.SubresourceSigner
}

// NewPublicHandler builds the public plane over a token verifier and an optional
// feedback sink. A nil sink yields the safe stub described on FeedbackSink.
// maxBodyBytes is the configured feedback POST cap (config.FeedbackConfig
// .MaxBodyBytes); a non-positive value falls back to the spec §5 default so an
// unconfigured handler still enforces the guard.
func NewPublicHandler(verifier PublicTokenVerifier, feedback FeedbackSink, maxBodyBytes int) *PublicHandler {
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxFeedbackBody
	}
	// Default rate limiters come from config.DefaultPublicPlaneConfig() so the
	// proxy-side default cannot drift from the config default (a single source of
	// truth for the numbers). The daemon overrides these with operator-configured
	// limiters via WithRateLimits; a direct caller (a test, `agnt publish serve`)
	// gets the house defaults, always on.
	ppc := config.DefaultPublicPlaneConfig()
	// A signer that cannot be generated leaves the subresource route dead rather
	// than unbound: rewriting stops, /s/{token}/sub refuses everything, and the
	// plane behaves exactly as it did before the route existed. Loud, because a
	// silently document-only plane looks like a rendering bug.
	signer, err := publish.NewSubresourceSigner()
	if err != nil {
		debug.Log("publish", "public plane: subresource signing key unavailable (%v) — the subresource route is disabled; proxied demos will render without upstream CSS/images", err)
		signer = nil
	}
	return &PublicHandler{
		verifier:        verifier,
		feedback:        feedback,
		maxFeedbackBody: int64(maxBodyBytes),
		assetPath:       PublicInstrumentationAssetPath(),
		cspHash:         PublicInstrumentationAssetCSPHash(),
		upstream:        &guardedUpstreamFetcher{},
		artifactLimiter: publish.NewRateLimiter(ppc.ArtifactRatePerMinute, ppc.ArtifactBurst, nil),
		outboundLimiter: publish.NewRateLimiter(ppc.OutboundRatePerMinute, ppc.OutboundBurst, nil),
		signer:          signer,
	}
}

// WithRateLimits overrides the artifact and outbound rate limiters. It is the
// seam the daemon uses to install operator-configured limiters (config drives
// behaviour, not just parses) and that rate tests use to inject a deterministic
// clock. A nil argument leaves that limiter as the default. Returns h so it can
// be chained onto NewPublicHandler.
func (h *PublicHandler) WithRateLimits(artifact, outbound *publish.RateLimiter) *PublicHandler {
	if artifact != nil {
		h.artifactLimiter = artifact
	}
	if outbound != nil {
		h.outboundLimiter = outbound
	}
	return h
}

// UpstreamSeam carries the test-only transport injection for the guarded
// upstream fetcher, letting a caller OUTSIDE internal/proxy (a serve-level
// integration test) drive the guarded DOCUMENT route end to end. Its fields are
// exactly the seams already defined on guardedUpstreamFetcher; the public wrapper
// only makes them reachable across the package boundary.
//
// It OBSERVES the guard rather than disabling it (publish-security-review-lessons
// §7): a test supplies a Resolve answering with a genuinely public address and a
// Dial that asserts it was handed exactly that address before redirecting to a
// local listener. The https-only deny-list, the pin-and-dial, and the per-hop
// re-check all run their real logic — nothing is stubbed out. A nil field falls
// back to the production resolver/dialer/tls, so a zero UpstreamSeam is itself
// the production fetcher.
type UpstreamSeam struct {
	Resolve   publish.Resolver
	Dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	TLSConfig *tls.Config
}

// WithUpstreamSeam installs a guarded upstream fetcher wired through the given
// test seam. It mirrors WithRateLimits: an opt-in override, chainable onto
// NewPublicHandler, used only by tests. Production never calls it, so an
// unconfigured handler keeps the default &guardedUpstreamFetcher{} and its
// behaviour byte-for-byte. Returns h so it can be chained.
func (h *PublicHandler) WithUpstreamSeam(seam UpstreamSeam) *PublicHandler {
	h.upstream = &guardedUpstreamFetcher{
		resolve:   seam.Resolve,
		dial:      seam.Dial,
		tlsConfig: seam.TLSConfig,
	}
	return h
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
		h.serveArtifact(w, r, rev, shareID, token)
	case subresourceSubPath:
		h.serveSubresource(w, r, rev, shareID)
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

// serveArtifact is the /s/{token} document route. It branches on whether the
// published revision names a live upstream origin (§0, §4a):
//
//   - Upstream named  → serveProxiedArtifact: fetch that origin through the
//     INV-13 guard and serve it with the RolePublic bundle injected.
//   - No upstream     → serveSelfContainedArtifact: the shell, unchanged.
//
// Both paths build their response headers wholesale here (INV-11/INV-12) and
// neither copies an upstream header, so a hostile origin's CSP, cookies, or CORS
// grants cannot reach a viewer. The token was already verified in serveShare, so
// a revoked share 404s before either branch — and therefore before any outbound
// fetch — which is what makes revoke atomic for the proxied route too (INV-4).
func (h *PublicHandler) serveArtifact(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough, shareID, token string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	// Inbound rate cap per (share, client IP). The token was already verified in
	// serveShare, so a 429 here reveals nothing the caller does not already hold
	// (INV-9 / Secret Hygiene): the message is a constant "rate limited", carries
	// artifact-kind headers, and never names the share or an internal id — the
	// same shape as the feedback route's 429.
	if h.artifactLimiter != nil && !h.artifactLimiter.Allow(publish.ShareIPKey(shareID, r.RemoteAddr)) {
		h.writeHeaders(w.Header(), kindArtifact, "")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	if rev != nil && rev.Upstream != nil && rev.Upstream.URL != "" {
		h.serveProxiedArtifact(w, r, rev, shareID, token)
		return
	}
	h.serveSelfContainedArtifact(w, r, rev)
}

// serveProxiedArtifact serves a share whose revision names a live upstream: the
// upstream document IS the artifact document, with the RolePublic bundle spliced
// in so the walkthrough player and the variant ops run over the real page.
//
// Security posture, all of it load-bearing:
//
//   - Every outbound fetch — origin and every redirect hop — passes the INV-13
//     guard inside guardedUpstreamFetcher. There is no unguarded path.
//   - The request to the upstream is built from nothing but the published URL.
//     No viewer header is forwarded: no Cookie, no Referer (which would carry
//     the share token — INV-9), no X-Forwarded-*. The public plane holds no
//     session scope to forward either (INV-1).
//   - Response headers are composed wholesale by writeHeaders; not one upstream
//     header is copied. The upstream CSP is deleted and ours set (INV-11/INV-12),
//     never merged through stripFrameDenyHeaders.
//   - A refusal is loud (502) and never falls back to the self-contained shell:
//     a share that names an upstream is a demo OF that upstream, and serving a
//     different page under the same URL is a silent failure, not degradation.
//
// The document's CSS, image, and font references are rewritten to the guarded
// subresource route (serveSubresource) so they load from 'self'. JS references
// are NOT — script-src is hash-only (INV-12) and proxied script would be bytes
// nothing can execute. This is also the ONE response shape whose style-src
// carries 'unsafe-inline' (INV-18, see proxiedUpstreamStyleSrc): without it the
// upstream's own inline <style> blocks and style= attributes are refused and the
// page renders broken no matter what the subresource route serves.
func (h *PublicHandler) serveProxiedArtifact(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough, shareID, token string) {
	if h.upstream == nil {
		// Fail closed. A missing fetcher must never degrade into an unguarded
		// fetch or into the self-contained shell.
		h.refuseUpstream(w, fmt.Errorf("no upstream fetcher configured"))
		return
	}
	// Per-origin outbound cap: bound the guarded fetch rate to this upstream
	// origin ACROSS all shares, so a flood spread over many distinct shares that
	// name the same origin cannot amplify into unbounded outbound traffic at a
	// third party. Refused with a generic 503 that does not distinguish a rate
	// refusal from any other upstream failure (no oracle for the viewer).
	if h.outboundLimiter != nil && !h.outboundLimiter.Allow(upstreamOriginKey(rev.Upstream.URL)) {
		h.refuseUpstreamRate(w, rev.Upstream.URL)
		return
	}
	body, err := h.upstream.fetchDocument(r.Context(), rev.Upstream.URL)
	if err != nil {
		h.refuseUpstream(w, err)
		return
	}

	// No nonce: this response emits no inline <style> of OURS (the reset in the
	// self-contained shell would restyle someone else's page). The style-src
	// widening is for the UPSTREAM's inline style — a nonce cannot serve that,
	// because the upstream's own <style> blocks carry no nonce attribute and we
	// do not rewrite them (spec §3.2/§3.3 reject both hashing and rewriting).
	// This is the only call site of writeHeadersStyle (INV-18).
	h.writeHeadersStyle(w.Header(), kindArtifact, proxiedUpstreamStyleSrc, authoredScriptCSPHashes(rev)...)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Bind every admitted subresource reference to this share before the bundle
	// is spliced in. The rewriter reads only bytes the guard already fetched, so
	// the route it points at can serve only references this daemon minted
	// (INV-16); with no signer it is a no-op and the document is served exactly
	// as it was before the route existed.
	body = rewriteUpstreamDocumentRefs(body, rev.Upstream.URL, token, shareID, h.signer)

	doc := injectPublicBundle(body, h.assetPath, h.cspHash)
	w.Header().Set("Content-Length", strconv.Itoa(len(doc)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(doc)
}

// serveSubresource is the /s/{token}/sub route: it serves ONE stylesheet, image,
// or font that the daemon itself found in a guarded fetch of this share's
// publisher-named upstream (design spec §4a, INV-16..19).
//
// It is not an open relay, and every clause below is what makes that true:
//
//   - The reference must carry a MAC this daemon minted for THIS share at THIS
//     depth. A viewer-composed URL — forged MAC, another share's reference, an
//     altered target, a depth downgrade — is refused BEFORE any socket opens.
//   - The fetch runs the same guardedUpstreamFetcher obligations as the
//     document: CheckUpstreamOrigin on the origin and every redirect hop,
//     fail-closed redirect cap, pin-and-dial, no viewer header forwarded.
//   - The served media type comes from a closed allowlist (INV-17). HTML, JS,
//     and SVG refuse loudly; nothing here can reach a script-executing context,
//     and script-src stays hash-only on this response as on every other.
//   - Both rate caps apply: the inbound (share, IP) bucket the artifact route
//     already uses, and the per-origin outbound bucket that bounds amplification.
//   - A revoked token 404s in serveShare before this function runs, so revoke
//     kills every subresource URL atomically with the artifact (INV-4).
//
// A share that names no upstream has no subresources: the route does not exist
// for it, rather than existing with nothing to serve.
func (h *PublicHandler) serveSubresource(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough, shareID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if rev == nil || rev.Upstream == nil || rev.Upstream.URL == "" {
		http.NotFound(w, r)
		return
	}
	// Same inbound bucket as the artifact GET: one page load is one document
	// plus N subresources, so they are one client's budget against one share.
	if h.artifactLimiter != nil && !h.artifactLimiter.Allow(publish.ShareIPKey(shareID, r.RemoteAddr)) {
		h.writeHeaders(w.Header(), kindArtifact, "")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	q := r.URL.Query()
	rawURL := q.Get("u")
	depth, derr := strconv.Atoi(q.Get("d"))
	// Deny-by-default on the reference itself. 404 (not 403) so the route is no
	// oracle: an unbound reference is indistinguishable from a route that does
	// not exist. No fetch, no dial, no log of the viewer's chosen URL beyond the
	// operator-facing debug line.
	if derr != nil || depth < 1 || depth > maxSubresourceDepth ||
		h.signer == nil || !h.signer.Verify(shareID, rawURL, depth, q.Get("sig")) {
		debug.Log("publish", "public plane: refused an unbound subresource reference (depth %q) — no fetch was made", q.Get("d"))
		http.NotFound(w, r)
		return
	}

	// Per-origin outbound cap, shared with the document fetch: one origin, one
	// budget, so a subresource flood cannot exceed what INV-13 permits per origin.
	if h.outboundLimiter != nil && !h.outboundLimiter.Allow(upstreamOriginKey(rawURL)) {
		h.refuseUpstreamRate(w, rawURL)
		return
	}

	body, mediaType, err := h.upstreamFetcher().fetchSubresource(r.Context(), rawURL)
	if err != nil {
		h.refuseUpstream(w, err)
		return
	}

	// A stylesheet's own url()/@import references are rewritten one level deeper,
	// bounded by the nesting cap. Anything else is served byte-for-byte.
	if mediaType == "text/css" {
		body = rewriteUpstreamCSSRefs(body, rawURL, shareTokenFromPath(r.URL.Path), shareID, h.signer, depth)
	}

	h.writeHeaders(w.Header(), kindArtifact, "")
	w.Header().Set("Content-Type", subresourceContentType(mediaType))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body)
}

// upstreamFetcher returns the configured fetcher, or a fetcher that refuses
// everything when none is installed. It never falls back to an unguarded fetch.
func (h *PublicHandler) upstreamFetcher() upstreamDocFetcher {
	if h.upstream == nil {
		return refusingFetcher{}
	}
	return h.upstream
}

// refusingFetcher is the fail-closed stand-in for a missing fetcher.
type refusingFetcher struct{}

func (refusingFetcher) fetchDocument(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("no upstream fetcher configured")
}

func (refusingFetcher) fetchSubresource(context.Context, string) ([]byte, string, error) {
	return nil, "", fmt.Errorf("no upstream fetcher configured")
}

// shareTokenFromPath recovers the token from an already-matched /s/{token}/sub
// path so a served stylesheet can mint references under the same token the
// viewer is holding. The path was matched by serveShare, so the prefix is known
// to be present; an unexpected shape yields "" and the rewriter then declines to
// rewrite rather than emitting a broken reference.
func shareTokenFromPath(p string) string {
	rest := strings.TrimPrefix(p, sharePrefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return ""
}

// subresourceContentType returns the header value for an allowlisted media type.
// It is composed from the ALLOWLISTED type, never echoed from the upstream
// header, so no upstream parameter (a bogus charset, a smuggled boundary) is
// relayed. text/css gets an explicit charset so a stylesheet is not decoded per
// the browser's locale default.
func subresourceContentType(mediaType string) string {
	if mediaType == "text/css" {
		return "text/css; charset=utf-8"
	}
	return mediaType
}

// refuseUpstream emits the loud 502 for an upstream that could not be served,
// with the public header policy still applied.
//
// The message to the viewer is deliberately constant: err can quote the guard's
// verdict (which resolved address was denied, which hop failed), and that is
// daemon-network topology, not viewer business. The detail goes to the debug log,
// which never sees the share token — err is built only from the published
// upstream URL, and the token lives in the request path this function is not
// given (INV-9).
func (h *PublicHandler) refuseUpstream(w http.ResponseWriter, err error) {
	debug.Log("publish", "public plane refused upstream: %v", err)
	h.writeHeaders(w.Header(), kindArtifact, "")
	http.Error(w, "upstream unavailable", http.StatusBadGateway)
}

// refuseUpstreamRate emits a 503 when the per-origin outbound budget is spent.
// The viewer-facing message is deliberately generic ("upstream unavailable") so
// a rate refusal is indistinguishable from any other upstream failure — it is
// not an oracle for how many other viewers are hitting the same origin. The
// origin (not the share token, which this function is not given) goes to the
// debug log for the operator. 503 rather than 429 because the limit being hit is
// the daemon's outbound budget to a third party, not this viewer's own rate.
func (h *PublicHandler) refuseUpstreamRate(w http.ResponseWriter, upstreamURL string) {
	debug.Log("publish", "public plane outbound rate cap hit for origin %s", upstreamOriginKey(upstreamURL))
	h.writeHeaders(w.Header(), kindArtifact, "")
	http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
}

// upstreamOriginKey reduces an upstream URL to its origin (scheme://host[:port],
// host lower-cased) so every share naming the same origin shares one outbound
// bucket. A URL that will not parse degrades to itself as the key rather than
// collapsing all unparseable URLs into one bucket (CheckUpstreamOrigin rejects
// such a URL at fetch time anyway).
func upstreamOriginKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}

// serveSelfContainedArtifact serves the artifact HTML shell that loads ONLY the
// RolePublic bundle (spec §2b) for a share that names no live upstream. The
// published walkthrough is then a self-contained artifact (steps + variant set),
// so this path performs no outbound fetch at all and copies NO upstream headers —
// the response headers are built wholesale here (INV-11/INV-12). The bundle
// derives the share token from window.location, so no secret is inlined into the
// shell.
func (h *PublicHandler) serveSelfContainedArtifact(w http.ResponseWriter, r *http.Request, rev *publish.PublishedWalkthrough) {
	nonce := newNonce()
	// The artifact document is where the renderer appends authored script, so
	// this is the only response whose script-src is widened — by exactly the
	// hashes of THIS revision's authored bodies (INV-12).
	h.writeHeaders(w.Header(), kindArtifact, nonce, authoredScriptCSPHashes(rev)...)
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
	// Pin the exact bundle bytes with Subresource Integrity. The SRI hash is
	// what makes the CSP script-src hash-source real for this EXTERNAL script:
	// a CSP 'sha256-…' source only authorises an external <script src> when the
	// element carries a matching integrity attribute (CSP L3 external-hash
	// matching). Together with dropping 'self' from script-src (writeHeaders),
	// the public plane loads THIS bundle and nothing else. crossorigin makes the
	// fetch integrity-checkable; same-origin requests are not CORS-blocked.
	b.WriteString("<script src=\"" + h.assetPath + "\" integrity=\"" + h.cspHash + "\" crossorigin=\"anonymous\"></script>")
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
	r.Body = http.MaxBytesReader(w, r.Body, h.maxFeedbackBody)
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
		revDigest := revisionDigest(rev)
		if err := h.feedback.Accept(shareID, revDigest, r.RemoteAddr, body); err != nil {
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
func revisionDigest(rev *publish.PublishedWalkthrough) publish.RevisionDigest {
	if rev == nil {
		return ""
	}
	d, err := publish.Digest(rev)
	if err != nil {
		return ""
	}
	return publish.RevisionDigest(d)
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
// authoredScriptCSPHashes enumerates one CSP sha256 source per authored script
// body carried by this revision's variant set (§6a addScript) — the exact set
// INV-12 widens script-src by. It is derived from the immutable published
// revision rather than recomputed from any live input, so the "pinned at publish
// time" property holds: the revision's bytes are what a token resolves to, and
// re-publishing an edited revision yields a different set for free.
//
// A body is hashed exactly as the renderer will assign it (script.textContent =
// op.code, variant-engine.js), deduped so a repeated body costs one source, and
// order-stable so the served header is deterministic. An op still carrying Src
// at serve time contributes NO hash: src is a publish-time fetch input, so an
// unresolved one means the pipeline did not run, and the renderer refuses it —
// pinning a hash for a body that was never inlined would authorise nothing.
func authoredScriptCSPHashes(rev *publish.PublishedWalkthrough) []string {
	if rev == nil || rev.VariantSet == nil {
		return nil
	}
	var hashes []string
	seen := make(map[string]bool)
	for _, v := range rev.VariantSet.Variants {
		for _, op := range v.Ops {
			if op.Op != publish.OpAddScript || op.Code == "" {
				continue
			}
			hash := cspSHA256([]byte(op.Code))
			if seen[hash] {
				continue
			}
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// proxiedUpstreamStyleSrc is the style-src of the ONE response shape that
// carries 'unsafe-inline': the proxied live-upstream artifact document (INV-18,
// design spec §3.1).
//
// Why it is safe here and nowhere else. The only authors of style on that
// response are the upstream — whose entire document we have already chosen to
// display, so refusing its inline <style> and style= while serving its HTML
// protects nothing — and the publisher, who is trusted. No viewer input can
// reach the document: feedback is never reflected (INV-7) and the player renders
// narration as text.
//
// 'unsafe-inline' in style-src grants NO script execution; the script story is
// unchanged and unchangeable here (script-src stays hash-sources-only, INV-12).
// CSS-driven exfiltration stays bounded by the FETCH directives, which do not
// widen anywhere: img-src 'self' data:, connect-src 'self', and font-src falling
// to default-src 'self'. That is the same containment argument the keystone spec
// already relies on.
//
// Style is presentation of a document we display; script is capability. The two
// directives move independently, and only this one moved.
const proxiedUpstreamStyleSrc = "'self' 'unsafe-inline'"

// writeHeaders applies the policy with the DEFAULT style-src: bare 'self', plus
// the shell's nonce when one is supplied. Every response shape except the
// proxied artifact goes through here, so 'unsafe-inline' is unreachable from it.
func (h *PublicHandler) writeHeaders(hdr http.Header, kind responseKind, nonce string, scriptHashes ...string) {
	styleSrc := "'self'"
	if nonce != "" {
		styleSrc = "'self' 'nonce-" + nonce + "'"
	}
	h.writeHeadersStyle(hdr, kind, styleSrc, scriptHashes...)
}

// writeHeadersStyle is writeHeaders with an explicit style-src. It exists so the
// widening lives at exactly one call site (serveProxiedArtifact) instead of
// being reachable by passing a magic value through the common path.
func (h *PublicHandler) writeHeadersStyle(hdr http.Header, kind responseKind, styleSrc string, scriptHashes ...string) {
	// INV-12: wholesale delete of any upstream CSP, then set agnt's. NEVER the
	// stripFrameDenyHeaders strip-merge path (which preserves upstream script-src).
	hdr.Del("Content-Security-Policy")
	hdr.Del("Content-Security-Policy-Report-Only")
	hdr.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		// script-src OMITS 'self' deliberately: with 'self' present the hash
		// source is inert (a hash only gates scripts once 'self'/'unsafe-inline'
		// are absent), defeating the "pin the exact bundle" intent. The public
		// plane serves no other same-origin JS, and the one bundle it does load
		// carries a matching SRI integrity attribute (see the <script> tag in
		// servePublicArtifact), so the hash source authorises exactly it.
		//
		// scriptHashes widens this by exactly one 'sha256-…' per authored script
		// body in the served revision (INV-12) — the whole containment story now
		// that INV-6 is retired: publisher script runs because its hash is
		// pinned here, while upstream and viewer script do not, because theirs is
		// not. Only hash sources are ever added; 'unsafe-inline', 'unsafe-eval',
		// and host sources remain unreachable from this composition.
		scriptSrc(h.cspHash, scriptHashes),
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

// scriptSrc composes the script-src directive from the bundle hash plus the
// authored-revision hashes. Every element is quoted as a hash source; there is
// no branch that can emit a keyword or host source, so the directive cannot be
// widened past "these exact script bodies" by any caller input.
func scriptSrc(bundleHash string, scriptHashes []string) string {
	var b strings.Builder
	b.WriteString("script-src '" + bundleHash + "'")
	for _, hash := range scriptHashes {
		b.WriteString(" '" + hash + "'")
	}
	return b.String()
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

// guardedUpstreamFetcher is the ONLY implementation of upstreamDocFetcher that
// opens a socket. It implements the §4a / INV-13 obligations the pure guard
// (publish.CheckUpstreamOrigin) documents but cannot discharge itself, because
// it does not own the transport:
//
//  1. https-only + resolved-address deny-list, via CheckUpstreamOrigin.
//  2. PIN-AND-DIAL: the connection is made to the exact address the guard
//     validated, not to a re-resolution of the hostname. Re-resolving is the
//     DNS-rebinding hole — a rebinding resolver answers publicly for the check
//     and privately for the dial.
//  3. Per-hop re-check: redirects are followed manually so every hop runs the
//     full guard. A chain longer than maxUpstreamRedirects fails closed.
//
// The zero value is the production fetcher. resolve, dial, and tlsConfig are
// test seams; each nil field falls back to the real thing. They exist because a
// correct guard makes an end-to-end test otherwise impossible: any local test
// listener is on loopback, which the deny-list refuses by design. A test supplies
// a resolver answering with a public address and a dialer that asserts it was
// handed exactly that address — so the pin itself is what gets verified, rather
// than being bypassed.
type guardedUpstreamFetcher struct {
	resolve   publish.Resolver
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	tlsConfig *tls.Config
}

func (f *guardedUpstreamFetcher) resolver() publish.Resolver {
	if f.resolve != nil {
		return f.resolve
	}
	return publish.SystemResolver
}

func (f *guardedUpstreamFetcher) dialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if f.dial != nil {
		return f.dial
	}
	return (&net.Dialer{Timeout: upstreamDialTimeout}).DialContext
}

// fetchDocument runs the guarded redirect walk and returns the upstream HTML.
// Every return path is either a complete document or an error — never both.
func (f *guardedUpstreamFetcher) fetchDocument(ctx context.Context, rawURL string) ([]byte, error) {
	body, _, err := f.walk(ctx, rawURL, "text/html", readUpstreamDocument)
	return body, err
}

// fetchSubresource runs the SAME guarded redirect walk for one subresource and
// returns its body with the canonical media type the allowlist admitted. Sharing
// walk() with fetchDocument is the point: there is one guarded fetch mechanism
// on this plane, so a hop check cannot be present on one route and absent on the
// other (INV-16).
func (f *guardedUpstreamFetcher) fetchSubresource(ctx context.Context, rawURL string) ([]byte, string, error) {
	return f.walk(ctx, rawURL, subresourceAcceptHeader, readUpstreamSubresource)
}

// subresourceAcceptHeader advertises exactly the INV-17 allowlist. It is a hint
// to the origin, never the enforcement — readUpstreamSubresource refuses on the
// response's own Content-Type regardless of what was asked for.
const subresourceAcceptHeader = "text/css,image/*,font/*"

// walk performs the guarded redirect walk and hands the terminal response to
// read. Every return path is either a complete body or an error — never both.
func (f *guardedUpstreamFetcher) walk(ctx context.Context, rawURL, accept string, read func(*http.Response) ([]byte, string, error)) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamFetchTimeout)
	defer cancel()

	current := rawURL
	for hop := 0; ; hop++ {
		if hop > maxUpstreamRedirects {
			// Fail closed: refuse, rather than serving whatever the last hop gave.
			return nil, "", fmt.Errorf("upstream: redirect chain exceeded %d hops", maxUpstreamRedirects)
		}
		// INV-13 on EVERY hop, not just the origin. A chain whose final hop lands
		// on 169.254.169.254 is refused at that hop.
		addrs, err := publish.CheckUpstreamOrigin(ctx, current, f.resolver())
		if err != nil {
			return nil, "", fmt.Errorf("upstream refused: %w", err)
		}
		resp, err := f.get(ctx, current, addrs[0], accept)
		if err != nil {
			return nil, "", fmt.Errorf("upstream fetch failed: %w", err)
		}

		if isRedirectStatus(resp.StatusCode) {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return nil, "", fmt.Errorf("upstream: %d with no Location", resp.StatusCode)
			}
			next, err := resolveRedirectTarget(current, location)
			if err != nil {
				return nil, "", fmt.Errorf("upstream: bad redirect target: %w", err)
			}
			current = next
			continue
		}

		body, mediaType, err := read(resp)
		resp.Body.Close()
		if err != nil {
			return nil, "", err
		}
		return body, mediaType, nil
	}
}

// get performs one guarded request. addr is the guard-validated address and is
// the ONLY address dialed; the URL's hostname is still what TLS verifies against,
// so pinning the address does not weaken certificate validation.
func (f *guardedUpstreamFetcher) get(ctx context.Context, rawURL string, addr netip.Addr, accept string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		port = "443" // CheckUpstreamOrigin already enforced https
	}
	pinned := net.JoinHostPort(addr.String(), port)

	dial := f.dialer()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// The address the transport derived from the URL host is DISCARDED in
			// favour of the validated one. This is the pin.
			return dial(ctx, network, pinned)
		},
		TLSClientConfig:     f.tlsConfig,
		TLSHandshakeTimeout: upstreamDialTimeout,
		// No connection pooling: a pooled connection is keyed by host, so reuse
		// would let a later request ride a connection established for a
		// separately-validated address.
		DisableKeepAlives: true,
		// Proxy is explicitly nil rather than defaulted: http.ProxyFromEnvironment
		// would route this fetch to whatever HTTPS_PROXY names, i.e. to an address
		// the guard never validated.
		Proxy: nil,
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Built from nothing but the published URL. No viewer header is forwarded:
	// forwarding Referer would hand the share token to the upstream (INV-9), and
	// forwarding Cookie/authorization would make the daemon a credential relay.
	req.Header.Set("User-Agent", "agnt-publish")
	req.Header.Set("Accept", accept)

	client := &http.Client{
		Transport: transport,
		// Redirects are walked by fetchDocument so each hop is re-guarded; letting
		// the client follow them would dial unvalidated addresses.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client.Do(req)
}

// isRedirectStatus reports whether a status carries a Location we must re-guard.
func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// resolveRedirectTarget resolves a Location value against the hop it came from.
// The result is fed straight back into CheckUpstreamOrigin, so a relative
// Location, a scheme downgrade, and a cross-origin jump are all re-validated
// rather than trusted.
func resolveRedirectTarget(from, location string) (string, error) {
	base, err := url.Parse(from)
	if err != nil {
		return "", err
	}
	loc, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(loc).String(), nil
}

// checkUpstreamEncoding refuses a body carrying a Content-Encoding we did not
// solicit (S6 advisory 1). The guarded fetch sets no Accept-Encoding, so Go's
// transport adds "gzip" itself and transparently decodes it — deleting the header
// before we ever see it. Anything LEFT on the response is therefore an
// unsolicited encoding (br, deflate, zstd, or a bogus token), and its bytes are
// NOT what their Content-Type says they are: splicing our bundle tag into a
// brotli stream labelled text/html would serve corrupt compressed bytes as an
// HTML document.
//
// The dev sibling (ProxyServer.modifyResponse, rewrite.go) DECODES the same set;
// this path deliberately REFUSES instead. The spec (§4e.1) permits either, and
// refusal is the conservative half of the choice on this plane: a decoder here is
// a decompression-bomb surface reachable by an anonymous viewer through a
// third-party origin, whereas on the dev plane the operator owns both ends. A
// refusal is loud (502), never a silently un-spliced document.
func checkUpstreamEncoding(resp *http.Response) error {
	enc := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Encoding")))
	if enc == "" || enc == "identity" {
		return nil
	}
	return fmt.Errorf("upstream: unsolicited Content-Encoding %q", enc)
}

// readUpstreamDocument validates and reads one non-redirect upstream response.
// Non-200, non-HTML, and over-cap all refuse: this document is about to be
// served as the published artifact, so anything we are not sure is a complete
// HTML document must not be presented as one.
func readUpstreamDocument(resp *http.Response) ([]byte, string, error) {
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !isHTMLDocumentMediaType(ct) {
		// The artifact route serves a document. Passing arbitrary upstream bytes
		// through it would turn an anonymous route into a general-purpose relay
		// for whatever the origin decides to return. EXACT media-type match, not a
		// substring scan (S6 advisory 2): "x-text/html-ish" is not a document.
		return nil, "", fmt.Errorf("upstream: content-type %q is not HTML", ct)
	}
	if err := checkUpstreamEncoding(resp); err != nil {
		return nil, "", err
	}
	body, truncated, err := readAllCapped(resp.Body, resp.ContentLength, maxPublicUpstreamBytes)
	if err != nil {
		return nil, "", fmt.Errorf("upstream: read failed: %w", err)
	}
	if truncated {
		return nil, "", fmt.Errorf("upstream: document exceeds %d bytes", maxPublicUpstreamBytes)
	}
	return body, "text/html", nil
}

// readUpstreamSubresource validates and reads one non-redirect subresource
// response (INV-17/INV-19). Order is load-bearing: status, then the media-type
// ALLOWLIST, then the encoding check, and only then the bounded read — a refused
// type never has its body buffered, let alone relayed. An over-cap body is
// refused, never truncated.
func readUpstreamSubresource(resp *http.Response) ([]byte, string, error) {
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream: status %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !isAllowedSubresourceMediaType(ct) {
		// HTML, JS, SVG, JSON, octet-stream, and anything unrecognised. Serving
		// these from 'self' would turn a bounded asset route into a relay for
		// whatever the origin decides to return — and SVG specifically is a
		// document format carrying script and style, not an image.
		return nil, "", fmt.Errorf("upstream: subresource content-type %q is not allowlisted", ct)
	}
	if err := checkUpstreamEncoding(resp); err != nil {
		return nil, "", err
	}
	body, truncated, err := readAllCapped(resp.Body, resp.ContentLength, maxPublicSubresourceBytes)
	if err != nil {
		return nil, "", fmt.Errorf("upstream: subresource read failed: %w", err)
	}
	if truncated {
		return nil, "", fmt.Errorf("upstream: subresource exceeds %d bytes", maxPublicSubresourceBytes)
	}
	return body, canonicalMediaType(ct), nil
}
