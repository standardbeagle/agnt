package proxy

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/standardbeagle/agnt/internal/debug"
)

// responseHasNoBody reports whether resp may not carry a body (RFC 9110 §6.4.1).
func responseHasNoBody(resp *http.Response) bool {
	if resp.Request != nil && resp.Request.Method == http.MethodHead {
		return true
	}
	return (resp.StatusCode >= 100 && resp.StatusCode < 200) ||
		resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusNotModified
}

// newDeflateReader decodes a "deflate" Content-Encoding body. RFC 9110 §8.4.1.2
// defines it as the zlib format (RFC 1950): a 2-byte header around the DEFLATE
// stream. Some servers send raw DEFLATE (RFC 1951) instead, so the header is
// sniffed the way browsers do: a valid zlib header (compression method 8 and a
// header checksum divisible by 31) selects zlib, anything else raw DEFLATE.
func newDeflateReader(body io.Reader) (io.ReadCloser, error) {
	br := bufio.NewReader(body)
	if h, err := br.Peek(2); err == nil && h[0]&0x0f == 8 && (uint16(h[0])<<8|uint16(h[1]))%31 == 0 {
		return zlib.NewReader(br)
	}
	return flate.NewReader(br), nil
}

// modifyResponse rewrites URLs and injects JavaScript into HTML responses.
func (ps *ProxyServer) modifyResponse(resp *http.Response) error {
	// The upstream answered (whatever the status): any outage the loading page
	// was counting is over.
	ps.noteUpstreamSuccess()

	// Auth breakout: a content-frame request answered with a redirect to a
	// matching identity provider must NOT be followed inside the iframe —
	// the IdP (a foreign origin the proxy never touches) refuses framing.
	// Replace the redirect with a stub that runs the flow in a top-level
	// window via the chrome shell. Checked before Location rewriting: a
	// breakout target is by definition external, never the proxied backend.
	if ps.interceptAuthRedirect(resp) {
		return nil
	}

	// Rewrite Location header for redirects
	ps.rewriteLocationHeader(resp)

	// Rewrite Set-Cookie headers for domain/path
	ps.rewriteSetCookieHeaders(resp)

	// No body to inject into: a HEAD response (its Content-Length describes
	// the GET body it did not send), an informational 1xx, 204 No Content and
	// 304 Not Modified. Injecting would give each a body it must not have and
	// a Content-Length that contradicts the real resource.
	if responseHasNoBody(resp) {
		return nil
	}

	contentType := resp.Header.Get("Content-Type")
	if !ShouldInject(contentType) {
		return nil
	}

	// Check if response is compressed
	encoding := strings.ToLower(resp.Header.Get("Content-Encoding"))
	compressed := encoding != "" && encoding != "identity"
	if compressed && !knownContentEncoding(encoding) {
		// Unsupported encoding - pass through without modification
		debug.Log("proxy", "Unsupported Content-Encoding: %s - passing through without injection", encoding)
		return nil
	}

	// The origin's Content-Length is the exact buffered size only for an identity
	// body; a decompressed stream's size is unknown until it has been read.
	var sizeHint int64 = -1
	if !compressed {
		sizeHint = resp.ContentLength
	}
	// A known-oversized identity body is passed through untouched: injection
	// requires buffering the whole document and an unbounded buffer is an OOM
	// vector on HTML-typed downloads/streams.
	if sizeHint > maxInjectBodyBytes {
		debug.Log("proxy", "Skipping injection: body too large (%d bytes)", sizeHint)
		return nil
	}
	// Reserve against the process-wide buffering budget BEFORE touching the body:
	// a decoder consumes bytes, so declining after one is open would forward a
	// half-read stream. An unknown-length body can grow to the per-response cap,
	// so that is what it reserves. Over budget the response is passed through
	// uninjected, and that is reported (see noteInjectShed).
	reserve := sizeHint
	if reserve < 0 {
		reserve = maxInjectBodyBytes
	}
	if !injectBudget.tryReserve(reserve) {
		ps.noteInjectShed(resp, reserve)
		return nil
	}
	// The buffered bytes live until the downstream client has consumed the
	// body, so the reservation is released when the body is closed, not when
	// this function returns. Every return that keeps the buffer hands that
	// release to the body it installs; the others release now.
	released := false
	releaseNow := func() {
		if !released {
			released = true
			injectBudget.release(reserve)
		}
	}
	defer releaseNow()
	holdUntilClosed := func(body io.ReadCloser) io.ReadCloser {
		if released {
			return body
		}
		released = true
		var once sync.Once
		return &heldBody{ReadCloser: body, onClose: func() { once.Do(func() { injectBudget.release(reserve) }) }}
	}

	var bodyReader io.Reader = resp.Body
	// Set when an oversized unknown-length body is passed through mid-decode:
	// the decompressor is handed downstream, so its deferred Close must not run.
	passthrough := false
	// closeDecoder releases the decompressor (zstd's owns goroutines). The
	// passthrough path hands it downstream, so the body's Close must run it.
	closeDecoder := func() {}
	defer func() {
		if !passthrough {
			closeDecoder()
		}
	}()

	if compressed {
		// Record what the decoder constructor consumes so a failed construction
		// can hand the untouched stream on.
		rec := &recordReader{r: resp.Body, recording: true}
		decoder, closer, err := newDecoder(encoding, rec)
		if err != nil {
			debug.Log("proxy", "Failed to decompress %s response: %v", encoding, err)
			resp.Body = &passthroughReadCloser{
				Reader: io.MultiReader(bytes.NewReader(rec.buf), resp.Body),
				Closer: resp.Body,
			}
			return nil
		}
		rec.recording = false
		rec.buf = nil
		bodyReader = decoder
		closeDecoder = closer
	}

	bodyBytes, truncated, err := readAllCapped(bodyReader, sizeHint, maxInjectBodyBytes)
	if err != nil {
		return err
	}
	if truncated {
		// Unknown-length (chunked or decompressed) body exceeded the injection
		// cap mid-read. Pass it through uncompressed without injection: the
		// already-read head plus the in-flight decompressor reassemble the full
		// stream downstream. The decoder is closed by the body's Close instead of
		// this function, because the reader is handed off.
		passthrough = true
		// Capture the origin body now: the closure runs after resp.Body is
		// reassigned below, and resp.Body.Close() would call itself.
		origBody := resp.Body
		resp.Body = holdUntilClosed(&passthroughReadCloser{
			Reader: io.MultiReader(bytes.NewReader(bodyBytes), bodyReader),
			Closer: closerFunc(func() error {
				closeDecoder()
				return origBody.Close()
			}),
		})
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("ETag")
		resp.Header.Del("Last-Modified")
		resp.Header.Del("Content-MD5")
		debug.Log("proxy", "Skipping injection: body exceeded %d bytes", maxInjectBodyBytes)
		return nil
	}
	resp.Body.Close()

	// Rewrite absolute URLs in HTML content pointing to target back to proxy
	modifiedBody := ps.rewriteURLsInBodyFor(resp.Request, bodyBytes)

	// Always-wrap model: a genuine top-level navigation (a real request, no
	// frame marker, a full HTML document) is replaced by an outer chrome shell
	// whose body is a content <iframe>; the real page is fetched when that
	// iframe loads (its request carries the frame marker) and served in content
	// role. Everything else — content-frame requests, HTML fragments, and
	// direct unit-test calls with no top-level request — is injected in place.
	// See docs/responsive-canonical-target.md §4.
	topLevel := resp.Request != nil &&
		!isContentFrameRequest(resp.Request) &&
		isTopLevelNavigation(resp.Request) &&
		isFullHTMLDocument(modifiedBody)
	authCfgJS := ps.AuthBreakoutRules().clientConfigJS()
	if topLevel {
		reqURI := resp.Request.URL.RequestURI()
		fid := frameIDForPath(resp.Request.URL.Path)
		// Shell id is distinct from the content frame id (shellFrameID) so the two
		// frames — which share a WS and both run the exec handler — are separately
		// addressable as "outer" (chrome) vs "inner" (content).
		modifiedBody = BuildShellDocument(ps.ID, shellFrameID(fid), contentFrameSrc(reqURI, fid), authCfgJS)
	} else {
		// Stamp the content role only when this is genuinely our content frame
		// (the request carries the frame marker minted in the shell). A
		// markerless response — a foreign app-embedded iframe, an HTML fragment,
		// or a direct unit-test call — gets the bundle with no role marker, so
		// scripts/frames.js resolves the role: a top frame is canonical content
		// (unwrapped fallback), a nested frame is passive.
		markerID := ""
		if resp.Request != nil && resp.Request.URL != nil {
			// The marker is attacker-controllable (arbitrary query value) yet is
			// echoed into an inline <script> via %q, which does NOT HTML/JS-escape.
			// Accept only a well-formed server-minted id; anything else is treated
			// as markerless (role resolved in-browser).
			markerID = sanitizeFrameID(resp.Request.URL.Query().Get(frameMarkerParam))
		}
		if markerID != "" {
			modifiedBody = InjectContentRuntime(modifiedBody, ps.ID, markerID, authCfgJS)
		} else {
			modifiedBody = InjectInstrumentationAndMeta(modifiedBody, ps.ID)
		}
		// The content is framed by our own same-origin shell, so strip any
		// frame-deny headers that would otherwise refuse the embed.
		stripFrameDenyHeaders(resp)
	}

	// Update response with uncompressed modified content
	resp.Body = holdUntilClosed(io.NopCloser(bytes.NewReader(modifiedBody)))
	resp.ContentLength = int64(len(modifiedBody))
	resp.Header.Set("Content-Length", strconv.Itoa(len(modifiedBody)))

	// Remove encoding headers since we're returning uncompressed content
	resp.Header.Del("Content-Encoding")

	// The body was rewritten/injected, so the origin's validators no longer
	// describe what we return. Leaving them lets a cache honour a later
	// conditional request (If-None-Match / If-Modified-Since) and serve the
	// original, un-injected body — stale from our perspective.
	resp.Header.Del("ETag")
	resp.Header.Del("Last-Modified")
	resp.Header.Del("Content-MD5")

	return nil
}

