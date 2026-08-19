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
| 2026-07-27 (b) | **Contradiction repair inside the operative tables** — no invariant added, retired, or renumbered. (1) §6's `applyStyle` row still cited the retired §5b property allowlist and a forbidden-token re-check, which §5b, §5 and §6a all forbid enforcing; it now cites only the surviving §5 size cap and §5a selector grammar and names §5b as non-enforced guidance. (2) §6a's `addScript src` was resolvable only by adding a host source to `script-src`, which INV-12 forbids; **decision: `src` is a publish-time fetch input, not a runtime attribute** — the daemon fetches it under §4a/INV-13, inlines the body, and pins its hash (INV-12), so the served revision emits no `<script src>` and CSP is never widened to a host source. Rejected alternative: deleting `src` outright — it is the cheaper edit, but it removes a capability the epic exists to demo (third-party demo scripts), and §4a already treats `addScript src` as an origin the **daemon fetches**, so inlining reuses machinery INV-13 mandates anyway rather than deleting a feature to dodge a wording conflict. SRI is moot under this choice: nothing is loaded by URL at render time. (3) §11's P10 e2e row scoped coverage to INV-1…INV-12 and now covers INV-13/INV-14/INV-15. | §6, §6a, §11 |
| 2026-07-28 | **`script-src` is hash-sources only — `'self'` removed** — no invariant added, retired, or renumbered. *Was:* `script-src 'self' 'sha256-<bundle>' 'sha256-<authored>'…`. *Now:* `script-src 'sha256-<bundle>' 'sha256-<authored>'…`. **Rationale:** the spec text was **weaker than the shipped policy** (`internal/proxy/public_routes.go`, `writeHeaders`/`scriptSrc`, commit `38627aad`), which never emitted `'self'`. Source expressions are a **union**, so `'self'` would authorise every same-origin script URL and make the bundle hash pin **inert** — the spec was describing a hole the code does not have. The pin works without `'self'` because the single external script is served as `<script src="…" integrity="<bundle-hash>" crossorigin="anonymous">` with the same hash quoted into `script-src` (CSP3 external-hash matching), and authored bodies are inline (`script.textContent = op.code`), which `'self'` never governed. Nothing regresses: no other same-origin JS is served on the public plane. The exclusion is stated **explicitly** in §4 and INV-12 rather than silently dropped, so a future slice cannot "restore" `'self'` as a tidy-up and re-open the gap. Rejected alternative: relaxing the code back to §4 — it would trade a strictly stronger shipped policy for a stale sentence. | §4, INV-11, INV-12 |
| 2026-08-18 | **`addScript` (both `code` and `src`) is REFUSED at publish on a published walkthrough — INV-14 operator decision, option (a)** — INV-14 text unchanged; this is an *enforcement*, not a re-statement of the invariant. *Was:* `addScript` with an inline `code` body published and executed under its pinned `script-src` hash; INV-14's blanket "no publisher-reachable input can remove, hide, or blank the indicator" did **not** hold against it (task `01KYQFAZRH` confirmed three real-Chrome defeats — id squatting, re-assert-budget exhaustion, `Node.prototype.appendChild` monkeypatch — all from a same-realm authored script; not fixable by hardening the module). *Now:* `PublishedWalkthrough.Validate` (`internal/publish/validate.go`) refuses the whole artifact if any variant op is `addScript`, naming INV-14 and a remedy. **Rationale:** a published walkthrough is served on the public plane AS the artifact document, always carrying the mandatory disclosure indicator (§9c/INV-14); an authored script in the badge's own realm can defeat the disclosure, and same-realm script beats same-realm script. Refusing authored script on exactly the badge-mandatory shares preserves INV-14 as written. The refusal is at the `PublishedWalkthrough` boundary, not the op/variant-set validator, so a bare variant set (never served) stays permissive; the store's read path does not re-validate, so pre-gate revisions are unaffected. Operator (user) chose this over (b) moving the disclosure out of the page realm (architecturally too big — the public plane serves the artifact AS the document, S6/S7) and (c) amending INV-14 to a named input class (leaves the disclosure defeatable). Both op forms refused: `src` was already refused at `Op.Validate` for the unrelated unimplemented-fetch reason (2026-07-29 row); on a public share the operative reason is now the disclosure. | §6a, §9c, §11, INV-14 |
| 2026-07-29 | **`addScript src` is REFUSED at publish until the §6a fetch half exists** — no invariant added, retired, or renumbered; the 2026-07-27 (b) decision stands as the recorded plan. *Was:* `Op.Validate` accepted an `addScript` op carrying `src`, gating it on scheme/length only and deferring the §4a resolved-address checks and the inlining fetch to a publish pipeline. *Now:* `Op.Validate` **rejects** `src` with an actionable error naming that publish-time fetching is not implemented and pointing the author at an inline `code` body. **Rationale:** the fetch half was never built, so `src` was a **validated input with no consumer** — nothing reads `Op.Src`, `authoredScriptCSPHashes` contributes no `'sha256-'` source for such an op, and `variant-engine.js` refuses it at render time. The op therefore validated, published, and reported success, after which the script silently never ran with **no signal to the author at any point** — the failure mode `.claude/rules/publish-security-review-lessons.md` §5 names (a parsed-but-inert input is worse than an absent one, because it looks configured and is not). Refusing at the source converts a silent miss into a loud publish error and costs nothing that ever worked. The renderer's refusal is **kept** as defence in depth: two layers refusing is correct, and the renderer also covers revisions published before this gate. Rejected alternative: building the guarded publish-time fetch now (dial-time binding, per-hop redirect re-check, fail-closed cap, inline, hash-pin) — that is a feature slice with a real security surface, not a cleanup, and building it speculatively inside a defect drain is the over-engineering the minimal-code ladder exists to prevent. Also rejected: deleting `src` from the op schema — that would discard the §6a decision rather than record it as pending. | §6a |
| 2026-08-18 (b) | **`style-src` is `'self'` unconditionally; the `'nonce-<n>'` source is added on exactly one response** — no invariant added, retired, or renumbered. *Was:* §4 showed `style-src 'self' 'nonce-<n>'` **unconditionally** (header value string and the "keeps its nonce" prose). *Now:* the value shows `style-src 'self'[ 'nonce-<n>']` and the notes state the nonce source is present **only** on the self-contained artifact shell (`serveSelfContainedArtifact`), with bare `style-src 'self'` on every other response — the proxied-upstream artifact, refusal/rate pages, variants/walkthrough JSON, and **all feedback responses**. **Rationale:** the spec **overstated** the shipped policy (`internal/proxy/public_routes.go`, `writeHeaders`, `nonce != ""` branch): `writeHeaders` emits the nonce source iff its `nonce` argument is non-empty, and only `serveSelfContainedArtifact` passes a non-empty one — it is the single response that appends an inline `<style>` the nonce authorises. Feedback and the other artifact responses append no inline `<style>` of ours, so authorising a nonce source there would grant capability nothing uses (`newNonce` also fails closed to `""`). Same claim-shape class as the 2026-07-28 `script-src`/`'self'` row, one directive over: normative text asserting a directive shape the code does not always produce. Verified while here that the remaining directives (`default-src`, `img-src`, `connect-src`, `frame-ancestors`, `base-uri`, `form-action`, `object-src`) are unconditional literals identical in value AND order on the artifact and feedback paths; `script-src`'s authored-hash widening is confirmed to apply only on the two authored-content paths (`serveProxiedArtifact`, `serveSelfContainedArtifact`). Also corrected §4's inline-body snippet identifier `script.textContent` → `scriptEl.textContent` (`variant-engine.js`), semantics unchanged. Rejected alternative: making the code always emit a nonce so the spec's unconditional shape becomes true — that widens `style-src` on responses with no inline style for no benefit, trading a strictly-minimal shipped policy for a tidier sentence. | §4 |

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
  **ACTIVE as of 2026-07-27.** The first pass of this epic narrowed publishing to
  self-contained artifacts with no live origin, which left INV-11 and INV-12
  written but dormant — nothing hostile was being proxied, so nothing had to
  strip a hostile CSP. Live upstream is now genuinely in scope, so **INV-11 and
  INV-12 are load-bearing, not aspirational**, and P7/P10 must gate on them. The
  upstream origin a publisher may name is constrained by §4a (INV-13).
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
exec, `__devtool` control API, audits, WS); ~~a malicious variant op injecting
JS/HTML/CSS~~ (**retired 2026-07-27 — see INV-6**; variant content is
publisher-authored, so it sits on the trusted side of the boundary and its
containment is CSP's job, not the validator's); an **untrusted upstream origin**
being used to reach private/link-local/metadata addresses through the proxy
(INV-13); feedback used as a command channel, XSS reflection, or abuse
amplifier; PII capture; state loss / silent corruption across daemon restart;
public traffic satisfying dev **session scope** and leaking another project's
data (`.claude/rules/daemon-architecture.md` §"Tool session-scoping").

