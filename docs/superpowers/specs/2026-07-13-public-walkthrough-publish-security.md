# Public Walkthrough Publish — Security Specification

Date: 2026-07-13
Status: Normative (keystone of the walkthrough-publish epic)
Kind: Security design spec (contract). Downstream P-tasks encode these numbers and tables verbatim.

## Amendment log

Invariant numbers are **never reused or renumbered** — a retired invariant keeps
its row and states why it was retired, so downstream tasks that already encode a
number keep resolving it.

| Date | Change | Sections touched |
|---|---|---|
| 2026-07-13 | Original spec. | all |
| 2026-07-27 | **INV-6 retired** (variant ops may carry raw CSS/HTML/JS from the authored revision). **INV-11/INV-12 activated** (live upstream is now genuinely proxied); `script-src` widens to the authored-revision script hash. **INV-13** upstream-origin allowlist (SSRF/open-relay hygiene). **INV-14** always-on demo indicator. **INV-15** token-per-file serve semantics. | §0, §1, §3a, §4, §4a, §5, §5b, §6, §9a, §9c, §11 |

**Explicitly unchanged by the 2026-07-27 amendment:**

- **INV-1 (public traffic never satisfies dev session scope)** stands verbatim.
  Widening what a *variant op* may carry does not widen what the *public plane*
  may reach. Public traffic still resolves to no `scope.Scope` and still cannot
  touch `proc`/`proxy`/`proxylog`/`get_errors`/`get_incidents`/`exec`.
- **INV-4 (a revoked share serves nothing)** stands verbatim. Revoke remains
  atomic, grace-window-free, and cache-proof; §3a's per-file revoke is an
  *additional* revoke trigger, not a relaxation of this one.

## 0. Scope and threat model