// interceptAuthRedirect replaces a content-frame 3xx response whose Location
// matches an auth-breakout pattern with a 200 HTML stub that hands the URL to
// the chrome shell for a top-level auth flow. Returns true when the response
// was replaced (caller must skip all further rewriting/injection). Only
// content-frame requests are intercepted: a genuine top-level navigation to
// the IdP is not framed and needs no breakout.
func (ps *ProxyServer) interceptAuthRedirect(resp *http.Response) bool {
	ab := ps.AuthBreakoutRules()
	if ab == nil || resp.StatusCode < 300 || resp.StatusCode > 399 {
		return false
	}
	if !isContentFrameRequest(resp.Request) {
		return false
	}
	location := resp.Header.Get("Location")
	if location == "" || !ab.MatchesURL(location) {
		return false
	}

	debug.Log("proxy", "auth breakout: intercepted %d redirect to %s (content frame)", resp.StatusCode, location)
	stub := buildAuthBreakoutStub(location)
	if resp.Body != nil {
		resp.Body.Close()
	}
	resp.Body = io.NopCloser(bytes.NewReader(stub))
	resp.StatusCode = http.StatusOK
	resp.Status = http.StatusText(http.StatusOK)
	resp.ContentLength = int64(len(stub))
	resp.Header.Del("Location")
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("ETag")
	resp.Header.Del("Last-Modified")
	resp.Header.Del("Content-MD5")
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	resp.Header.Set("Content-Length", strconv.Itoa(len(stub)))
	resp.Header.Set("Cache-Control", "no-store")
	return true
}