**Not in the threat model:** a publisher deceiving their own viewers. The
publisher is trusted by construction (they hold the dev session). Deception is
addressed by disclosure — the always-on demo indicator (INV-14) — not by any
security control in this spec; see §4a for why the origin allowlist in
particular must not be mistaken for an anti-phishing measure.

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
| **INV-6** | **RETIRED 2026-07-27.** *Was:* variant ops are declarative only — no `innerHTML`, `<script>`, event-handler attributes, `javascript:`/`data:` URLs, `eval`, `url()`/`expression`/`@import` in CSS. **Now:** variant ops **may carry raw CSS, raw HTML, and script** sourced from the authored revision (§6). **Rationale:** INV-6 defended against *author-supplied* strings, but the author here is the **publisher — a trusted actor** holding the dev session (§0 Actors), not the anonymous viewer. It was guarding the wrong boundary, and the cost was real: a declarative-only op vocabulary cannot express the visual variants publishing exists to demo. The genuinely untrusted inputs are unaffected — **viewer** feedback stays inert (INV-7) and the **upstream** stays hostile (INV-11/INV-12). Containment of publisher-authored script moves to CSP: it executes only if it matches the authored-revision script hash pinned in `script-src` (INV-12), so nothing the upstream or a viewer injects can run. Size caps and the selector grammar (§5, §5a) survive as parse/abuse bounds and are **not** retired. | *(no invariant test; the retirement is asserted negatively)* §6 raw-content ops round-trip unmodified through the validator, and a script whose hash is absent from `script-src` is refused by the browser — see INV-12. |
| **INV-7** | Feedback is **anonymous data, never a command.** It enters a write-only sink; it is size-capped, rate-limited, never reflected unescaped, never interpreted as a control message, and captures no PII beyond an opaque anonymous session id. | POST with control-shaped payload has no side effect beyond an appended feedback row; stored body is inert on read-back. |
| **INV-8** | Published state (variant set, published walkthrough, share token hash, feedback) **survives daemon restart** via a persistent authoritative store — unlike today's in-memory walkthrough/traffic state. Corruption on load **fails loud** (`daemon-architecture.md` §"Silent Failure Prohibition"), never silently serves partial/empty. | Kill+restart daemon → published share still serves; corrupt the store file → load emits a visible error event, does not silently 404. |
| **INV-9** | The share token is **redacted** from every log, event, incident, and outbound `Referer`/`Referrer-Policy` surface. Only its hash prefix (≤8 hex) may appear for correlation. **`Referrer-Policy` alone does NOT protect the token server-side:** the existing proxy traffic log records the full request path by default (`internal/proxy/proxy_handler.go:532` sets `HTTPLogEntry.URL = r.URL.String()`, emitted via `ps.logger.LogHTTP`), which would capture the raw `/s/{token}` path. Therefore publish routes MUST scrub the `/s/{token}` path segment to `hash[:8]` **before** any request/traffic logging — a **P6/P7 obligation**. | grep test over log/event emitters; traffic-log assertion that a public request's logged URL contains only `hash[:8]`, never the full token; `Referrer-Policy: no-referrer` on artifact responses. |
| **INV-10** | **Single source of truth** per artifact (§10). The public plane may only **mutate feedback**; it may **read** the published walkthrough + variant set; it may mutate **nothing** in the control plane. | Write-attempt from public plane on any control artifact → 403; source-of-truth table has exactly one writer per artifact. |
| **INV-11** | **ACTIVE 2026-07-27** (live upstream is now proxied; previously dormant under the self-contained-artifact scope). Injected-bundle **CSP is authoritative** over the third-party upstream. The existing frame-header precedent (`stripFrameDenyHeaders`, `internal/proxy/rewrite.go` ~L226-267) is a **STRIP-MERGE**, **not** a replace: it deletes `X-Frame-Options` and removes **only** the `frame-ancestors` directive, **preserving every other upstream CSP directive** (its own comment: *"only the frame-ancestors directive is removed from CSP (other directives are preserved)"*). That path is **NOT sufficient** for the public plane — reusing it would keep a hostile upstream's `script-src 'unsafe-inline'`/`'unsafe-eval'`, so **upstream** inline JS would execute on the public plane. With INV-6 retired this is now the *only* thing separating publisher script (which may run) from upstream and viewer script (which may not) — a strip-merge here would collapse that distinction entirely. The public plane therefore does a **wholesale replace** (INV-12): forbid `unsafe-inline`/`unsafe-eval`, pin the bundle by **content hash + matching SRI integrity** (**not** `'self'` — see INV-12) **plus the authored-revision script hashes**. | Response header assertion (see INV-12); injected inline handlers are refused by the browser. |
| **INV-12** | **ACTIVE 2026-07-27** (see INV-11). On the public publish plane the proxy MUST **delete** the upstream `Content-Security-Policy` **and** `Content-Security-Policy-Report-Only` headers **wholesale** and **SET** its own — `Header.Del` then `Header.Set`, **never a merge**. The set policy is `script-src 'sha256-<bundle>' 'sha256-<authored-revision-script>'…` (no `unsafe-inline`/`unsafe-eval`), `connect-src 'self'`, etc. (§4). **`script-src` is hash-sources only; `'self'` is deliberately EXCLUDED** (amended 2026-07-28; *Was:* `script-src 'self' 'sha256-…'`, *Now:* `script-src 'sha256-…'`) — `'self'` would union in every same-origin script URL and render the bundle pin inert, so re-adding it is **forbidden**, not a stylistic choice. The bundle is authorised instead by its hash source plus a **matching `integrity` attribute** on its `<script src>` element (CSP3 external-hash matching), and authored bodies are inline, which `'self'` never governs. **`script-src` widening:** because INV-6 is retired, the served revision may carry publisher-authored script (`addScript`, §6a), so `script-src` gains **one `'sha256-…'` per script body in that authored revision** — computed at publish time, pinned into the stored record, and **never** widened to `'self'`/`'unsafe-inline'`/`'unsafe-eval'`/a host source as a shortcut. Hashes are per-revision: re-publishing an edited revision recomputes the set. Reusing the `stripFrameDenyHeaders` strip-merge path for CSP on the public plane is **forbidden** — it would preserve upstream `script-src`. | Given an upstream response carrying `Content-Security-Policy: script-src 'unsafe-inline'`, the served public response contains **no** `unsafe-inline` and carries **only** the agnt-set CSP. Separately: a variant's authored script executes, while byte-identical script injected by the upstream (hash absent from the pinned set) is refused. |

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

