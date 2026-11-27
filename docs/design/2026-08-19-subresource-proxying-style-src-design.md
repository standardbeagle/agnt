# Subresource Proxying for Live-Upstream Shares — style-src + Guarded Fetch Route

Date: 2026-08-19
Status: **IMPLEMENTED 2026-08-19** (task `01KYQFAZSV6AQTKASQHYVPKJZC`). The owner's ruling was
"follow the spec's recommendations", so every §8 open question resolves to this document's own
recommended position, or — where it stated none — to the most conservative option. Those
resolutions and the implementation's divergences from the plan are recorded in §9; §0-§7 are
left as written so the reasoning that produced the design stays auditable.
Kind: Design spec extending `2026-07-13-public-walkthrough-publish-security.md` (the security keystone). Invariant numbering continues after INV-15; no existing invariant is changed, retired, or renumbered here.
Worktrack: `01KYQFAZSV6AQTKASQHYVPKJZC` (follow-up declared by S6 `01KYJC0CQTMCV91FZ5B251V50S`, sharpened by S10's code verification).

## 0. Problem

A share whose revision names a live upstream is served by proxying **only the
document** (`serveProxiedArtifact` → `guardedUpstreamFetcher.fetchDocument`,
`internal/proxy/public_routes.go`). Everything the document references still
points at the upstream origin, and the public CSP refuses it. The result: a
proxied demo of a real site renders substantially unstyled and imageless.

S10's finding stands and is confirmed against the shipped code: this is **not**
merely a missing fetch route. `serveProxiedArtifact` passes an empty `nonce` to
`writeHeaders`, whose `styleSrc` is then bare `'self'` — so the proxied
response authorises **no inline style at all**. The upstream's own inline
`<style>` blocks and `style=` attributes die along with its external
stylesheets. A subresource route alone cannot fix that; the `style-src` story
on the proxied response is the load-bearing design question, and it is answered
first (§3) before the route (§4).

## 1. Current state (verified in code, not assumed)

All claims below were read from the tree at design time.

| Fact | Where |
|---|---|
| Only the document is fetched; the `/s/{token}` sub-route switch has exactly `""`, `/variants.json`, `/walkthrough.json`, `/feedback` — no subresource route exists | `PublicHandler.serveShare` |
| Proxied-path CSP: `default-src 'self'; script-src <hash-only>; style-src 'self'; img-src 'self' data:; connect-src 'self'; …` — `writeHeaders` emits the `'nonce-…'` style source iff its `nonce` argument is non-empty, and `serveProxiedArtifact` passes `""` | `writeHeaders`, `serveProxiedArtifact`; spec §4 as amended 2026-08-18 (b) |
| Consequence for upstream styling: external stylesheets are cross-origin → refused by `style-src 'self'`; inline `<style>` and `style=` attributes → refused because no `'unsafe-inline'`, nonce, or hash source exists on that response; fonts fall to `default-src 'self'`; cross-origin images refused by `img-src 'self' data:` | `writeHeaders` |
| `script-src` is hash-sources only; `'self'` is deliberately excluded and re-adding it is forbidden (would render the bundle pin inert) | `scriptSrc`, `writeHeaders` comment; keystone spec §4 / INV-12, 2026-07-28 amendment |
| Every outbound fetch runs the INV-13 guard per redirect hop with a fail-closed cap, pin-and-dial, no proxy env, no keep-alive reuse | `guardedUpstreamFetcher.fetchDocument` / `.get`, `publish.CheckUpstreamOrigin` |
| Inbound artifact GETs are rate-capped per (share, IP) (`artifactLimiter`); guarded outbound fetches are rate-capped per upstream origin across shares (`outboundLimiter`); both default from `config.DefaultPublicPlaneConfig` and are operator-overridable via `WithRateLimits` | `serveArtifact`, `serveProxiedArtifact`, `internal/config/publicplane.go` |
| `variants.json` / `walkthrough.json` sub-routes carry **no** inbound limiter of their own (only the artifact route does) — a new sub-route must not copy that shape | `serveShare` dispatch |
| `readUpstreamDocument` ignores `Content-Encoding` entirely (S6 advisory 1): an unsolicited `br`/`deflate` body with `Content-Type: text/html` would get our tag spliced into compressed bytes and served as HTML. The dev sibling (`ProxyServer.modifyResponse`, `internal/proxy/rewrite.go`) already decodes gzip/deflate/br/zstd before splicing | `readUpstreamDocument` vs `modifyResponse` |
| `ShouldInject` is a `strings.Contains(ct, "text/html")` substring scan (S6 advisory 2) — one shared predicate, about to be load-bearing on more content-type surfaces | `internal/proxy/injector.go` |
| Test seam pattern for the guard exists and observes rather than disables it | `UpstreamSeam` / `WithUpstreamSeam`; `.claude/rules/publish-security-review-lessons.md` §7–§8 |
| Viewer privacy contract on the proxied path today: nothing about the viewer reaches the upstream — no Cookie, no Referer, no X-Forwarded-\*; `Referrer-Policy: no-referrer` on responses | `guardedUpstreamFetcher.get`, `writeHeaders` |

## 2. Option space

Four options were required by the task; each is assessed on security surface
(SSRF, amplification), CSP delta, viewer privacy, and complexity.

### (a) Proxy subresources through a guarded `'self'` route

Rewrite subresource references in the fetched document (and, for nested
references, in fetched CSS) to `/s/{token}/sub/…`; each viewer GET of such a
path triggers a daemon-side fetch through the **same** `CheckUpstreamOrigin`
per-hop guard, size-capped and content-type-allowlisted, served from `'self'`.

- **Security**: largest new surface — every subresource is another guarded
  outbound fetch on an anonymous route. SSRF stays bounded by INV-13 *if and
  only if* the route cannot be fed a viewer-chosen URL (§4's reference-binding
  requirement). Amplification is real (one inbound GET → one outbound fetch,
  multiplied by subresource count) and must ride the existing per-origin
  `outboundLimiter` plus new count/size caps.
- **CSP delta**: none for external resources — they become `'self'` and the
  existing directives admit them. Inline styles still need §3.
- **Privacy**: preserves the shipped contract fully; the upstream sees only the
  daemon.
- **Complexity**: high — URL rewriting in HTML and CSS, a reference-binding
  mechanism, caps.

### (b) Proxy stylesheets only; images/fonts direct or absent

Same machinery as (a) but scoped to `text/css`.

- Cuts the content-type surface but keeps almost all of (a)'s machinery (the
  rewriter, the binding, the caps) while still rendering pages imageless, and
  CSS `url()` references to fonts/images would still dangle. It buys little:
  the marginal cost of admitting `image/*` and `font/*` through an allowlist
  that already exists is small next to the rewriter that (a) and (b) share.