// maxInjectBodyBytes caps how much of an HTML response body modifyResponse
// will buffer for rewriting/injection. Injection is inherently a whole-body
// operation, so without a bound a huge HTML-typed download or stream is fully
// loaded into memory (OOM vector). Oversized bodies are passed through
// unmodified — injection is a dev convenience, never worth destabilizing the
// proxy for.
const maxInjectBodyBytes = 16 << 20 // 16 MiB

// injectBufferBudget is the most bytes of HTML the proxy buffers for injection
// across all in-flight responses (every proxy in the process). A response whose
// reservation does not fit is passed through uninjected.
const injectBufferBudget = 64 << 20

type bufferBudget struct {
	limit int64
	used  atomic.Int64
	shed  atomic.Int64
}

func (b *bufferBudget) tryReserve(n int64) bool {
	for {
		cur := b.used.Load()
		if cur+n > b.limit {
			b.shed.Add(1)
			return false
		}
		if b.used.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func (b *bufferBudget) release(n int64) { b.used.Add(-n) }

var injectBudget = &bufferBudget{limit: injectBufferBudget}

// noteInjectShed makes a skipped injection visible: the page works but has no
// agnt instrumentation, which an agent would otherwise read as a broken proxy.
// Logged for the first shed and every 50th after, as a diagnostic.
func (ps *ProxyServer) noteInjectShed(resp *http.Response, reserve int64) {
	n := injectBudget.shed.Load()
	debug.Log("proxy", "Skipping injection: buffering budget exhausted (%d bytes wanted)", reserve)
	if n != 1 && n%50 != 0 {
		return
	}
	url := ""
	if resp.Request != nil && resp.Request.URL != nil {
		url = resp.Request.URL.String()
	}
	ps.logger.LogDiagnostic(ProxyDiagnostic{
		Timestamp: time.Now(),
		Level:     DiagnosticWarning,
		Category:  "injection",
		Event:     "injection_skipped_memory_budget",
		Message:   fmt.Sprintf("HTML response %s was served without agnt instrumentation: injection buffering budget (%dMiB) is in use by other responses (%d skipped so far)", url, injectBufferBudget>>20, n),
		Target:    ps.TargetURL.String(),
	})
}

// passthroughReadCloser pairs a reassembled reader (buffered head + in-flight
// remainder) with the closer of the original response body.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

type passthroughReadCloser struct {
	io.Reader
	io.Closer
}

// readAllCapped reads up to cap bytes from r. When sizeHint > 0 it presizes
// the buffer to that exact size plus a small read margin, so a body whose
// length is known up front (identity Content-Length) is read in one
// allocation with no grow-by-doubling reallocations. A non-positive hint
// (unknown length — chunked or decompressed) starts small. truncated reports
// whether r still had data beyond the cap; the unread remainder stays in r
// for the caller to pass through.
func readAllCapped(r io.Reader, sizeHint int64, cap int64) ([]byte, bool, error) {
	var buf *bytes.Buffer
	if sizeHint > 0 {
		if sizeHint > cap {
			sizeHint = cap
		}
		buf = bytes.NewBuffer(make([]byte, 0, sizeHint+bytes.MinRead))
	} else {
		buf = &bytes.Buffer{}
	}
	_, err := buf.ReadFrom(io.LimitReader(r, cap+1))
	data := buf.Bytes()
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > cap {
		// The (cap+1)-th byte is already consumed; hand the whole head to the
		// passthrough MultiReader so no byte is lost.
		return data, true, nil
	}
	return data, false, nil
}

// injectPublicBundle splices the RolePublic bundle tag into a PROXIED UPSTREAM
// document for the public publish plane (S6). It is the body half of what
// serveProxiedArtifact does; the header half is PublicHandler.writeHeaders.
//
// This is deliberately NOT one of the dev composers (BuildShellDocument /
// InjectContentRuntime): those hardcode dev-only payload — window
// .__devtool_proxy_id, the auth-breakout config, the popup relay — and giving
// them a role parameter would manufacture the one code path on which public
// traffic could inherit the dev control surface (INV-1). See the "Wrap+inject
// seam" note in injector.go: the reusable half (instrumentationInsertOffset,
// spliceInto) is already role-free, and this function is the public composer
// that calls it.
//
// Both arguments are server-derived constants (the content-addressed asset path
// and its sha256), never upstream or viewer input, so no escaping is required
// here and none is implied for future callers: this must not be handed an
// attacker-controlled value.
//
// The upstream document's own bytes are otherwise passed through untouched. The
// bundle lands before </head> when the document has one, so it runs ahead of the
// upstream's body scripts — which the public CSP refuses regardless, their hash
// being absent from the hash-only script-src (INV-12).
func injectPublicBundle(body []byte, assetPath, integrity string) []byte {
	tag := []byte(`<script src="` + assetPath + `" integrity="` + integrity + `" crossorigin="anonymous"></script>`)
	if at, ok := instrumentationInsertOffset(body); ok {
		return spliceInto(body, at, tag)
	}
	return spliceInto(body, 0, tag) // last resort: prepend
}

// stripFrameDenyHeaders removes framing-prohibition headers from a proxied
// content response so our own same-origin chrome shell can embed it. X-Frame-
// Options is dropped wholesale; only the frame-ancestors directive is removed
// from CSP (other directives are preserved). Pragmatic, narrowly-scoped to the
// proxied content the proxy itself serves — see
// docs/responsive-canonical-target.md §4.3.
//
// FORBIDDEN ON THE PUBLIC PUBLISH PLANE (INV-11/INV-12). This is a strip-MERGE:
// it deletes only frame-ancestors and preserves every other upstream directive,
// so a hostile upstream's script-src 'unsafe-inline' would survive onto a
// published artifact. The public plane must instead Header.Del then Header.Set
// wholesale — PublicHandler.writeHeaders. Routing public responses through here
// already caused one review rewind on this epic; the preservation behaviour is
// pinned by TestStripFrameDenyHeadersPreservesUpstreamScriptSrc so the
// distinction cannot quietly erode.
func stripFrameDenyHeaders(resp *http.Response) {
	resp.Header.Del("X-Frame-Options")
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		return
	}
	if stripped := stripFrameAncestors(csp); stripped != csp {
		if stripped == "" {
			resp.Header.Del("Content-Security-Policy")
		} else {
			resp.Header.Set("Content-Security-Policy", stripped)
		}
	}
}

// stripFrameAncestors removes the frame-ancestors directive (if present) from a
// CSP header value, returning the remaining directives joined by "; ".
func stripFrameAncestors(csp string) string {
	parts := strings.Split(csp, ";")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		// Match on the directive name (first whitespace-delimited token) exactly,
		// so "frame-ancestors" is stripped but "frame-ancestors-foo" (a different
		// directive) is not mistaken for it by a bare prefix test.
		name := trimmed
		if i := strings.IndexAny(trimmed, " \t"); i != -1 {
			name = trimmed[:i]
		}
		if strings.EqualFold(name, "frame-ancestors") {
			continue
		}
		kept = append(kept, trimmed)
	}
	return strings.Join(kept, "; ")
}