### 3a. Token-per-file serve semantics

A publish serves **files**, and each served file carries its **own** `(id, token)`
pair — publishing is not one token fronting a directory. This keeps §2b's route
shape (`/s/{token}/…`) but scopes each token to exactly one file.

| Property | Rule |
|---|---|
| Key | The pair is keyed by **`(filename, content-digest)`**. `filename` is the publisher-facing identity; `content-digest` is `sha256` of the served bytes, and is what makes a revision addressable and its script hashes computable (INV-12). |
| `id` | Stable, non-secret, per-file. Safe to log, safe to show in `publish status`. Correlation handle for feedback rows and for the authored revision's pinned `script-src` set. |
| `token` | The §3 secret: 256-bit CSPRNG, returned once, sha256-at-rest, constant-time verified. All of §3 applies unchanged **per file** — there is no shared or derived token, so compromising one file's token grants nothing about another's. |
| **Edit keeps the token** | Re-publishing a file changes its `content-digest`, so a **new revision** is recorded under the **same `id` and the same token**. Already-shared URLs keep working and now serve the new revision — the point of publishing a demo is that the link you sent stays live while you iterate. The revision's `script-src` hash set is recomputed on each edit (INV-12); the token is **not** rotated as a side effect. Rotation stays an explicit `publish rotate` (§2a). |
| **Delete revokes** | Deleting a file **revokes its token** with full INV-4 force: artifact, variants, walkthrough, and feedback routes 404 atomically, the share is tombstoned rather than erased (audit trail), and its feedback is purged (§7 retention). Delete is per-file — it revokes that file's token and **only** that one, leaving sibling files in the same publish serving. |
| No implicit widening | A token grants its **one** `(filename, content-digest)` lineage and nothing else. There is no path-traversal or sibling-enumeration route from a valid token to another file's bytes; deny-by-default (INV-2) covers the rest of the namespace. |

