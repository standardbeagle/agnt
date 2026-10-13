package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/publish"
)

// multiPortUpstream is testUpstream for redirects that cross origins. The
// second origin is example.com on another port: a distinct origin (and so a
// distinct outbound bucket) that the test certificate still covers, so TLS
// verification stays real. The guard's seam is observed, not bypassed — the
// dialer still demands the validated address and only then redirects the
// connection to the local listener (publish-security-review-lessons §7).
func multiPortUpstream(t *testing.T, handler http.Handler) *guardedUpstreamFetcher {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return &guardedUpstreamFetcher{
		resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "example.com" {
				return nil, errors.New("unexpected resolve of " + host)
			}
			return []netip.Addr{publicAddr}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil || host != publicAddr.String() {
				return nil, errors.New("dial address is not the guard-validated one: " + addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
		tlsConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}
}

// A redirect must not carry a fetch onto an origin whose outbound budget is
// spent. Only the published origin used to be charged, so an origin with tokens
// left could bounce unlimited traffic at one with none.
//
// MUTATION PROOF: drop the per-hop charge in walk() and the destination serves
// the document — the hit counter and the 503 assertion both trip.
func TestOutboundCap_ChargesRedirectDestination(t *testing.T) {
	const destination = "https://example.com:8443/final"
	var destinationHits int
	f := multiPortUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			destinationHits++
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, upstreamDoc)
			return
		}
		http.Redirect(w, r, destination, http.StatusFound)
	}))

	clk := newManualClock()
	outbound := publish.NewRateLimiter(60, 1, clk.now)
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
	h.WithRateLimits(publish.NewRateLimiter(6000, 1000, clk.now), outbound)
	f.admitHop = h.admitOutbound

	// Spend the destination origin's whole budget. The published origin's
	// bucket is untouched: it is a different origin.
	if !outbound.Allow(upstreamOriginKey(destination)) {
		t.Fatal("setup: destination bucket should start with one token")
	}

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("redirect onto an exhausted origin: got %d, want 503", w.Code)
	}
	if destinationHits != 0 {
		t.Fatalf("the exhausted origin was fetched %d time(s) — the cap must refuse before the request", destinationHits)
	}
	if strings.Contains(w.Body.String(), "example.com") {
		t.Fatalf("503 body names the upstream: %q", w.Body.String())
	}
}

// Same-origin hops are charged too: each one is another request at the origin.
func TestGuardedWalk_ChargesEveryHopAfterTheFirst(t *testing.T) {
	_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			http.Redirect(w, r, "/c", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, upstreamDoc)
		}
	}))
	var charged []string
	f.admitHop = func(rawURL string) bool {
		charged = append(charged, rawURL)
		return true
	}
	if _, _, err := f.fetchDocument(context.Background(), "https://example.com/a"); err != nil {
		t.Fatal(err)
	}
	want := []string{"https://example.com/b", "https://example.com/c"}
	if len(charged) != len(want) || charged[0] != want[0] || charged[1] != want[1] {
		t.Fatalf("charged hops = %v, want %v (the first URL is charged by the caller)", charged, want)
	}
}

// signedSubresourceURL mints the reference the document rewriter would.
func signedSubresourceURL(h *PublicHandler, shareID, rawURL string, depth int) string {
	q := url.Values{}
	q.Set("u", rawURL)
	q.Set("d", strconv.Itoa(depth))
	q.Set("sig", h.signer.Sign(shareID, revisionForToken(h, validToken), rawURL, depth))
	return sharePrefix + validToken + subresourceSubPath + "?" + q.Encode()
}

// mintedTargets returns the upstream URLs a rewritten body points at.
func mintedTargets(t *testing.T, body string) []string {
	t.Helper()
	var targets []string
	for _, part := range strings.Split(body, subresourceSubPath+"?")[1:] {
		end := strings.IndexAny(part, `"')`)
		if end < 0 {
			end = len(part)
		}
		q, err := url.ParseQuery(strings.ReplaceAll(part[:end], "&amp;", "&"))
		if err != nil {
			t.Fatalf("unparseable minted reference %q: %v", part[:end], err)
		}
		targets = append(targets, q.Get("u"))
	}
	return targets
}