// rewriteLocationHeader rewrites Location headers to point to the proxy instead of the target.
func (ps *ProxyServer) rewriteLocationHeader(resp *http.Response) {
	location := resp.Header.Get("Location")
	if location == "" {
		return
	}

	rewritten := ps.rewriteURLFor(resp.Request, location)
	if rewritten != location {
		resp.Header.Set("Location", rewritten)
	}
}

// rewriteSetCookieHeaders rewrites Set-Cookie headers to work with the proxy domain.
func (ps *ProxyServer) rewriteSetCookieHeaders(resp *http.Response) {
	cookies := resp.Header["Set-Cookie"]
	if len(cookies) == 0 {
		return
	}

	targetHost := ps.TargetURL.Hostname()

	for i, cookie := range cookies {
		// Remove or rewrite Domain attribute if it matches target
		// This allows cookies to work on localhost proxy
		if strings.Contains(strings.ToLower(cookie), "domain=") {
			// Parse and rebuild cookie without domain restriction
			// or with proxy domain
			cookies[i] = ps.rewriteCookieDomain(cookie, targetHost)
		}
	}

	resp.Header["Set-Cookie"] = cookies
}

// rewriteCookieDomain removes or rewrites the Domain attribute in a Set-Cookie header.
func (ps *ProxyServer) rewriteCookieDomain(cookie string, targetHost string) string {
	// Split cookie into parts
	parts := strings.Split(cookie, ";")
	var newParts []string

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		lower := strings.ToLower(trimmed)

		// Skip domain attributes that match target host
		if strings.HasPrefix(lower, "domain=") {
			domainValue := strings.TrimPrefix(lower, "domain=")
			domainValue = strings.TrimPrefix(domainValue, ".") // Remove leading dot

			// Strip the Domain attribute (so the cookie applies to the proxy host)
			// only when it genuinely covers the target: an exact host match, or the
			// target host is a subdomain of the cookie domain (cookie-domain scoping
			// rules). A naive bidirectional substring test wrongly matched unrelated
			// hosts (e.g. "evil-example.com" vs "example.com") and stripped valid
			// domains.
			th := strings.ToLower(targetHost)
			if th == domainValue || strings.HasSuffix(th, "."+domainValue) {
				continue
			}
		}

		newParts = append(newParts, part)
	}

	return strings.Join(newParts, ";")
}