| # | Invariant | Test shape |
|---|---|---|
| **INV-15** | Each served file has its own `(id, token)` keyed by `(filename, content-digest)`. Editing a file preserves its token and serves the new revision; deleting a file revokes that token and no other. A token is never valid for a file it was not minted for. | Publish two files → each token serves only its own file (cross-token GET 404s). Edit file A → same URL serves the new digest, token unchanged, `script-src` hashes recomputed. Delete file A → A's routes 404 (INV-4) while B still serves. |

---

## 4. HTTP header policy

Injected public bundle runs over a **hostile third-party upstream**; the proxy is
authoritative for these headers and **replaces** whatever the upstream sent.
This section is **active, not aspirational**, as of 2026-07-27 — a live upstream
is now proxied (§0), so INV-11/INV-12 are enforced on every public response.
**Do not** reuse the frame-header precedent for CSP: `stripFrameDenyHeaders`
(`internal/proxy/rewrite.go`) is a **strip-merge** — it deletes `X-Frame-Options`
and strips only `frame-ancestors`, *preserving all other upstream CSP directives
(including a hostile `script-src 'unsafe-inline'`)*. For the public plane the CSP
(and `Content-Security-Policy-Report-Only`) is **deleted wholesale and re-set**
(`Del` then `Set`, never merged) — INV-11 / INV-12.