### (c) No proxying; widen CSP directives to the upstream origin

Add the share's upstream origin to `style-src`/`img-src`/`font-src` on the
proxied response, letting the **browser** fetch subresources directly.

- **Security**: smallest server surface — zero new outbound fetches, zero SSRF
  or amplification delta. The directive widening is per-share and derived from
  the published `Upstream.URL` (publisher-named, already INV-13-checked at
  fetch time), never viewer input. `script-src` untouched.
- **Privacy**: breaks the shipped contract. The viewer's browser connects to
  the upstream directly: viewer IP, User-Agent, TLS fingerprint, and timing go
  to a third party the viewer never chose to visit. `Referrer-Policy:
  no-referrer` keeps the token out of it, but the header policy's own comment
  ("nothing about the viewer … is forwarded to it") stops being true for
  subresources. Also functionally weaker: only same-origin subresources are
  admitted; a real site's CDN assets (different origin) still fail, and there
  is no principled stopping point short of `https:` (which would re-open the
  pixel-beacon exfil `img-src 'self' data:` deliberately closed — keystone §4).
- **Complexity**: lowest. Inline styles still need §3.

### (d) Publish-time inlining / snapshotting of critical CSS

Fetch stylesheets once at publish time under INV-13 (the machinery §6a already
specifies for `addScript src`), store them in the revision, serve from `'self'`.