This spec adds **publishing** to walkthrough mode. The existing walkthrough demo
(`docs/superpowers/specs/2026-06-20-walkthrough-design.md`) ran entirely inside a
trusted dev session, browser-side, in-memory — publishing was explicitly OUT OF
SCOPE there (§"Out of scope (YAGNI v1)": *"Persisting scripts across daemon
restart"*). This spec turns walkthrough mode into a **public webapp**:

- `agnt` reverse-proxies an external **live upstream URL** (the same always-wrap
  model as `internal/proxy/server.go` + `internal/proxy/injector.go`).
- Into that proxied page it injects a **stripped public bundle** — walkthrough
  player + variant cycler + anonymous feedback widget — and *nothing else*.
- Anonymous viewers reach it behind an **unguessable share token**; the trusted
  publisher (local, session-scoped) can **rotate or revoke** it.
- Anonymous **feedback** is collected as pure data.

### Actors

| Actor | Trust | Plane | How it authenticates |
|---|---|---|---|
| Publisher | trusted | control plane | local dev session, session-scope token (`internal/scope`, `resolveProjectScope`) |
| Anonymous viewer | **untrusted** | public plane | share token only (bearer of a URL) |
| Upstream app | **untrusted third party** | proxied origin | none — treated as hostile HTML/JS |
| Daemon | trusted | both | owns the authoritative store |

### Threats in scope
Token guessing/enumeration; token leakage via logs/referrer/events; a revoked
share still serving; the public bundle exposing dev-only capabilities (proxy
exec, `__devtool` control API, audits, WS); a malicious variant op injecting
JS/HTML/CSS; feedback used as a command channel, XSS reflection, or abuse
amplifier; PII capture; state loss / silent corruption across daemon restart;
public traffic satisfying dev **session scope** and leaking another project's
data (`.claude/rules/daemon-architecture.md` §"Tool session-scoping").

---

## 1. Security invariants (INV-*)

Each is testable. Downstream tasks (§11) must uphold every one.

| # | Invariant | Test shape |
|---|---|---|
| **INV-1** | Public traffic **never** satisfies dev session scope. A public request resolves to *no* `scope.Scope` — it is neither gated, id-scoped, nor debug-exempt (`daemon-architecture.md` §"Tool session-scoping"). It cannot reach `proc`/`proxy`/`proxylog`/`get_errors`/`get_incidents`/`exec` or any project's data. | Public request with a valid share token returns 403/404 on every control/debug verb; unit test asserts no code path maps a share token to a `SessionCode` or `Directory`. |
| **INV-2** | **Deny by default.** Every route not explicitly in the public endpoint matrix (§2) is `404` (unknown) or `403` (known-but-forbidden). No wildcard passthrough of `__devtool/*` control paths to the public plane. | Route table test: fuzz N paths; only matrix rows respond 2xx. |
| **INV-3** | Share tokens are **CSPRNG, ≥256 bits, returned once, hashed at rest** (sha256, plaintext never persisted), and **constant-time verified** (`subtle.ConstantTimeCompare`). | §3 tests. |
| **INV-4** | A **revoked** share serves nothing: artifact route, variant API, and feedback API all die (404) atomically. No grace window, no cache that outlives revoke. | Revoke → immediately GET artifact + POST feedback → both 404. |
| **INV-5** | The public bundle contains **only** RolePublic-allowlisted modules (§9). A forbidden module in the dependency closure **fails the build**, exactly like `TestRoleBundleDependencyClosure` in `internal/proxy/scripts/rolebundle_test.go`. | Build-gate test: add a forbidden dep → test red. |
| **INV-6** | Variant ops are **declarative only** (§6). No `innerHTML`, `<script>`, event-handler attributes, `javascript:`/`data:` URLs, `eval`, `url()`/`expression`/`@import` in CSS. Renderer has no code path that evaluates author-supplied strings as code or markup. | §5/§6 validator rejects each forbidden form; renderer uses `textContent`/`setAttribute` on an allowlist only. |
| **INV-7** | Feedback is **anonymous data, never a command.** It enters a write-only sink; it is size-capped, rate-limited, never reflected unescaped, never interpreted as a control message, and captures no PII beyond an opaque anonymous session id. | POST with control-shaped payload has no side effect beyond an appended feedback row; stored body is inert on read-back. |
| **INV-8** | Published state (variant set, published walkthrough, share token hash, feedback) **survives daemon restart** via a persistent authoritative store — unlike today's in-memory walkthrough/traffic state. Corruption on load **fails loud** (`daemon-architecture.md` §"Silent Failure Prohibition"), never silently serves partial/empty. | Kill+restart daemon → published share still serves; corrupt the store file → load emits a visible error event, does not silently 404. |
| **INV-9** | The share token is **redacted** from every log, event, incident, and outbound `Referer`/`Referrer-Policy` surface. Only its hash prefix (≤8 hex) may appear for correlation. **`Referrer-Policy` alone does NOT protect the token server-side:** the existing proxy traffic log records the full request path by default (`internal/proxy/proxy_handler.go:532` sets `HTTPLogEntry.URL = r.URL.String()`, emitted via `ps.logger.LogHTTP`), which would capture the raw `/s/{token}` path. Therefore publish routes MUST scrub the `/s/{token}` path segment to `hash[:8]` **before** any request/traffic logging — a **P6/P7 obligation**. | grep test over log/event emitters; traffic-log assertion that a public request's logged URL contains only `hash[:8]`, never the full token; `Referrer-Policy: no-referrer` on artifact responses. |
| **INV-10** | **Single source of truth** per artifact (§10). The public plane may only **mutate feedback**; it may **read** the published walkthrough + variant set; it may mutate **nothing** in the control plane. | Write-attempt from public plane on any control artifact → 403; source-of-truth table has exactly one writer per artifact. |
| **INV-11** | Injected-bundle **CSP is authoritative** over the third-party upstream. The existing frame-header precedent (`stripFrameDenyHeaders`, `internal/proxy/rewrite.go` ~L226-267) is a **STRIP-MERGE**, **not** a replace: it deletes `X-Frame-Options` and removes **only** the `frame-ancestors` directive, **preserving every other upstream CSP directive** (its own comment: *"only the frame-ancestors directive is removed from CSP (other directives are preserved)"*). That path is **NOT sufficient** for the public plane — reusing it would keep a hostile upstream's `script-src 'unsafe-inline'`/`'unsafe-eval'`, so upstream inline JS would execute on the public plane and **defeat INV-6**. The public plane therefore does a **wholesale replace** (INV-12): forbid `unsafe-inline`/`unsafe-eval`, pin the bundle to `self` + content hash. | Response header assertion (see INV-12); injected inline handlers are refused by the browser. |
| **INV-12** | On the public publish plane the proxy MUST **delete** the upstream `Content-Security-Policy` **and** `Content-Security-Policy-Report-Only` headers **wholesale** and **SET** its own — `Header.Del` then `Header.Set`, **never a merge**. The set policy is `script-src 'self' 'sha256-<bundle>'` (no `unsafe-inline`/`unsafe-eval`), `connect-src 'self'`, etc. (§4). Reusing the `stripFrameDenyHeaders` strip-merge path for CSP on the public plane is **forbidden** — it would preserve upstream `script-src`. | Given an upstream response carrying `Content-Security-Policy: script-src 'unsafe-inline'`, the served public response contains **no** `unsafe-inline` and carries **only** the agnt-set CSP. |

---

## 2. Endpoint matrix

Two disjoint planes. **Deny-by-default: any route absent from both tables is 404 (unknown) / 403 (forbidden control path on the public plane).**

### 2a. Control plane — trusted publisher (local, session-scoped)

Reached only over the dev session; every row routes through `resolveProjectScope`
(`daemon-architecture.md`). Exposed as an MCP tool (`publish`) + local daemon
verbs, **never** on the public listener.

| Route / verb | Methods | Auth | Rate limit | Reads / Writes |
|---|---|---|---|---|
| `publish create` | POST | session scope | n/a (local) | W: variant set, published walkthrough, mints token (returns plaintext **once**) |
| `publish rotate` | POST | session scope | n/a | W: new token hash; old hash invalidated |
| `publish revoke` | POST | session scope | n/a | W: marks share dead (INV-4) |
| `publish status` | GET | session scope | n/a | R: share state, feedback count, never the token plaintext |
| `publish feedback list` | GET | session scope | n/a | R: feedback rows (control-side only) |

### 2b. Public plane — anonymous viewer

Served on the proxy listener. Share token in the **path segment**, not a query
string (query strings leak via `Referer` and proxy logs). No cookies, no CORS
credentials, deny-by-default.

| Route | Methods | Auth | Rate limit | Reads / Writes |
|---|---|---|---|---|
| `GET /s/{token}` | GET, HEAD | valid token | 60 req/min/IP | R: artifact HTML shell (injects RolePublic bundle); no write |
| `GET /s/{token}/variants.json` | GET | valid token | 60 req/min/IP | R: published variant set (immutable snapshot); no write |
| `GET /s/{token}/walkthrough.json` | GET | valid token | 60 req/min/IP | R: published walkthrough script; no write |
| `POST /s/{token}/feedback` | POST | valid token | **10 req/min per (token,IP)**, burst 5 | W: append one feedback row (INV-7); no read-back |
| `GET /__devtool/inject.<hash>.js` (RolePublic asset) | GET | valid token (referer-bound to `/s/{token}`) | 60 req/min/IP | R: content-addressed public bundle only |
| everything else (`/__devtool/*` control, WS `/__devtool/ws`, `proxy exec`, upstream-arbitrary control paths) | any | — | — | **403/404 — INV-1/INV-2** |

**WebSocket:** the public plane exposes **no** WS. The dev channel (`ws_handler.go`,
`ws_session.go`) is control-plane only; a public request to the WS upgrade path is 403.

---

## 3. Share-token rules

| Property | Rule |
|---|---|
| Source | `crypto/rand.Read` (CSPRNG). **Never** `math/rand`. Repo crypto precedent: `crypto/rand` currently appears **only in `internal/sshclient` tests** (`hostkey_test.go`, `testfixture_test.go`) — **P6 introduces the first PRODUCTION use** of `crypto/rand`. Production `crypto/sha256` precedent lives in `internal/incident/blob_store.go` and `internal/store/url.go`. |
| Entropy | **256 bits** (32 bytes). Encoded `base64.RawURLEncoding` → 43-char URL-safe string. Unguessable: 2^256 keyspace defeats enumeration; §2b rate limit caps online guessing. |
| Returned once | Plaintext token is returned **only** from `publish create` / `publish rotate`. It is never stored, never re-derivable, never in `publish status`. Lost token ⇒ rotate. |
| Hash at rest | Persist `sha256(token)` hex only (`daemon-architecture.md` never persists secrets; mirror `internal/incident/blob_store.go` / `internal/store/url.go` sha256 use). Plaintext never touches disk, logs, or events. |
| Verify | Look up by hash; compare with `subtle.ConstantTimeCompare(want, got) == 1`. **New primitive for this repo** (grep shows no existing `ConstantTimeCompare`) — introduced here, pinned by P6. No early-return string `==`. |
| Rotate | Mint a fresh 256-bit token, replace the stored hash atomically, invalidate the prior hash in the same write. In-flight requests bearing the old token 404 immediately after commit. |
| Revoke | Mark the share dead (tombstone, not delete — so an audit trail of "was published, now revoked" survives). Artifact + variants + walkthrough + feedback routes all 404 (INV-4). |
| Redaction | Token never appears in `debug.Log`, session log, incident events, proxy traffic log, or `Referer`. Correlation uses `hash[:8]` only (INV-9). **The proxy traffic log logs full request paths by default** (`proxy_handler.go:532` → `HTTPLogEntry.URL = r.URL.String()`), so publish routes MUST scrub the `/s/{token}` path segment to `hash[:8]` **before** the traffic log records it (P6/P7). Artifact responses also set `Referrer-Policy: no-referrer` so the token path never leaks to the upstream's third-party subresource requests — but that header is client-side only and does **not** cover the server-side traffic log. |

---

## 4. HTTP header policy

Injected public bundle runs over a **hostile third-party upstream**; the proxy is
authoritative for these headers and **replaces** whatever the upstream sent.
**Do not** reuse the frame-header precedent for CSP: `stripFrameDenyHeaders`
(`internal/proxy/rewrite.go`) is a **strip-merge** — it deletes `X-Frame-Options`
and strips only `frame-ancestors`, *preserving all other upstream CSP directives
(including a hostile `script-src 'unsafe-inline'`)*. For the public plane the CSP
(and `Content-Security-Policy-Report-Only`) is **deleted wholesale and re-set**
(`Del` then `Set`, never merged) — INV-11 / INV-12.

| Header | Value (public plane) | Why |
|---|---|---|
| `Content-Security-Policy` | `default-src 'self'; script-src 'self' 'sha256-<bundle-hash>'; style-src 'self' 'nonce-<n>'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'` | No `unsafe-inline`, no `unsafe-eval` (INV-6/INV-11). Set **wholesale**: `Header.Del` the upstream `Content-Security-Policy` **and** `Content-Security-Policy-Report-Only`, then `Header.Set` this value — **never merged** into upstream directives (INV-12); the `stripFrameDenyHeaders` strip-merge path must **not** be reused here. Bundle pinned to `self` + content hash (matches `injector.go` content-addressed asset path). `connect-src 'self'` blocks feedback exfil to third parties. `img-src` is `'self' data:` (**`https:` deliberately dropped**) to block pixel-beacon exfil via upstream `<img>` tags — chosen default; tradeoff: upstream images genuinely needed must be **proxied through `self`**. |
| `X-Frame-Options` / `frame-ancestors 'none'` | deny | The published artifact is the top document; it is not itself embeddable elsewhere. |
| `Referrer-Policy` | `no-referrer` | Token in path must never leak to upstream subresource hosts (INV-9). |
| CORS (`Access-Control-Allow-Origin`) | **absent** on artifact + walkthrough/variants; feedback `POST` is **same-origin only** (no ACAO). | No cross-origin reads; no credentialed CORS. Same-origin form-post needs no ACAO. |
| `Cache-Control` (artifact/variants/walkthrough) | `public, max-age=0, must-revalidate` (immutable snapshots may use content-addressed `max-age=31536000, immutable` for the `inject.<hash>.js` asset only) | Revoke must take effect immediately (INV-4) — no stale artifact cached past revoke. The hashed JS asset is safe to cache long because revoke kills the `/s/{token}` route that references it. |
| `Cache-Control` (feedback) | `no-store` | Feedback POST responses are never cached. |
| `Set-Cookie` | **never** | No sessions, no auth cookies on the public plane; the anonymous session id (§7) lives client-side only. |

---

## 5. Concrete limits table

P2's validators encode these **verbatim**. Reject (422) anything exceeding a limit; never truncate silently.

| Limit | Value | Rationale |
|---|---|---|
| Max variants per set | **12** | A cycler UI stays usable; caps snapshot size. |
| Max steps per walkthrough | **50** | Matches the linear-step demo model; bounds payload. |
| Max selector length | **256** chars | Long enough for real nested selectors, short enough to bound the parser. |
| Selector charset / grammar | Allowlist grammar in §5a. | Blocks script-y selector tricks. |
| Max style-patch size | **4096** bytes (per variant) | Bounds CSS parse cost. |
| Allowed CSS properties | Allowlist (§5b). | Deny-by-default; blocks behavioral CSS. |
| Forbidden CSS tokens | `url(`, `expression(`, `behavior`, `-moz-binding`, `@import`, `javascript:` | Classic CSS-exfil / CSS-injection vectors. |
| Max text length (setText) | **2048** bytes UTF-8 | Bounds DOM text writes. |
| Max URL length | **2048** chars | Standard practical URL cap. |
| Allowed URL schemes | **`https:` only** (upstream target + any op-supplied URL). No `http:`, `data:`, `javascript:`, `file:`, `blob:`. | Public webapp; TLS mandatory; blocks JS/data URL injection. |
| Max feedback body | **4096** bytes | Anti-abuse; a comment, not a document. |
| Feedback rate | **10 req/min per (token,IP)**, burst **5** | Anti-spam without blocking a real reviewer. |
| Public read rate | **60 req/min per IP** | Serves a normal browser page load; caps token brute-force + scraping. |
| Feedback retention | **500 rows per share** OR **90 days**, whichever first; oldest evicted (ring, mirrors incident inbox band cap in `daemon-architecture.md`). | Bounds storage; privacy-driven expiry. |

### 5a. Allowed selector grammar (deny-by-default)

Accept **only** this restricted CSS-selector subset; reject everything else (422):

```
selector      := compound ( combinator compound )*        # max depth 6
combinator    := WS | '>'                                  # descendant / child only
compound      := ( type | '*' )? ( '.' ident | '#' ident | attr | pseudo )*
attr          := '[' ident ( ( '=' | '^=' | '$=' | '*=' ) quoted )? ']'
pseudo        := ':' ( 'first-child' | 'last-child'
                     | 'nth-child(' INT ')' )              # allowlist only
ident         := [A-Za-z_][A-Za-z0-9_-]*                   # no escapes, no unicode ranges
```

**Forbidden:** `:has()`, `:is()`, `:where()`, `:not()`, sibling combinators
(`~`, `+`), pseudo-elements (`::before` …), namespace `|`, functional pseudos
other than `nth-child(INT)`, comments, `@`-anything, whitespace-smuggled tokens.
Rationale: `:has()`/`:is()` enable expensive or selector-injection tricks; the
allowlist is small, auditable, and sufficient for "point at one element."

### 5b. Allowed CSS property allowlist (applyStyle)

Layout/paint-safe only — `color`, `background-color`, `background`
(color/gradient values only, **no `url()`**), `border*`, `outline*`, `padding*`,
`margin*`, `font*`, `text-*`, `line-height`, `letter-spacing`, `opacity`,
`display`, `visibility`, `width`/`height`/`min-*`/`max-*`, `box-shadow`,
`border-radius`, `transform`, `transition`. Values re-validated against the
forbidden-token list in §5. Any property not on the allowlist ⇒ 422.

---

## 6. No-arbitrary-JS/HTML rule — the declarative op set

Variant ops are a **closed, declarative vocabulary**. The renderer is a switch over
these op types; there is **no** op that takes a code or markup string.

| Op | Signature | Renderer action | Guards |
|---|---|---|---|
| `setText` | `{op:"setText", selector, value}` | `el.textContent = value` | value ≤2048 B; **never** `innerHTML` |
| `setAttribute` | `{op:"setAttribute", selector, name, value}` | `el.setAttribute(name, value)` | `name` ∈ attr allowlist (`class`,`id`,`title`,`alt`,`aria-*`,`data-*`,`href`,`src`); `href`/`src` value must be `https:` (§5); **event-handler attrs (`on*`) forbidden** |
| `replaceClass` | `{op:"replaceClass", selector, from, to}` | `classList.replace(from,to)` | idents only |
| `addClass`/`removeClass` | `{op:"addClass", selector, value}` | `classList.add/remove` | ident only |
| `applyStyle` | `{op:"applyStyle", selector, props:{...}}` | `el.style[prop]=val` per allowlisted prop | §5b allowlist; forbidden-token re-check |
| `setImageSrc` | `{op:"setImageSrc", selector, url}` | `img.src = url` | `https:` only, ≤2048 chars |

**Explicitly forbidden (validator rejects; renderer has no path for them):**
`innerHTML`/`outerHTML`/`insertAdjacentHTML`, `<script>` or any tag string,
`on*` event-handler attributes, `javascript:`/`data:`/`vbscript:` URLs,
`eval`/`Function`/`setTimeout(string)`, `style` values containing
`url()`/`expression`/`@import`, DOM-node insertion from author strings.
INV-6 pins that the renderer dereferences author input only through
`textContent`, `setAttribute` (allowlist), `classList`, and `style[prop]`.

---

## 7. CSRF / abuse / privacy / retention (feedback)

| Concern | Rule |
|---|---|
| Auth | Feedback is **anonymous** — the share token authorizes the *plane*, not a user. No login, no cookie. |
| CSRF | Same-origin only (`form-action 'self'`, no CORS creds). A cross-site auto-post carries no credentials and gains nothing (feedback is not privileged); rate limit + size cap bound abuse. Optionally a per-artifact origin check on `Origin`/`Sec-Fetch-Site` (reject `cross-site`). |
| Anti-abuse | Rate limit 10/min per (token,IP) burst 5 (§5); body cap 4096 B; retention ring 500/90d. Excess ⇒ 429, not a crash. |
| No reflection / XSS | Stored feedback is **inert**: escaped on any control-plane read-back (`publish feedback list`), never echoed into the public artifact. INV-7. |
| PII | Capture **only**: feedback body, a **client-generated opaque anonymous session id** (random, cycler-scoped, not a fingerprint), server timestamp, and the truncated IP hash used *solely* for rate-limiting (`sha256(ip+daily-salt)[:8]`, not stored with the row). **No** raw IP, user-agent string, cookies, referrer, or geolocation persisted. |
| Retention | 500 rows/share or 90 days, oldest evicted. Revoke tombstones the share and **purges** its feedback. |

---

## 8. Revoke / restart semantics

| Concern | Rule |
|---|---|
| Revoke = dead | On `publish revoke`, the share is tombstoned; `GET /s/{token}`, `variants.json`, `walkthrough.json`, and `POST feedback` all return 404 **atomically** (single store write, no partial state). INV-4. No grace window; `Cache-Control: max-age=0, must-revalidate` on the artifact prevents a browser serving a cached copy past revoke. |
| Restart survival | Published shares live in a **persistent authoritative store** (not daemon in-memory cache). After daemon restart, an un-revoked share still serves — a **deliberate departure** from the demo model (`2026-06-20-walkthrough-design.md` kept all state in-memory; `daemon-architecture.md` §"Data Ownership" treats daemon memory as cache only). The publish store is the **first daemon-side artifact whose source of truth is its own on-disk record**, not rebuilt-from-config and not evicted on session end. See §10 + Deviations. |
| Corruption fails loud | Unreadable/corrupt store on load emits a **visible error event** through the incident/session-log path (`daemon-architecture.md` §"Silent Failure Prohibition": `debug.Log` alone is insufficient) and refuses to serve that share — it does **not** silently 404 or serve an empty artifact. A per-record checksum detects tampering; a bad checksum tombstones that one record and surfaces the error, leaving healthy records serving. |
| Atomicity | Store writes use the repo's rename-on-write discipline (write temp, fsync, atomic rename) so a crash mid-write never leaves a half-written share/token. |

---

## 9. Public-bundle allowlist + forbidden-capabilities

A **new frame role `RolePublic`** is added to `moduleRole` / `includeInRole` in
`internal/proxy/scripts/embed.go`, and `TestRoleBundleDependencyClosure`
(`rolebundle_test.go`) is extended to cover it. This is the **HARD split** — a
compile/test-time exclusion, **not** a runtime flag. If any allowed module's
declared dependency closure pulls in a forbidden module, the build gate goes red
(INV-5), exactly as the existing chrome/content split enforces.

### 9a. Allowed into RolePublic

| Module (new/derived) | Purpose |
|---|---|
| `core` (public subset) | frame-role bootstrap, shadow-root mount primitive — **without** the `__devtool` control API literal |
| `frames` (public subset) | role resolution only (no cross-frame exec/registry control) |
| `variant-render` (**new**) | the §6 declarative op renderer |
| `variant-cycler` (**new**) | the public variant-switch UI |
| `walkthrough-view` (**new**) | read-only player — narration cards + highlight; **no** MCP/exec forwarding (unlike chrome-only `walkthrough.js` / `walkthrough-proxy.js`) |
| `feedback-client` (**new**) | posts to `POST /s/{token}/feedback`; `connect-src 'self'` only |
| `ui-tokens`, `toast` (public subset) | styling + inert notices |

### 9b. Forbidden from RolePublic (build-gate fails if pulled in)

`api.js` (`window.__devtool` control API), `core` control surface,
`ws_*` / `session.js` dev WebSocket, `proxy exec` bridge, `inspection`,
`tree`, `visual`, `layout`, `layout_diagnose`, `interactive`, `capture` /
`snapshot-helper` / `html2canvas-pro`, `accessibility` / `axe-core`, all
`audit-*`, `design.js` / `style-editor` / `override-store` / `store`, `indicator*`
(dev panel + hotkeys), `sketch`, `wireframe`, `mutation`, `transform`,
`recorder`, `voice`, `authbreakout` (dev auth flow), `diagnostics`,
`responsive*`, any `eval`/`Function` path.

**Enforcement:** the dependency-closure test asserts the RolePublic bundle's
transitive closure ∩ forbidden-set = ∅. Adding a forbidden edge without
reclassifying fails CI (the `TestUnscopedCallSites`-style pinned-set pattern
from `daemon-architecture.md`). The public bundle is served content-addressed at
`/__devtool/inject.<hash>.js` for `RolePublic` (its own hash flavour, per
`injector.go`'s per-role cached asset map).

---

## 10. Control-plane vs data-plane + source-of-truth

| Artifact | Single source of truth | May mutate | May read | Plane |
|---|---|---|---|---|
| Variant set | persistent publish store (record) | publisher (control) | publisher; viewer (immutable snapshot) | control writes / public reads |
| Published walkthrough | persistent publish store (record) | publisher (control) | publisher; viewer (immutable snapshot) | control writes / public reads |
| Share token (hash) | persistent publish store (record) | publisher (`create`/`rotate`/`revoke`) | never returned (hash only) | control only |
| Feedback rows | persistent feedback store (append-only ring) | **anonymous viewer (append only)** + publisher (purge on revoke/retention) | publisher (`feedback list`) | **public writes DATA / control reads** |

**Command/data split (INV-7/INV-10):** the public plane's *only* write is an
append of one inert feedback row. It issues **no commands** — it cannot mutate a
variant set, walkthrough, or token, and it cannot read another project's data
(INV-1). Feedback is **data crossing a trust boundary**, validated + size-capped +
escaped, never interpreted as a control message. This mirrors
`daemon-architecture.md` §"Data Ownership": one writer per artifact, cache never
outranks the authoritative record.

---

## 11. Downstream binding

Traceability from invariant/limit to the enforcing P-task.

| Enforces | Invariants / limits | P-task |
|---|---|---|
| Variant/walkthrough/op **schemas + validators** (§5, §5a, §5b, §6) | INV-6; all §5 limits | **P2** — schemas & validators |
| **RolePublic bundle** split + dependency-closure build gate (§9) | INV-5 | **P4** — public bundle |
| **Share tokens** — CSPRNG, 256-bit, sha256-at-rest, constant-time verify, rotate/revoke, redaction (§3) | INV-3, INV-9 | **P6** — token store |
| **Public routes** + endpoint matrix + deny-by-default + header policy + scope isolation (§2, §4) | INV-1, INV-2, INV-10, INV-11, INV-12 | **P7** — public routes |
| **Public-plane CSP wholesale replace** — `Header.Del` upstream `Content-Security-Policy` + `Content-Security-Policy-Report-Only`, then `Header.Set` agnt policy; **no strip-merge reuse** of `stripFrameDenyHeaders` (§4) | INV-11, INV-12 | **P7** (rule) / **P10** (upstream-`unsafe-inline` e2e assertion) |
| **Token log-scrub** — scrub `/s/{token}` to `hash[:8]` before traffic/request logging (§3, §9) | INV-9 | **P6/P7** |
| **Feedback** sink — anonymous, rate-limited, size-capped, inert, retention (§7) | INV-7 | **P8** — feedback |
| **Persistence + revoke/restart** — authoritative store, atomic revoke, corruption-fails-loud (§8) | INV-4, INV-8 | **P6/P9** — publish store |
| **End-to-end**: token guess, revoke-kills-all, forbidden-module build fail, XSS/JS-injection attempts, **upstream `unsafe-inline` CSP stripped** (INV-12), restart survival | INV-1…INV-12 (integration) | **P10** — e2e |

---

## Deviations from epic assumptions

Flagged where repo precedent disagrees with an implicit epic assumption, per task
instruction (note rather than silently follow):

1. **Persistence is a genuine architectural first, not a free addition.**
   `daemon-architecture.md` §"Data Ownership" is emphatic that daemon in-memory
   state is *cache, never authority*, the script registry is *rebuilt from config
   / never persisted across sessions*, and blob/inbox state is *best-effort,
   evicted on session end*. The published-share store contradicts this by design:
   it is the **first daemon-side artifact whose on-disk record IS the source of
   truth** and which must **outlive the session** (INV-8). Downstream P6/P9 must
   add this as a new, explicitly-authoritative store — it cannot reuse the
   ephemeral registry/cache patterns. Reviewers should scrutinize it as a
   deliberate exception, documented here.

2. **The demo spec explicitly deferred persistence.**
   `2026-06-20-walkthrough-design.md` lists *"Persisting scripts across daemon
   restart"* under "Out of scope (YAGNI v1)." Publishing **requires** it. This
   spec supersedes that YAGNI line **for published shares only**; the in-session
   demo walkthrough stays in-memory as designed.

3. **Public plane needs a new scope classification, not an existing bucket.**
   `daemon-architecture.md` §"Tool session-scoping" classifies every verb as
   gated / id-scoped / debug-exempt — **all three assume a dev session**. Public
   traffic fits none: it must resolve to *no* `scope.Scope` and be structurally
   incapable of satisfying `resolveProjectScope` (INV-1). P7 must add a **fourth
   category — "public, unscoped-but-not-`Unscoped()`, deny-by-default"** — kept
   entirely off the session-scope chokepoint rather than passing an
   `Unscoped(reason)` (which would *match every project* — the exact opposite of
   what's wanted).

4. **`subtle.ConstantTimeCompare` is new to this codebase.**
   A grep (`internal/`) finds `crypto/rand` (sshclient) and `crypto/sha256`
   (incident/store/overlay) but **no** `subtle.ConstantTimeCompare`. INV-3
   introduces it; P6 owns the first use, and a test must pin that token verify
   never falls back to a plain `==`/`bytes.Equal` early-return.