// rewriteURL rewrites a URL from the target server to the proxy server, for
// callers with no request in hand (see proxyOriginFor for what that implies).
func (ps *ProxyServer) rewriteURL(rawURL string) string {
	return ps.rewriteURLFor(nil, rawURL)
}

// rewriteURLFor rewrites a URL from the target server to the origin the
// client of req reached the proxy on. req is the upstream request of the
// response being rewritten (resp.Request); nil falls back to the proxy-wide
// origin.
func (ps *ProxyServer) rewriteURLFor(req *http.Request, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// Only rewrite absolute URLs that point to the target
	if parsed.Host == "" {
		// Relative URL, no rewriting needed
		return rawURL
	}

	// Check if this URL points to our target
	targetHost := ps.TargetURL.Host
	if parsed.Host != targetHost {
		// Different host, don't rewrite
		return rawURL
	}

	// Rewrite to the origin this client is on
	proxyScheme, proxyHost := ps.proxyOriginFor(req)

	parsed.Scheme = proxyScheme
	parsed.Host = proxyHost

	return parsed.String()
}

// proxyOriginFor returns the scheme and host a rewritten URL should carry for
// the client behind req: the origin that client reached the proxy on.
//
// One proxy answers on several origins at once — its loopback listener, a
// tailnet or LAN address, a tunnel's public hostname — and a URL rewritten to
// the wrong one sends the browser somewhere else. Rewriting everything to the
// proxy-wide value did exactly that: with a tunnel up, a developer on
// http://localhost:<port> was redirected out to the public hostname (through
// Cloudflare Access, for a named tunnel), and on a tailnet or LAN bind a
// remote browser was redirected to http://localhost:<port> — its own machine.
//
// req is the upstream request (resp.Request). It carries what the Rewrite
// hook recorded about the inbound one: the listener it arrived on (context),
// and the Host and scheme the client used (X-Forwarded-Host/-Proto, which
// Rewrite sets itself after ReverseProxy has dropped any the client sent).
//
//   - Arrived through the named tunnel's ingress, or addressed to the bound
//     public URL's host (a quick tunnel shares the proxy's own listener and
//     is told apart only by the Host it forwards): the public URL.
//   - Addressed to one of the listener's own authorities (see
//     isOwnListenerAuthority): that authority, with the scheme the listener
//     served.
//   - Anything else, or no request: the proxy-wide origin (public URL if
//     bound, else http://localhost:<port>), as before.
//
// The client-supplied Host is only ever echoed back when it names this
// listener's own port on a loopback name, an IP literal or one of this node's
// tailnet names — never an arbitrary hostname — and only into that client's
// own response.
func (ps *ProxyServer) proxyOriginFor(req *http.Request) (scheme, host string) {
	scheme, host = ps.getProxyScheme(), ps.getProxyHost()
	if req == nil {
		return scheme, host
	}

	var public *url.URL
	if raw := ps.GetPublicURL(); raw != "" {
		if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" && parsed.Scheme != "" {
			public = parsed
		}
	}
	if tunnelled, _ := req.Context().Value(tunnelledKey{}).(bool); tunnelled && public != nil {
		return public.Scheme, public.Host
	}

	inboundHost := req.Header.Get("X-Forwarded-Host")
	if inboundHost == "" {
		return scheme, host
	}
	if public != nil && strings.EqualFold(inboundHost, public.Host) {
		return public.Scheme, public.Host
	}
	if ps.isOwnListenerAuthority(req.Context(), inboundHost) {
		inboundScheme := "http"
		if req.Header.Get("X-Forwarded-Proto") == "https" {
			inboundScheme = "https"
		}
		return inboundScheme, inboundHost
	}
	return scheme, host
}