- **Security**: fetches move to the trusted control plane — no anonymous-route
  amplification at all. Strongest posture.
- **Fidelity**: the demo freezes its styling at publish time; a live upstream
  that ships new CSS drifts from its own demo — arguably a feature (immutable
  revision, INV-15-consistent) but it contradicts the point of naming a *live*
  upstream, and images/fonts snapshotted the same way balloon the stored
  revision. Nested/dynamic references (CSS injected by upstream JS — which the
  hash-only `script-src` refuses anyway) are unresolvable at publish time.
- **Complexity**: medium; plus a storage story.

## 3. The style-src decision (answered before the route)

The proxied response must authorise the upstream's inline styles or the page
renders broken no matter what route exists. Candidates:

1. **`style-src 'self' 'unsafe-inline'` on the proxied artifact response
   only** (recommended). Rationale: on this response the only authors of style
   are the upstream (whose entire document we have already chosen to display —
   refusing its inline styles while serving its HTML protects nothing) and the
   publisher (trusted, §0 Actors of the keystone spec). No viewer input can
   reach the document: feedback is never reflected (INV-7), and the walkthrough
   player renders narration as text. `'unsafe-inline'` in `style-src` grants
   **no script execution**, and CSS-driven exfiltration remains bounded by the
   *fetch* directives, which do not widen: `img-src 'self' data:`,
   `connect-src 'self'`, `font-src`→`default-src 'self'` — the same
   containment argument the keystone spec already uses to retire the §5b token
   scan ("`url()`/`@import` fetches are bounded by CSP `img-src`/`default-src`
   instead"). The widening is confined to exactly one response shape and stated
   in the header-policy table, mirroring how the nonce is confined to the
   self-contained shell (2026-08-18 (b) amendment discipline).
2. Per-response hashing of every upstream `<style>` block plus
   `'unsafe-hashes'` for `style=` attributes. Rejected: hashing hostile content
   we display anyway is cost without containment — the hash set would be
   derived from the very bytes it authorises, at serve time, from an untrusted
   origin. It is the §14 lesson inverted: a pin is only meaningful when it is
   derived from *trusted* content; here it would be a content-derived pin of
   *untrusted* content, i.e. `'unsafe-inline'` with extra steps and a new
   failure mode (hash-set drift between fetch and render).
3. Rewriting inline styles out of the document into a served stylesheet.
   Rejected: a full HTML transform on hostile input, strictly more parser
   surface than either alternative.

Note the asymmetry this preserves, which is the whole point of INV-11/INV-12:
`script-src` stays hash-only on **every** public response — upstream and viewer
script still cannot run. Style is presentation of a document we display;
script is capability. The two directives move independently.

## 4. Recommended design: (a) scoped, with bound references

**Recommendation: option (a), scoped to CSS + images + fonts, with
`style-src 'self' 'unsafe-inline'` on the proxied artifact response (§3.1). JS
is explicitly out of scope** — proxying it would serve bytes nothing can
execute (`script-src` is hash-only and must remain so, INV-12); the code
comment on the route must say exactly that, per the task's acceptance criteria.
Option (c) is the fallback if the owner rejects the added surface (§8 Q1), and
(d) remains open as a later *optimisation* (publish-time prefetch into the
same route's cache), not a competing architecture.

### 4a. Reference binding — the route must not be an open relay

A naive `/s/{token}/sub?u=<url>` route lets **any anonymous token holder**
drive daemon fetches to arbitrary `https:` URLs — an open relay bounded only by
the deny-list, exactly what keystone §4a exists to prevent, and a violation of
its premise that fetched origins are **publisher-named**. The route therefore
serves only references the *upstream document itself* (or its CSS) named:

- At splice time, the daemon rewrites each admitted subresource URL in the
  fetched document to `/s/{token}/sub?u=<absolute-url>&sig=<mac>`, where the
  MAC is HMAC-SHA256 over `(shareID, absolute-url)` with a per-daemon key
  (generated at first use, held with the publish store). The viewer can replay
  only URLs the daemon itself emitted for that share; it can mint none.
- Fetched CSS gets the same rewrite applied to `url()` / `@import` before it is
  served (nesting depth cap: 2 — document → CSS → font/image; deeper chains
  refuse loudly).
- The MAC binds to `shareID`, so revoke kills every subresource URL atomically
  with the artifact (INV-4): the token check in `serveShare` already 404s the
  whole family before any signature is examined.
- Rejected alternative — a publish-time manifest of allowed URLs: it cannot
  name URLs that only appear in live-fetched CSS, and it re-introduces (d)'s
  drift problem for a share whose upstream changes its asset names.

### 4b. Per-fetch obligations (all mandatory, none new in kind)

Each subresource fetch reuses `guardedUpstreamFetcher` semantics wholesale:
`CheckUpstreamOrigin` on the origin **and every redirect hop**, fail-closed
redirect cap, pin-and-dial, `Proxy: nil`, no keep-alive reuse, no viewer header
forwarded, whole-fetch timeout. The test seam is `UpstreamSeam` — observed, not
bypassed (`publish-security-review-lessons.md` §7), and refusal tests must
assert the guard's verdict and that the dialer was never reached (§8 of the
same rules file).

### 4c. Content-type allowlist (serve side)

A subresource response is served only when its `Content-Type` media type is in
a closed allowlist: `text/css`, `image/*` **except** `image/svg+xml`, and
`font/*` (+ the legacy `application/font-woff`). Everything else — HTML, JS,
JSON, and notably SVG — is refused loudly (502). SVG is excluded from v1
because it is a document format that can carry script and style; admitting it
safely (forced `Content-Disposition` or a sandboxing per-response CSP) is a
follow-up decision, not a default. `X-Content-Type-Options: nosniff` (already
set by `writeHeaders`) is required on every subresource response so a
mislabeled body cannot be sniffed into an executable type. The match is an
exact media-type comparison after `mime.ParseMediaType`, **not** a substring
scan — which forces the S6 advisory-2 fix first (§5).

### 4d. Caps (amplification is the named risk)

`AGENTS.md` § Exposure Posture already names the amplification asymmetry; a
subresource route multiplies it, so every bound below is load-bearing:

| Cap | Proposed default | Enforcement point |
|---|---|---|
| Inbound subresource GETs | share the existing per-(share, IP) `artifactLimiter` bucket (a page load is 1 document + N subresources, so the default rate rises accordingly — number owner-confirmed, §8 Q5) | new sub-route handler |
| Outbound fetches per origin | existing `outboundLimiter` (subresource fetches draw from the same bucket as document fetches — one origin, one budget) | before each guarded fetch |
| Per-subresource size | 2 MiB, refuse (never truncate) | `readAllCapped`-style bounded read |
| Distinct subresource references rewritten per document | 64; references past the cap are left un-rewritten (they fail under CSP exactly as today) and the refusal is logged | splice-time rewriter |
| CSS nesting depth | 2 | rewriter |
| Fetch timeout | existing `upstreamFetchTimeout` per fetch | fetcher |

Daemon-side response caching (keyed by signed URL, small LRU) is a v2
optimisation; v1 correctness must not depend on it.

### 4e. Prerequisite fixes folded in first (task acceptance criteria)

1. **`Content-Encoding` on the guarded fetch path**: `readUpstreamDocument`
   must refuse or decode encoded bodies before splicing — reusing the decode
   set the dev sibling in `modifyResponse` already handles — and the
   subresource reader inherits the same behaviour.
2. **One shared "safe to splice / safe media type" predicate**: replace the
   `ShouldInject` substring scan with an exact media-type predicate shared by
   the dev injector, `readUpstreamDocument`, and the new allowlist, before the
   content-type surfaces multiply.

## 5. New invariants (numbering continues; none existing are touched)

| # | Invariant | Test shape |
|---|---|---|
| **INV-16** | The subresource route serves only references derived by the daemon from a guarded fetch of that share's publisher-named upstream (document or its CSS). A viewer-composed URL — wrong MAC, foreign shareID, unreferenced target — is refused before any socket opens. Every subresource fetch passes the full INV-13 guard on the origin and every redirect hop, fail-closed. | Forged/absent MAC → 404/403 with zero dial (assert via seam dialer, per lessons §8); a subresource redirecting to `169.254.169.254` is refused at the hop; revoke → subresource URLs 404 atomically with the artifact (INV-4). |
| **INV-17** | Subresource responses are media-type allowlisted (`text/css`, `image/*` minus SVG, `font/*`); HTML, JS, SVG, and everything else refuse loudly. `script-src` remains hash-sources-only on **every** public response — no subresource can be served into a script-executing context. | Upstream answers `text/html`/`application/javascript`/`image/svg+xml` on a subresource fetch → 502, body never relayed; header assertion that no public response's `script-src` ever contains `'self'`, a host source, or `unsafe-*`. |
| **INV-18** | `style-src 'unsafe-inline'` appears on exactly one response shape — the proxied live-upstream artifact document — and nowhere else (self-contained shell keeps its nonce; JSON, feedback, refusal, and subresource responses keep bare `'self'`). The fetch-governing directives (`img-src`, `connect-src`, `font-src`/`default-src`) do not widen anywhere. | Header table test across every response shape; mutation check: removing the widening must fail only the proxied-artifact assertion. |
| **INV-19** | Subresource work is bounded and refusal is loud: per-subresource size cap, per-document reference cap, CSS nesting cap, and both existing rate limiters cover the route. No cap is enforced by silent truncation. | Over-cap body → 502 not a partial body; reference #65 left un-rewritten with a logged refusal; limiter exhaustion → 429/503 with the constant non-oracle message shapes already used. |

## 6. Traceability

| Change | Files | Test tier |
|---|---|---|
| `style-src` widening on proxied response (§3) | `internal/proxy/public_routes.go` (`writeHeaders` gains a per-response style mode, `serveProxiedArtifact`) | `internal/proxy/public_routes_test.go` header-table tests (host-safe) |
| Shared media-type predicate + `Content-Encoding` handling (§4e) | `internal/proxy/injector.go` (`ShouldInject` successor), `internal/proxy/rewrite.go`, `internal/proxy/public_routes.go` (`readUpstreamDocument`) | existing injector/rewrite unit tests extended (host-safe) |
| Subresource route + MAC binding + rewriter (§4a) | `internal/proxy/public_routes.go` (route), new sibling file for the rewriter, `internal/publish` (MAC key alongside the store) | `public_routes_test.go` via `UpstreamSeam` (host-safe); guard-provenance assertions per lessons §8 |
| Caps + config (§4d) | `internal/config/publicplane.go` (+ `docs/configuration.md` follow-up — every new key needs a traced consumer + a non-default-changes-behaviour test, lessons §5) | config round-trip + behaviour tests |
| Real-browser rendering assertion (a proxied page's stylesheet actually applies; upstream script still refused) | `internal/proxy/publish_browser_e2e_test.go` | `chromee2e`-tagged tier, `make test-chrome-e2e` / `make e2e-publish-browser` — this file must be inside the implementing slice's fileScope (lessons §9) |
| Keystone spec amendment (INV-16..19 rows, §4 header table row for the proxied style mode) | `docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md` amendment log | doc slice, swept for coverage-quantifier language (lessons §10/§10a) |
| Behaviour doc update (known-gap §"There is no subresource proxy route" retires **only when the code ships**) | `docs/public-walkthroughs.md` | doc slice, same sweep |

## 7. Explicitly out of scope

- Proxying or executing upstream JS in any form (INV-12; stated in code).
- SVG subresources (excluded by INV-17 pending a sandboxing decision).
- Daemon-side subresource caching (v2 optimisation).
- Any change to the dev plane's `stripFrameDenyHeaders` strip-merge or to the
  self-contained artifact path.

## 8. Open questions for owner

1. **Privacy vs surface**: confirm option (a) over (c) — all subresource
   traffic routes through the daemon, accepting the amplification surface (with
   §4d caps) to keep viewer identity away from the upstream. If viewer→upstream
   direct fetches are acceptable, (c) is a fraction of the work.
2. **`'unsafe-inline'` in `style-src`** on the proxied response (§3.1):
   accept, or require the hash/`'unsafe-hashes'` alternative despite §3.2's
   argument against it?
3. **Reference binding mechanism**: HMAC-signed rewritten URLs (proposed) vs a
   publish-time manifest — the manifest is more auditable but cannot cover
   URLs appearing only in live-fetched CSS.
4. **CSS nesting**: is depth 2 (document → CSS → font/image) sufficient, or
   must `@import` chains resolve deeper?
5. **Cap numbers** (§4d): 2 MiB / 64 refs / shared limiter buckets are
   proposals; confirm or adjust before they are encoded verbatim downstream.
6. **JS exclusion permanence**: recommend permanent (documented in the route's
   code comment); confirm so the comment can say "by design", not "for now".
7. **SVG**: leave excluded until someone asks, or design the sandboxed-serve
   path now?

---

## 9. Amendment 2026-08-19 — open questions resolved, and what shipped

Owner ruling: *follow the spec's recommendations.* Each §8 question is answered
below by this document's own stated lean; Q7, where the document expressed none,
takes the option that grants least.

### 9a. Resolutions

| # | Question | Resolution | Basis |
|---|---|---|---|
| Q1 | Privacy vs surface: option (a) daemon-proxied, or (c) direct viewer→upstream fetches | **(a)**, scoped to CSS + images + fonts. All subresource traffic routes through the daemon; the viewer's IP, User-Agent, TLS fingerprint, and timing never reach the upstream. | §4's recommendation. (c) breaks the shipped privacy contract §1 records, and admits only same-origin assets anyway. |
| Q2 | `'unsafe-inline'` in `style-src` on the proxied response, or per-response hashing | **`style-src 'self' 'unsafe-inline'`, proxied artifact only.** | §3.1. §3.2's hashing is a content-derived pin of *untrusted* content — the same permission with a drift failure mode; §3.3's rewriting is a full HTML transform over hostile input. |
| Q3 | Reference binding: HMAC-signed rewritten URLs, or a publish-time manifest | **HMAC**, over `(shareID, absolute-url, depth)`, key per-daemon. | §4a. A manifest cannot name URLs that appear only in live-fetched CSS, and reintroduces option (d)'s drift. `depth` joined the tuple during implementation (see 9b). |
| Q4 | CSS nesting depth 2, or deeper `@import` chains | **Depth 2** (document → CSS → font/image). | §4d's proposal, unchanged. Deeper chains are the amplification multiplier growing without a matching need. |
| Q5 | Cap numbers | **2 MiB per subresource, 64 distinct references per resource, shared rate buckets** — the existing per-`(share, IP)` inbound bucket and per-origin outbound bucket, unchanged. | §4d as proposed. No new config key was added: an unused knob is a config-authority defect, and the existing buckets already cover the route. |
| Q6 | Is the JS exclusion permanent | **Permanent, and the route's code comment says "by design", not "for now".** | §4/§7. `script-src` is hash-only (INV-12) and stays so, therefore proxied script is bytes nothing can execute; a route serving it would only create the impression that upstream JS runs on a published artifact. |
| Q7 | SVG: leave excluded, or design a sandboxed-serve path now | **Leave excluded** (refused with HTML and JS). | The document expressed no lean, so the least-granting option wins. SVG is a document format carrying script and style; admitting it needs a forced `Content-Disposition` or a per-response sandboxing CSP — a decision, not a default. |

### 9b. Divergences from §4 as planned

1. **`depth` is inside the MAC**, which §4a did not state. Without it a viewer
   could replay a document-level reference as though it had come from a
   stylesheet and walk the chain past the §4d cap; the cap is a security bound,
   so it must be bound. MAC fields are length-prefixed for the same reason —
   otherwise `("ab","c")` and `("a","bc")` collide.
2. **§4e.1 `Content-Encoding`: this plane REFUSES rather than decodes.** §4e
   permitted either. Refusal is the conservative half: a decoder here is a
   decompression-bomb surface an anonymous viewer can aim at a third-party
   origin, whereas the dev sibling's decode path runs where the operator owns
   both ends. Go's transport already solicits and transparently decodes gzip, so
   only *unsolicited* encodings are refused, and loudly (502).
3. **The MAC key is in memory, not persisted with the store.** §4a said "held
   with the publish store". A restart therefore invalidates outstanding
   subresource URLs — which fails closed, because the artifact revalidates on
   every load (`Cache-Control: max-age=0, must-revalidate`) and the fresh
   document carries freshly signed references. Persisting it would add a
   long-lived on-disk secret for no correctness gain.
4. **A stylesheet reached at the depth cap SERVES; only the chain stops.** §4d
   said deeper chains "refuse loudly". The reference cap in the same table
   already had the shape "leave it un-rewritten and log the refusal", and the
   resource itself is allowlisted and bounded, so refusing it outright would
   break a legitimate asset to enforce a bound about its *children*. The refusal
   is logged; the un-rewritten references then fail under CSP exactly as today.
5. **The HTML rewriter is regex-scoped, not a parser.** It rewrites
   `<link rel=stylesheet|icon>` `href`, `<img src>`, and `url()`/`@import`
   inside inline `<style>` blocks and fetched CSS. It only ever replaces an
   attribute *value* with a percent-encoded same-origin path and never inserts
   markup, so a mis-parse leaves a reference un-rewritten — today's behaviour —
   rather than injecting content. `srcset` and `<picture>` are **not** covered;
   those references stay un-rewritten and fail under CSP as before.
6. **No `chromee2e` rendering assertion shipped in this slice.** §6's
   traceability lists one in `internal/proxy/publish_browser_e2e_test.go`. That
   file was outside this task's file scope, and per
   `publish-security-review-lessons.md` §9 a criterion naming a test tier belongs
   to a slice that can reach it. Declared, not silently dropped: the host-safe
   tier covers the guard, the binding, the allowlist, the caps, revoke, and the
   header table; what is not yet asserted is that a real browser *applies* the
   proxied stylesheet.

### 9c. Where it lives

| Concern | File |
|---|---|
| MAC binding (`SubresourceSigner`) | `internal/publish/subresource.go` |
| Reference rewriter + caps | `internal/proxy/public_subresource.go` |
| Route, guarded subresource fetch, allowlist read, `style-src` widening | `internal/proxy/public_routes.go` |
| Shared media-type predicates (S6 advisory 2) | `internal/proxy/mediatype.go` |
| Tests | `internal/proxy/public_subresource_test.go`, `public_csp_table_test.go`, `mediatype_test.go`, `internal/publish/subresource_test.go` |