// Relative references resolve against where the resource was served from, not
// against the URL that redirected there.
//
// MUTATION PROOF: pass the requested URL to the rewriter instead of the final
// one and the minted target is https://example.com/theme.css.
func TestRedirectedResources_ResolveReferencesAgainstFinalURL(t *testing.T) {
	t.Run("document", func(t *testing.T) {
		_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/app/index.html" {
				http.Redirect(w, r, "/app/index.html", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<!DOCTYPE html><html><head><link rel="stylesheet" href="theme.css"></head><body></body></html>`)
		}))
		h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
		if h.signer == nil {
			t.Fatal("no subresource signer")
		}

		w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", w.Code)
		}
		targets := mintedTargets(t, w.Body.String())
		if len(targets) != 1 || targets[0] != "https://example.com/app/theme.css" {
			t.Fatalf("minted targets = %v, want [https://example.com/app/theme.css]", targets)
		}
	})

	t.Run("stylesheet", func(t *testing.T) {
		_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/assets/v2/site.css" {
				http.Redirect(w, r, "/assets/v2/site.css", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "text/css")
			io.WriteString(w, `.hero{background:url(img/bg.png)}`)
		}))
		h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)

		w := do(h, http.MethodGet, signedSubresourceURL(h, "share-1", "https://example.com/css/site.css", 1), "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("got %d, want 200: %s", w.Code, w.Body.String())
		}
		targets := mintedTargets(t, w.Body.String())
		if len(targets) != 1 || targets[0] != "https://example.com/assets/v2/img/bg.png" {
			t.Fatalf("minted targets = %v, want [https://example.com/assets/v2/img/bg.png]", targets)
		}
	})

	t.Run("fetcher reports the terminal hop", func(t *testing.T) {
		_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/app/index.html" {
				http.Redirect(w, r, "index.html", http.StatusFound) // relative Location
				return
			}
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, upstreamDoc)
		}))
		if _, final, err := f.fetchDocument(context.Background(), "https://example.com/app/"); err != nil || final != "https://example.com/app/index.html" {
			t.Fatalf("final = %q, err = %v", final, err)
		}
		if _, final, err := f.fetchDocument(context.Background(), "https://example.com/app/index.html"); err != nil || final != "https://example.com/app/index.html" {
			t.Fatalf("unredirected final = %q, err = %v", final, err)
		}
	})
}

// The JSON snapshots draw on the same (share, IP) budget as the document. They
// were unmetered: a viewer refused the document with 429 kept getting 200s.
//
// MUTATION PROOF: remove admitArtifact from either JSON route and its request
// here returns 200.
func TestJSONRoutes_ShareTheArtifactRateCap(t *testing.T) {
	for _, route := range []string{"/variants.json", "/walkthrough.json"} {
		t.Run(route, func(t *testing.T) {
			const burst = 2
			clk := newManualClock()
			h := NewPublicHandler(&fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: "share-1"}, nil, 0).
				WithRateLimits(publish.NewRateLimiter(60, burst, clk.now), nil)

			if w := do(h, http.MethodGet, sharePrefix+validToken, "", nil); w.Code != http.StatusOK {
				t.Fatalf("document: got %d, want 200", w.Code)
			}
			if w := do(h, http.MethodGet, sharePrefix+validToken+route, "", nil); w.Code != http.StatusOK {
				t.Fatalf("%s within burst: got %d, want 200", route, w.Code)
			}
			w := do(h, http.MethodGet, sharePrefix+validToken+route, "", nil)
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("%s over the budget: got %d, want 429", route, w.Code)
			}
			if body := w.Body.String(); strings.Contains(body, "share-1") || strings.Contains(body, validToken) {
				t.Fatalf("429 body leaked an identifier: %q", body)
			}
			if w := do(h, http.MethodGet, sharePrefix+validToken, "", nil); w.Code != http.StatusTooManyRequests {
				t.Fatalf("document after the JSON route spent the budget: got %d, want 429", w.Code)
			}
		})
	}
}

// The house defaults must admit the largest page the plane itself produces: one
// document, the two JSON snapshots, and every reference the rewriter will mint
// for that document — all from one viewer, all at one origin.
//
// MUTATION PROOF: restore the old bursts (30 inbound / 20 outbound) and the
// load is refused part-way: 503 at the 20th subresource.
func TestPublicPlaneDefaults_AdmitOneFullPageLoad(t *testing.T) {
	d := config.DefaultPublicPlaneConfig()
	if d.OutboundBurst < 1+maxSubresourceRefsPerDocument {
		t.Errorf("outbound burst %d cannot cover a document plus its %d rewritten references", d.OutboundBurst, maxSubresourceRefsPerDocument)
	}
	if d.ArtifactBurst < 3+maxSubresourceRefsPerDocument {
		t.Errorf("artifact burst %d cannot cover a document, both JSON snapshots, and %d subresources", d.ArtifactBurst, maxSubresourceRefsPerDocument)
	}
	if d.OutboundBurst > d.ArtifactBurst || d.OutboundRatePerMinute > d.ArtifactRatePerMinute {
		t.Errorf("the outbound bound protects a third party and must stay the tighter one: %+v", d)
	}

	// And the handler built from those defaults serves that load.
	var doc strings.Builder
	doc.WriteString("<!DOCTYPE html><html><head>")
	for i := 0; i < maxSubresourceRefsPerDocument; i++ {
		fmt.Fprintf(&doc, `<link rel="stylesheet" href="/css/%d.css">`, i)
	}
	doc.WriteString("</head><body></body></html>")
	f := &countingFetcher{body: []byte(doc.String())}
	h := upstreamHandler(t, upstreamRevision("https://app.example.com/live"), f)

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("document: got %d, want 200", w.Code)
	}
	targets := mintedTargets(t, w.Body.String())
	if len(targets) != maxSubresourceRefsPerDocument {
		t.Fatalf("rewriter minted %d references, want %d", len(targets), maxSubresourceRefsPerDocument)
	}
	for _, route := range []string{"/variants.json", "/walkthrough.json"} {
		if w := do(h, http.MethodGet, sharePrefix+validToken+route, "", nil); w.Code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200", route, w.Code)
		}
	}
	f.body = []byte(".a{color:red}")
	for i, target := range targets {
		if w := do(h, http.MethodGet, signedSubresourceURL(h, "share-1", target, 1), "", nil); w.Code != http.StatusOK {
			t.Fatalf("subresource %d of %d: got %d, want 200 — the default limits broke a legitimate page load", i+1, len(targets), w.Code)
		}
	}
}