// isOwnListenerAuthority reports whether authority (a Host header value)
// names this proxy's own listener: the bound port, on a loopback name, a
// literal IP address, or — for a tailnet bind — one of this node's tailnet
// names. Any other hostname is not recognised, so it is never echoed into a
// rewritten URL.
func (ps *ProxyServer) isOwnListenerAuthority(ctx context.Context, authority string) bool {
	hostname, port, err := net.SplitHostPort(authority)
	if err != nil || port != strconv.Itoa(ps.BoundPort()) {
		return false
	}
	if isLoopbackAuthority(authority) || net.ParseIP(hostname) != nil {
		return true
	}
	return ps.boundToTailnet() && ps.isTailnetAuthority(ctx, authority)
}

// getProxyHost returns the host:port for the proxy server.
// If a public URL is configured (for tunnels), returns that host.
// Otherwise returns localhost:port for local development.
func (ps *ProxyServer) getProxyHost() string {
	// If a public URL is configured (for tunnels), use its host
	if publicURL := ps.GetPublicURL(); publicURL != "" {
		if parsed, err := url.Parse(publicURL); err == nil && parsed.Host != "" {
			return parsed.Host
		}
	}

	// liveAddr() is in format "addr:port" or "[::]:port"
	// We need to return "localhost:port" for redirect purposes
	addr := ps.liveAddr()
	port := "8080"
	if lastColon := strings.LastIndex(addr, ":"); lastColon != -1 {
		port = addr[lastColon+1:]
	}
	return "localhost:" + port
}