| Header | Value (public plane) | Why |
|---|---|---|
| `Content-Security-Policy` | `default-src 'self'; script-src 'sha256-<bundle-hash>' 'sha256-<authored-script-1>' …; style-src 'self'[ 'nonce-<n>']; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'` | No `unsafe-inline`, no `unsafe-eval` (INV-11/INV-12). **`script-src` is hash-sources ONLY — `'self'` is deliberately EXCLUDED, not omitted by oversight** (amended 2026-07-28; *Was:* `script-src 'self' 'sha256-…'`, *Now:* `script-src 'sha256-…'`). **Do not "restore" it.** A hash source only gates a script while `'self'`/`'unsafe-inline'` are absent: source expressions are a **union**, so with `'self'` present any same-origin script URL loads regardless of its hash or integrity, and the bundle pin becomes **inert**. The pin is real because the one external script the plane loads is emitted as `<script src="…" integrity="<bundle-hash>" crossorigin="anonymous">` with the **same** hash value quoted into `script-src` — CSP3 external-hash matching requires integrity metadata matching a hash source, so it is identical by construction, not coincidence. Nothing is lost by dropping `'self'`: that bundle is the only external script, and authored bodies are **inline** (`scriptEl.textContent = op.code`, `variant-engine.js`), which `'self'` never governs either way. **`script-src` is widened by exactly one `'sha256-…'` per authored-revision script body** (§6a `addScript`) — enumerated at publish time from the stored revision, never a host source, never `'self'`, never `unsafe-*`. This is the whole containment story for publisher script now that INV-6 is retired: publisher script runs because its hash is pinned; upstream and viewer script do not, because theirs is not. **`style-src` is `'self'` on every response, and the `'nonce-<n>'` source is added on EXACTLY ONE of them** — the self-contained artifact shell (`serveSelfContainedArtifact`, `internal/proxy/public_routes.go`), the only response that appends an inline `<style>` of ours (the `html,body{margin:0;height:100%}` reset), which the nonce authorises. `writeHeaders` emits the `'nonce-<n>'` source **iff** its `nonce` argument is non-empty, and that argument is non-empty **only** at that one call site. Every other response emits **bare `style-src 'self'`** with no nonce source: the proxied live-upstream artifact (`serveProxiedArtifact` — it deliberately injects no reset `<style>`, which would restyle the upstream's own page), the upstream refusal/rate-limit pages, the variants/walkthrough JSON, and **all feedback responses** (`serveFeedback`, `kindFeedback`). This is minimal / fail-closed: a response that carries no inline `<style>` of ours authorises no nonce source. `newNonce` also returns `""` on a CSPRNG failure, which the same branch renders as bare `style-src 'self'` so the inline style is refused rather than admitted under a predictable nonce. Raw authored CSS arrives via that one nonce'd `<style>` the renderer appends, not inline on upstream elements — §5b's property allowlist being retired does not widen this. **All remaining directives — `default-src 'self'`, `img-src 'self' data:`, `connect-src 'self'`, `frame-ancestors 'none'`, `base-uri 'none'`, `form-action 'self'`, `object-src 'none'` — are unconditional literals in `writeHeaders`, byte-for-byte identical in value AND order on the artifact and feedback paths alike.** The only two directives that vary by response are `style-src` (the nonce, above) and `script-src` (widened by the authored-revision hashes only on the two paths that serve authored content — `serveProxiedArtifact` and `serveSelfContainedArtifact`; every other path, feedback included, emits just the bundle hash). Set **wholesale**: `Header.Del` the upstream `Content-Security-Policy` **and** `Content-Security-Policy-Report-Only`, then `Header.Set` this value — **never merged** into upstream directives (INV-12); the `stripFrameDenyHeaders` strip-merge path must **not** be reused here. Bundle pinned by **content hash + matching SRI integrity** (matches `injector.go` content-addressed asset path) — *not* by `self`. `connect-src 'self'` blocks feedback exfil to third parties. `img-src` is `'self' data:` (**`https:` deliberately dropped**) to block pixel-beacon exfil via upstream `<img>` tags — chosen default; tradeoff: upstream images genuinely needed must be **proxied through `self`**. |
| `X-Frame-Options` / `frame-ancestors 'none'` | deny | The published artifact is the top document; it is not itself embeddable elsewhere. |
| `Referrer-Policy` | `no-referrer` | Token in path must never leak to upstream subresource hosts (INV-9). |
| CORS (`Access-Control-Allow-Origin`) | **absent** on artifact + walkthrough/variants; feedback `POST` is **same-origin only** (no ACAO). | No cross-origin reads; no credentialed CORS. Same-origin form-post needs no ACAO. |
| `Cache-Control` (artifact/variants/walkthrough) | `public, max-age=0, must-revalidate` (immutable snapshots may use content-addressed `max-age=31536000, immutable` for the `inject.<hash>.js` asset only) | Revoke must take effect immediately (INV-4) — no stale artifact cached past revoke. The hashed JS asset is safe to cache long because revoke kills the `/s/{token}` route that references it. |
| `Cache-Control` (feedback) | `no-store` | Feedback POST responses are never cached. |
| `Set-Cookie` | **never** | No sessions, no auth cookies on the public plane; the anonymous session id (§7) lives client-side only. |

---

## 4a. Upstream origin allowlist (SSRF / open-relay hygiene)

Proxying a **publisher-named** live origin (§0) turns the daemon into a fetcher
that will retrieve an arbitrary URL and return the bytes. Without a constraint
that is an **open relay into whatever network the daemon can see** — a developer
laptop's LAN, a CI runner's VPC, a cloud instance's metadata service. This
section exists to close that, and **only** that.

### What this is NOT

**This is not an anti-phishing control, and must never be described as one.**
A publisher may proxy any legitimate public site and dress it up with variant
ops; nothing here prevents that, and nothing here is intended to. Deception by
the publisher is out of the threat model (§0) — the publisher is trusted, and
the honest mitigation is **disclosure**, i.e. the always-on demo indicator
(INV-14, §9c). Do not reach for this allowlist to solve a deception problem: it
would not work (attackers use public origins, which are exactly what the
allowlist permits) and the attempt would cost real functionality.

### Deny-list (deny-by-default after resolution)

The publisher-supplied origin is resolved to IPs; **every** resolved address must
pass, or the origin is rejected at `publish create`/`rotate` time with a loud,
actionable error (never a silent fallback — `daemon-architecture.md` §"Silent
Failure Prohibition").

| Denied | Range / value |
|---|---|
| Loopback | `127.0.0.0/8`, `::1` |
| RFC1918 private | `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` |
| Link-local | `169.254.0.0/16`, `fe80::/10` |
| **Cloud metadata** | **`169.254.169.254`** (AWS/Azure/GCP/OpenStack), `fd00:ec2::254`, `100.100.100.200` (Alibaba), `169.254.170.2` (ECS task metadata), and any host resolving to one of them (`metadata.google.internal`, `metadata.goog`) |
| Unique-local / IPv6 private | `fc00::/7` |
| Carrier-grade NAT / shared | `100.64.0.0/10` |
| Unspecified / broadcast / reserved | `0.0.0.0/8`, `::`, `255.255.255.255`, `240.0.0.0/4`, multicast `224.0.0.0/4` + `ff00::/8` |
| Non-`https:` schemes | anything but `https:` (§5) |

**Evasion handling — all required, the list alone is insufficient:**

- **IPv4-mapped/compatible IPv6** (`::ffff:169.254.169.254`) and non-dotted-quad
  encodings (decimal, octal, hex) are normalized **before** the check, not after.
- **Check the resolved IP, not the hostname.** A hostname is attacker-chosen
  text; only what it resolves to matters.
- **DNS rebinding:** pin the resolved address and dial **that IP**, so the
  address validated is the address connected to. A re-resolve between check and
  dial is a TOCTOU hole.
- **Redirects:** re-run the full check on **every** hop; a public origin that
  302s to `169.254.169.254` is the canonical bypass. Cap redirect depth and fail
  closed on the cap, never truncate the chain silently.
- Applies equally to **op-supplied URLs** (§6a `addScript src`, `setImageSrc`),
  not just the top-level upstream.

| # | Invariant | Test shape |
|---|---|---|
| **INV-13** | Every origin the daemon fetches on behalf of a publish — the upstream target, every redirect hop, and every op-supplied URL — resolves **only** to addresses outside the deny-list above, is `https:`, and is dialed at the **same** address that was validated. Failure is a loud rejection, never a silent skip or an unproxied fallback. | Table test over the deny-list with decimal/octal/hex/IPv4-mapped encodings of each entry; a redirect chain ending at `169.254.169.254` is refused at the hop, not at the origin; a rebinding resolver (first answer public, second private) cannot cause a private dial. |

---

## 5. Concrete limits table

P2's validators encode these **verbatim**. Reject (422) anything exceeding a limit; never truncate silently.

| Limit | Value | Rationale |
|---|---|---|
| Max variants per set | **12** | A cycler UI stays usable; caps snapshot size. |
| Max steps per walkthrough | **50** | Matches the linear-step demo model; bounds payload. |
| Max selector length | **256** chars | Long enough for real nested selectors, short enough to bound the parser. |
| Selector charset / grammar | Allowlist grammar in §5a. | Blocks script-y selector tricks. |
| Max style-patch size | **4096** bytes (per variant) | Bounds CSS parse cost. **Survives INV-6's retirement** — a size bound is an abuse control, not an injection control. |
| Allowed CSS properties | ~~Allowlist (§5b).~~ **Retired with INV-6** — see §5b. | Was deny-by-default against behavioral CSS from an author now known to be trusted. |
| Forbidden CSS tokens | ~~`url(`, `expression(`, `behavior`, `-moz-binding`, `@import`, `javascript:`~~ **Retired with INV-6.** Raw CSS is accepted as authored; `url()`/`@import` fetches are bounded by CSP `img-src`/`default-src` instead (§4). | Retired: these blocked publisher-authored styling, while the CSP that actually contains exfil was already required. |
| Max raw-HTML fragment (setHTML) | **8192** bytes per op | Bounds parse cost + snapshot size for the raw-content ops INV-6's retirement admits (§6). |
| Max raw-script size (addScript) | **16384** bytes per authored revision | Same; also bounds the hash set `script-src` must carry (INV-12). |
| Max text length (setText) | **2048** bytes UTF-8 | Bounds DOM text writes. |
| Max URL length | **2048** chars | Standard practical URL cap. |
| Allowed URL schemes | **`https:` only** (upstream target + any op-supplied URL). No `http:`, `data:`, `javascript:`, `file:`, `blob:`. | Public webapp; TLS mandatory. **Survives INV-6's retirement**, now justified by INV-13 (an op-supplied URL is a proxy-reachable origin, so it inherits the §4a allowlist) rather than by JS-URL injection. |
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

### 5b. Allowed CSS property allowlist (applyStyle) — RETIRED with INV-6

**Retired 2026-07-27.** *Was:* layout/paint-safe properties only — `color`,
`background-color`, `background` (color/gradient values only, **no `url()`**),
`border*`, `outline*`, `padding*`, `margin*`, `font*`, `text-*`, `line-height`,
`letter-spacing`, `opacity`, `display`, `visibility`,
`width`/`height`/`min-*`/`max-*`, `box-shadow`, `border-radius`, `transform`,
`transition`; any other property ⇒ 422.

**Now:** `applyStyle` accepts any CSS property, and §6's `addStyle` accepts raw
CSS text. The allowlist is retained here **only as guidance** for what a variant
typically needs — validators MUST NOT enforce it. Rationale as INV-6: it
constrained a trusted publisher, and every containment it nominally provided
(no exfil to third parties, no behavioral CSS reaching the viewer's data) is
delivered by the wholesale-replaced CSP (§4) instead. The **size cap** (§5,
4096 B per variant) is unaffected and still enforced.

---

## 6. The op set — declarative core plus raw-content ops

**Amended 2026-07-27 (INV-6 retired).** Variant ops are still a **closed
vocabulary** — the renderer is a switch over known op types, and an unknown op
is a 422, not a passthrough. What changed is that the vocabulary now includes
**raw-content ops** (§6a) that carry authored CSS, HTML, and script strings. The
declarative ops below are unchanged and remain the preferred form: they are
cheaper to validate, cheaper to diff between variants, and they do not consume
`script-src` hash budget.

| Op | Signature | Renderer action | Guards |
|---|---|---|---|
| `setText` | `{op:"setText", selector, value}` | `el.textContent = value` | value ≤2048 B; **never** `innerHTML` |
| `setAttribute` | `{op:"setAttribute", selector, name, value}` | `el.setAttribute(name, value)` | `name` ∈ attr allowlist (`class`,`id`,`title`,`alt`,`aria-*`,`data-*`,`href`,`src`); `href`/`src` value must be `https:` (§5); **event-handler attrs (`on*`) forbidden** |
| `replaceClass` | `{op:"replaceClass", selector, from, to}` | `classList.replace(from,to)` | idents only |
| `addClass`/`removeClass` | `{op:"addClass", selector, value}` | `classList.add/remove` | ident only |
| `applyStyle` | `{op:"applyStyle", selector, props:{...}}` | `el.style[prop]=val` per prop | ≤4096 B per variant (§5 style-patch cap); `selector` per the §5a grammar. **No property allowlist and no forbidden-token scan** — §5b survives as non-enforced guidance only (retired with INV-6); validators MUST NOT enforce either list (§6a) |
| `setImageSrc` | `{op:"setImageSrc", selector, url}` | `img.src = url` | `https:` only, ≤2048 chars |

### 6a. Raw-content ops (admitted by INV-6's retirement)

These carry publisher-authored strings verbatim. They are **not** validated for
"script-ness" — that check guarded the wrong boundary (INV-6 rationale). They
are bounded by size (§5) and contained by CSP (§4), and every one of them is
attributed to exactly one **authored revision** (§3a), which is what makes the
`script-src` hash set computable.

| Op | Signature | Renderer action | Guards |
|---|---|---|---|
| `setHTML` | `{op:"setHTML", selector, html}` | `el.innerHTML = html` | ≤8192 B; selector per §5a; inline `on*` handlers and `javascript:` URLs inside the fragment are **inert under CSP** (`script-src` has no `unsafe-inline`) — the validator does not strip them |
| `addStyle` | `{op:"addStyle", css}` | append `<style>` to the variant root | ≤4096 B per variant; no property allowlist (§5b retired); `url()`/`@import` targets are bounded by CSP `default-src`/`img-src`, not by a token scan |
| `addScript` | `{op:"addScript", src?, code?}` | append an **inline** `<script>` (never `<script src>`) to the variant root | ≤16384 B per authored revision, counted on the **final inlined body**; the body executes **only** if its `sha256` is in the revision's pinned `script-src` hash set (INV-12). `src` is a **publish-time fetch input, not a runtime attribute**: it must be `https:`, pass §4a (INV-13), and its fetched body is inlined at publish time so its hash joins the pinned set — see below |

**`addScript src` resolves at publish time, never at render time.** A served
revision contains **no** `<script src=…>` element. When an op carries `src`, the
daemon fetches that URL **once, at publish time**, under the same §4a machinery
that guards the upstream target (scheme check, deny-list on the *resolved*
address, resolve-pinned dial, per-hop redirect re-check — INV-13). The fetched
bytes are then treated exactly as `code`: bounded by the 16384 B revision cap
(§5), stored in the authored revision (§3a), and hashed into the pinned
`script-src` set (INV-12). A fetch that fails, exceeds the cap, or fails §4a is
a **loud publish rejection** — never a fallback to a runtime `src`, and never an
unfetched passthrough. Consequence, which is the point: because no `src`
attribute ever reaches the served DOM, there is **no** shape of this op that can
be satisfied by adding a host source to `script-src` — INV-12's "never widened
to … a host source" stays literally true, and no SRI attribute is needed because
nothing is loaded by URL at render time. `src` and `code` are alternative
sources for one body; supplying both is a 422.

**IMPLEMENTATION STATUS (2026-07-29): the fetch half above is NOT implemented,
and `src` is therefore REFUSED at publish time.** The design in this section
stands as the recorded plan — nothing here is retired. But no publish pipeline
fetches `Op.Src`, so until one exists `Op.Validate`
(`internal/publish/op.go`) rejects any `addScript` op carrying `src` with an
error naming that publish-time fetching is not implemented and directing the
author to an inline `code` body. Read this section as the specification of work
still to do, not of shipped behaviour. See the 2026-07-29 amendment-log row for
why acceptance was worse than rejection.

**INV-14 ENFORCEMENT (2026-08-18, operator decision option (a)): `addScript` in
BOTH forms is refused at publish on a published walkthrough — the code above is
served-behaviour only for revisions stored before this gate.** A published
walkthrough is served on the public plane AS the artifact document, always
carrying the mandatory disclosure indicator (§9c/INV-14). An authored `addScript`
body runs in the badge's own realm and can defeat the disclosure (task
`01KYQFAZRH`: id squatting, re-assert-budget exhaustion, `appendChild`
monkeypatch — same-realm script beats same-realm script, unfixable by hardening
the module). So `PublishedWalkthrough.Validate` (`internal/publish/validate.go`)
now rejects the whole artifact if any variant op is `addScript`, naming INV-14 and
a remedy. This refusal, not the pinned-hash `script-src`, is what keeps INV-14's
blanket claim true. The refusal is at the `PublishedWalkthrough` boundary (a bare
`VariantSet` via `DecodeVariantSet` is never served and stays permissive), and the
store's read path does not re-validate, so pre-gate stored revisions keep serving.
See the 2026-08-18 amendment-log row.

**Still forbidden (validator rejects — these are not "raw content", they are
plane-crossing):** any op naming a `__devtool` control API, `proxy exec`, a
dev WebSocket endpoint, or a daemon verb; any op that would make the public
plane write a control-plane artifact (INV-10); any op supplying an origin that
fails §4a (INV-13). The retirement of INV-6 widened what a variant may
*render*; it widened nothing about what the public plane may *reach*
(INV-1 unchanged).

**Where the containment now lives.** Previously: a validator that refused
code-shaped strings. Now: (a) CSP wholesale-replaced and pinned to the
authored-revision script hash — upstream and viewer script cannot execute even
though publisher script can (INV-11/INV-12); (b) the public plane's structural
inability to resolve a `scope.Scope` (INV-1); (c) feedback remaining inert data
(INV-7). Downstream tasks must not reintroduce the retired string scan as
"defense in depth" — it produces false rejections of legitimate variants
while adding nothing the CSP does not already guarantee.

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
| `demo-indicator` (**new**) | the always-on disclosure badge (§9c, INV-14) — **mandatory**, not an opt-in module |
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

### 9c. Always-on demo indicator

Every published artifact renders a **persistent, non-dismissible indicator**
identifying it as an `agnt` demo of a proxied site — not the site itself.

| Property | Rule |
|---|---|
| Always on | Rendered on **every** public artifact response. There is **no** publisher setting, URL parameter, config key, or variant op that removes, hides, or empties it. A `.agnt.kdl` key that appeared to disable it would be a Config Authority bug (`daemon-architecture.md`) — the correct behaviour is that no such key exists. |
| Non-dismissible | No close affordance. It may collapse to a compact form, but a collapsed state still shows the disclosure text and is re-expandable. |
| Tamper-resistant | Mounted in a **closed shadow root** at a top z-layer, styled from the RolePublic `ui-tokens` scale, so neither upstream CSS nor a variant op (§6a — which may carry raw CSS/HTML, INV-6 retired) can hide, cover, or restyle it out of legibility. A variant that visually obscures it is a defect in the renderer's layering, not a supported effect. **The one op class that *could* defeat the shadow root — an authored `addScript` running in the badge's own realm — is refused at publish** (INV-14 enforcement, 2026-08-18; `PublishedWalkthrough.Validate`, §6a), because same-realm script beats same-realm script and the module cannot be hardened against it (task `01KYQFAZRH`). |
| Content | Names `agnt`, states the page is a **demo of a proxied site**, and links to the publisher-visible share identity (`id`, §3a — never the token, INV-9). |
| Why | This is the **honest** mitigation for publisher-side deception, which §4a's origin allowlist explicitly cannot and does not address. A proxied lookalike is indistinguishable from the real site to a viewer *unless something on the page says otherwise*; the indicator is that something. It is a disclosure control, so its value is entirely in being unconditional — an indicator that can be turned off protects exactly the viewers who most need it least. |

| # | Invariant | Test shape |
|---|---|---|
| **INV-14** | Every public artifact response renders the demo indicator, and no publisher-reachable input (config, op, query param, feedback) can remove, hide, or blank it. | Serve an artifact → indicator present in the DOM with non-empty disclosure text. Publish a variant whose raw CSS/HTML targets the indicator (`display:none`, overlay, removal) → indicator still present and visible. Fuzz publish inputs for a disable path → none exists. |

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
| Variant/walkthrough/op **schemas + validators** (§5, §5a, §6, §6a) | ~~INV-6~~ (retired — validators must **not** scan raw content for code-shape); all §5 limits; §5b is guidance only | **P2** — schemas & validators |
| **RolePublic bundle** split + dependency-closure build gate (§9) | INV-5 | **P4** — public bundle |
| **Share tokens** — CSPRNG, 256-bit, sha256-at-rest, constant-time verify, rotate/revoke, redaction (§3) | INV-3, INV-9 | **P6** — token store |
| **Token-per-file serve semantics** — `(id, token)` keyed by `(filename, content-digest)`; edit keeps token, delete revokes (§3a) | INV-15, INV-4 | **P6** (store/keying) / **P7** (routes) |
| **Always-on demo indicator** — mandatory RolePublic module, non-dismissible, tamper-resistant (§9c); **authored `addScript` refused at publish** so no same-realm authored script can defeat it (INV-14 enforcement, 2026-08-18, `PublishedWalkthrough.Validate`) | INV-14 | **P4** (module) / **P10** (variant-cannot-hide-it e2e) / **publish-time refusal** (`TestAddScriptRefusedOnPublishedWalkthrough`, `internal/publish`) |
| **Public routes** + endpoint matrix + deny-by-default + header policy + scope isolation (§2, §4) | INV-1, INV-2, INV-10, INV-11, INV-12 | **P7** — public routes |
| **Public-plane CSP wholesale replace** — `Header.Del` upstream `Content-Security-Policy` + `Content-Security-Policy-Report-Only`, then `Header.Set` agnt policy; **no strip-merge reuse** of `stripFrameDenyHeaders` (§4) | INV-11, INV-12 | **P7** (rule) / **P10** (upstream-`unsafe-inline` e2e assertion) |
| **Token log-scrub** — scrub `/s/{token}` to `hash[:8]` before traffic/request logging (§3, §9) | INV-9 | **P6/P7** |
| **Upstream origin allowlist** — deny-list + normalization + resolve-pinned dial + per-hop redirect re-check (§4a) | INV-13 | **P7** (rule) / **P10** (redirect + rebinding e2e) |
| **Feedback** sink — anonymous, rate-limited, size-capped, inert, retention (§7) | INV-7 | **P8** — feedback |
| **Persistence + revoke/restart** — authoritative store, atomic revoke, corruption-fails-loud (§8) | INV-4, INV-8 | **P6/P9** — publish store |
| **End-to-end**: token guess, revoke-kills-all, forbidden-module build fail, XSS/JS-injection attempts, **upstream `unsafe-inline` CSP stripped** (INV-12), **op-supplied `addScript src` inlined + hash-pinned with no host source in `script-src`** (INV-12/INV-13, §6a), **private-address/redirect/rebinding refusal** (INV-13), **variant cannot hide the demo indicator** (INV-14), **per-file token scoping across edit and delete** (INV-15), restart survival | INV-1…INV-15 (integration) | **P10** — e2e |

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