// getProxyScheme returns the scheme (http/https) for the proxy server.
// If a public URL is configured with HTTPS (common for tunnels), returns https.
func (ps *ProxyServer) getProxyScheme() string {
	if publicURL := ps.GetPublicURL(); publicURL != "" {
		if parsed, err := url.Parse(publicURL); err == nil && parsed.Scheme != "" {
			return parsed.Scheme
		}
	}
	return "http"
}

// cutOriginPrefix reports whether value is a URL on origin (scheme://host:port)
// and returns what follows the origin. The origin must end at a URL boundary
// (end of value, '/', '?' or '#'): a bare prefix match would also take
// http://localhost:30001 or http://localhost:3000.example.com for a target of
// localhost:3000 and rewrite a different server's URL onto the proxy.
func cutOriginPrefix(value, origin string) (string, bool) {
	rest, ok := strings.CutPrefix(value, origin)
	if !ok {
		return "", false
	}
	if rest != "" && !strings.ContainsRune("/?#", rune(rest[0])) {
		return "", false
	}
	return rest, true
}

// urlBearingAttrPatterns match HTML attributes whose value is a navigational
// or resource-fetch URL (href/src/action/etc). Rewriting is scoped to these
// attribute values only — never to the raw body — so JSON payloads, inline
// <script> bodies, and plain visible text that merely mention the target
// host as a string are left untouched. A quoted value is captured verbatim
// (including any `\/`-escaped slashes, e.g. inside a JSON-in-attribute blob
// like `data-config`) so the replacement can preserve the same escaping.
// Go's RE2 engine has no backreferences, so double- and single-quoted forms
// are two separate patterns rather than one pattern with \2.
var urlBearingAttrPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)(\b(?:href|src|action|formaction|poster|cite|manifest|background)\s*=\s*)(")([^"]*)(")`),
	regexp.MustCompile(`(?is)(\b(?:href|src|action|formaction|poster|cite|manifest|background)\s*=\s*)(')([^']*)(')`),
}

// rewriteURLsInBody rewrites absolute URLs in URL-bearing HTML attributes
// (href, src, action, etc.) from target to proxy. It intentionally does not
// touch the rest of the body — inline <script> content, JSON payloads, and
// plain text may legitimately contain the target host as a string, and a
// blind whole-body replace would corrupt that data (see
// 01KXM3MH9C3CXM12W7VEGSD1V4). It also never downgrades an https:// target
// reference to plaintext http:// — only the http:// variant respects the
// configured proxy scheme (e.g. upgraded to https behind a tunnel).
func (ps *ProxyServer) rewriteURLsInBody(body []byte) []byte {
	return ps.rewriteURLsInBodyFor(nil, body)
}

// rewriteURLsInBodyFor is rewriteURLsInBody for the client behind req, the
// upstream request of the response being rewritten (see proxyOriginFor).
func (ps *ProxyServer) rewriteURLsInBodyFor(req *http.Request, body []byte) []byte {
	// Guard against nil TargetURL (can happen in tests with partial setup)
	if ps.TargetURL == nil {
		return body
	}

	targetHost := ps.TargetURL.Host
	if targetHost == "" {
		return body
	}

	// Fast path: no occurrence of the target host anywhere in the body means
	// nothing to rewrite, so skip the regex scan entirely.
	if !bytes.Contains(body, []byte(targetHost)) {
		return body
	}

	proxyScheme, proxyHost := ps.proxyOriginFor(req)

	targetHTTP := "http://" + targetHost
	targetHTTPS := "https://" + targetHost
	httpProxyURL := proxyScheme + "://" + proxyHost
	// The https variant is never downgraded to plaintext, regardless of the
	// scheme the proxy itself is currently reachable on.
	httpsProxyURL := "https://" + proxyHost

	rewriteValue := func(value string) string {
		unescaped := value
		escaped := false
		if strings.Contains(value, `\/`) {
			unescaped = strings.ReplaceAll(value, `\/`, "/")
			escaped = true
		}

		var rewritten string
		if rest, ok := cutOriginPrefix(unescaped, targetHTTPS); ok {
			rewritten = httpsProxyURL + rest
		} else if rest, ok := cutOriginPrefix(unescaped, targetHTTP); ok {
			rewritten = httpProxyURL + rest
		} else {
			return value
		}

		if escaped {
			return strings.ReplaceAll(rewritten, "/", `\/`)
		}
		return rewritten
	}

	result := body
	for _, pattern := range urlBearingAttrPatterns {
		result = pattern.ReplaceAllFunc(result, func(match []byte) []byte {
			sub := pattern.FindSubmatch(match)
			prefix, openQuote, value, closeQuote := sub[1], sub[2], string(sub[3]), sub[4]

			newValue := rewriteValue(value)
			if newValue == value {
				return match
			}

			out := make([]byte, 0, len(prefix)+len(openQuote)+len(newValue)+len(closeQuote))
			out = append(out, prefix...)
			out = append(out, openQuote...)
			out = append(out, newValue...)
			out = append(out, closeQuote...)
			return out
		})
	}
	return result
}

// knownContentEncoding reports whether newDecoder can decode encoding.
func knownContentEncoding(encoding string) bool {
	return strings.Contains(encoding, "gzip") || strings.Contains(encoding, "deflate") ||
		strings.Contains(encoding, "br") || strings.Contains(encoding, "zstd")
}

// newDecoder opens the decompressor for encoding over r. closer releases it
// (zstd's owns goroutines).
func newDecoder(encoding string, r io.Reader) (io.Reader, func(), error) {
	switch {
	case strings.Contains(encoding, "gzip"):
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return gz, func() { gz.Close() }, nil
	case strings.Contains(encoding, "deflate"):
		df, err := newDeflateReader(r)
		if err != nil {
			return nil, nil, err
		}
		return df, func() { df.Close() }, nil
	case strings.Contains(encoding, "br"):
		return brotli.NewReader(r), func() {}, nil
	default:
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return zr, zr.Close, nil
	}
}

// recordReader keeps every byte it passes on while recording, so bytes a
// decoder constructor consumed can be replayed if construction fails.
type recordReader struct {
	r         io.Reader
	recording bool
	buf       []byte
}

func (rr *recordReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if rr.recording {
		rr.buf = append(rr.buf, p[:n]...)
	}
	return n, err
}

// heldBody runs onClose (releasing an injection-budget reservation) when the
// downstream client closes the body.
type heldBody struct {
	io.ReadCloser
	onClose func()
}

func (h *heldBody) Close() error {
	err := h.ReadCloser.Close()
	h.onClose()
	return err
}
