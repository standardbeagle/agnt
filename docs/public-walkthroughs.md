# Public Walkthrough Publishing

Operator guide for publishing a walkthrough as an anonymous, token-gated public
webapp. Covers secure defaults, the publish lifecycle (create / rotate / revoke),
anonymous feedback, and incident response.

- **Security spec (normative):** [`superpowers/specs/2026-07-13-public-walkthrough-publish-security.md`](superpowers/specs/2026-07-13-public-walkthrough-publish-security.md)
  — the threat model, security invariants (INV-1..INV-15; INV-6 retired), the
  concrete limits table, and the endpoint matrix. This guide summarizes what
  **ships today** and points at the spec for the full contract; it does not
  restate it.
- **MCP tool surface:** [`mcp-tools.md` → `publish` Tool](mcp-tools.md#publish-tool)
- **Config surface:** [`configuration.md` → Public Walkthrough Feedback](configuration.md#public-walkthrough-feedback-internalconfigfeedbackgo-feedback-in-agntkdl)

---

## 1. Two planes, secure by default

Publishing splits into two disjoint planes that never overlap:

| Plane | Who | How reached | What it can do |
|-------|-----|-------------|----------------|
| **Control plane** | trusted publisher (you) | local dev session, the `publish` MCP tool, project-scoped | create / status / list / revoke / rotate / read feedback |
| **Public plane** | anonymous viewer | share token in a URL path (`/s/{token}`) | read the artifact, read variants/walkthrough JSON, POST one feedback row |

**Secure defaults — nothing is exposed until you opt in:**

- **No public port is auto-bound.** The token-gated public handler is always
  *built* inside the daemon (so `publish feedback` reads work), but a dedicated
  public HTTP listener is stood up **only** when you set the `AGNT_PUBLIC_ADDR`
  environment variable. Without it, there is no public network surface.
  (`cmd/agnt/daemon.go`, `internal/daemon/publish_public.go:startPublicListener`.)
- **Token is 256-bit CSPRNG, returned once, hashed at rest.** `create`/`rotate`
  return the plaintext token exactly once; the store persists only `sha256(token)`
  and verifies with a constant-time compare. Plaintext never touches disk, logs,
  or events.
- **Deny-by-default routes.** The public handler serves *only* the routes in the
  endpoint matrix below; every other path — including the entire dev
  `/__devtool/*` control surface — is `403`/`404`. The dev control surface is not
  registered on the public handler at all, so it is structurally unreachable, not
  merely guarded (INV-1 / INV-2, `internal/proxy/public_routes.go`).
- **Redacted logs.** Only a token hash prefix (`hash[:8]`) ever appears in status
  output, arrival events, or logs.

---

## 2. Public plane vs. the dev tunnel / proxy

The public publish plane is a **different `http.Handler`** from the dev reverse
proxy — not the dev proxy with auth bolted on. Concretely:

| | Dev proxy / tunnel | Public publish plane |
|---|---|---|
| Handler | `internal/proxy/server.go` mux | `internal/proxy/public_routes.go` (`PublicHandler`) |
| Injected bundle | full dev instrumentation (`__devtool` API, WS, exec, audits) | **stripped `RolePublic` bundle** — player + variant cycler + feedback widget + boot glue, nothing else |
| Control surface | present (metrics WS, proxy exec, `__devtool/*`) | **absent** — never registered, `403`/`404` |
| Session scope | resolves a project scope | resolves **no** scope; a token maps to an immutable revision, never a project (INV-1) |
| Listener | dev proxy port(s) | separate, **opt-in** listener (`AGNT_PUBLIC_ADDR`, or `agnt publish serve`) |
| Upstream | live reverse-proxies a dev-server origin, **subresources and all** | live-proxies the **document only**, through the INV-13 origin guard, for a share that names an upstream — see §6 |

---

## 3. Publish lifecycle

All lifecycle actions go through the `publish` MCP tool (control plane). Full
per-action reference: [`mcp-tools.md`](mcp-tools.md#publish-tool).

```
publish {action: "create", walkthrough: {...}}   // -> id + token (SHOWN ONCE) + /s/{token}
publish {action: "list"}                          // this project's shares (no tokens)
publish {action: "status", id: "<share-id>"}      // state + hash prefix; never the token
publish {action: "rotate", id: "<share-id>"}      // new token SHOWN ONCE; old token dies now
publish {action: "revoke", id: "<share-id>"}      // kills every route immediately
```

### create → share

`create` validates the walkthrough JSON, mints a share, and returns:

```
Share <share-id>
  token (SHOWN ONCE — save it now): <43-char base64url token>
  url: /s/<token>
  digest: <content digest>
```

- The **token is shown exactly once.** It is never stored plaintext, never
  re-derivable, and never returned by `status`/`list`. Save it now — a lost token
  cannot be recovered, only `rotate`d.
- Share it as the full public URL: `https://<your-public-host>/s/<token>`.
  (The `url` field is the relative path; prepend your public origin.)

### token handling

| Concern | Rule |
|---------|------|
| Where the token appears | `create` / `rotate` output **only**. Never in `status`, `list`, `feedback`, logs, or events. |
| Correlation | `status`/`list` show `token_hash_prefix` (≤8 hex) — use it to match a share to a log line. |
| Lost token | You cannot recover it. `rotate` to mint a fresh one; the old one dies immediately. |
| Rotation | `rotate` issues a new token and invalidates the old hash in the same write. In-flight requests bearing the old token `404` at once. |

### revoke / rotate

- **`revoke`** tombstones the share. `GET /s/{token}`, `/variants.json`,
  `/walkthrough.json`, and `POST /s/{token}/feedback` **all `404` atomically** —
  no grace window, no cache that outlives revoke (INV-4). The share stays visible
  in `list` marked `revoked` for audit.
- **`rotate`** keeps the share alive but swaps the token. Use it when a token may
  have leaked but you still want the share up.

---

## 4. Public endpoint matrix (what a viewer can reach)

Served only when `AGNT_PUBLIC_ADDR` is set. Token is in the **path segment**
(never a query string — query strings leak via `Referer` and proxy logs).

| Route | Methods | Serves |
|-------|---------|--------|
| `GET /s/{token}` | GET, HEAD | Artifact HTML shell; loads only the `RolePublic` bundle |
| `GET /s/{token}/variants.json` | GET, HEAD | Immutable published variant-set snapshot |
| `GET /s/{token}/walkthrough.json` | GET, HEAD | Immutable published walkthrough script |
| `POST /s/{token}/feedback` | POST | Appends one anonymous feedback row (`application/json` only) |
| `GET /__devtool/inject.<hash>.js` | GET | The content-addressed `RolePublic` bundle (the only permitted `/__devtool` path) |
| **everything else** | any | `403` (known dev-control path) / `404` (unknown) |

- An invalid, revoked, or unknown token returns `404` for **every** sub-route —
  no existence oracle, no timing leak beyond the constant-time compare.
- Wrong method on a valid route returns `405` with an `Allow` header (only after a
  valid token, so it is not an enumeration oracle).
- Feedback content-type must be `application/json` (`415` otherwise); over-cap
  body is `413`; rate-limited is `429`; otherwise-invalid is `422`.

### Response headers (public plane, `writeHeaders`)

| Header | Value |
|--------|-------|
| `Content-Security-Policy` | `default-src 'self'; script-src 'sha256-<bundle>' 'sha256-<authored-script>'…; style-src 'self' ['nonce-<n>']; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'` |
| `X-Frame-Options` | `DENY` |
| `Referrer-Policy` | `no-referrer` (token path never leaks to third-party subresource hosts) |
| `X-Content-Type-Options` | `nosniff` |
| `Cache-Control` (artifact/JSON) | `public, max-age=0, must-revalidate` (so revoke takes effect immediately) |
| `Cache-Control` (feedback) | `no-store` |
| `Set-Cookie` / `Access-Control-Allow-Origin` | **never** (deleted wholesale) |

No `unsafe-inline`, no `unsafe-eval` (INV-11 / INV-12). Three details bite in
practice:

- **`script-src` carries hash sources only — `'self'` is deliberately absent.**
  Source expressions are a union, so `'self'` would authorise every same-origin
  script URL and make the bundle pin inert. The bundle is authorised by its hash
  plus a matching `integrity=` attribute on its `<script src>`. Do not "restore"
  `'self'`.
- **`script-src` widens by one `'sha256-…'` per authored `addScript` body** in the
  served revision, computed at publish time. That is the whole containment story:
  your script runs because its hash is pinned; upstream or viewer script does not,
  because theirs is not.
- **The `style-src` nonce exists only on the self-contained path.** The proxied-
  upstream response emits no inline `<style>` of ours and therefore passes an
  empty nonce, so that response authorises **no** inline style at all — see §6.

---

## 5. Feedback: limits, retention, storage, restart

Anonymous feedback is **data, never a command** (INV-7): a viewer's `POST` appends
one inert, size-capped row to a write-only sink. It cannot mutate the walkthrough,
the variant set, or the token, and it cannot read anything back.

### Limits (spec §5 defaults)

| Limit | Default | KDL key |
|-------|---------|---------|
| Feedback rate | 10 req/min per **(share, IP)** | `rate-per-minute` |
| Burst | 5 | `burst` |
| Max feedback body | 4096 bytes | `max-body-bytes` |
| Retention (rows) | 500 rows per share, oldest evicted | `max-rows-per-share` |
| Retention (age) | 90 days | `retention-days` |

Retention is **500 rows OR 90 days, whichever comes first.** These limits are
configurable and honored by the live limiter — see [`configuration.md`](configuration.md#public-walkthrough-feedback-internalconfigfeedbackgo-feedback-in-agntkdl).

### Storage & restart

- Feedback persists in a **durable on-disk store** —
  `~/.local/state/devtool-mcp/feedback` (or `$XDG_STATE_HOME/devtool-mcp/feedback`).
  Published shares persist in the sibling `.../devtool-mcp/publish` store. Both are
  **authoritative on-disk records, not caches** — a deliberate departure from the
  daemon's usual in-memory-is-cache model (spec Deviations #1).
- Published shares and feedback **survive daemon restart** (INV-8). Restart the
  daemon and an un-revoked share still serves; its feedback is intact.
- **Publish-store corruption fails loud and closed.** If the publish store fails
  to load, the daemon emits `publish_store_load_failed`, does not build the public
  handler, and therefore skips the public listener. Publish control operations
  are unavailable; no share is served as an empty or partial artifact.
- **Feedback-store corruption fails loud but leaves publishing active.** If the
  feedback store fails to load, the daemon emits `feedback_store_load_failed`
  but still builds the public handler and starts the configured listener. Existing
  shares remain available. Valid feedback POSTs are accepted (`202`) and dropped
  because no persistence sink is attached, while control-plane feedback reads
  return an empty result (`rows: []`, empty cursor, zero totals). If silent
  feedback loss is unacceptable, operators must manually shut down the public
  listener by unsetting `AGNT_PUBLIC_ADDR` and restarting the daemon until the
  feedback store is repaired.
- **Owner-scoped read.** Read feedback only via the control plane:
  `publish {action: "feedback", id: "<share-id>"}`. Ownership is enforced
  daemon-side (a share from another project reports not-found — no cross-project
  leak). Row bodies are **raw and inert** — escape before rendering as HTML.

### Arrival events

On a successful feedback append, the daemon emits a **counts-only, project-scoped**
arrival event to the owning project's dev/agent surface — carrying `share_id`,
`revision_id`, `total`, `dropped`, and a static remediation hint, and **never** the
token or the body. Subscribers on other projects never receive it.

---

## 6. Live upstream proxying — and the limitation that will bite you first

A published walkthrough may name a **live third-party origin** it is a demo of.
`serveArtifact` (`internal/proxy/public_routes.go`) branches on it:

| Revision | `/s/{token}` serves |
|---|---|
| `upstream.url` set | that origin's **document**, fetched through the INV-13 guard (§6a), with the `RolePublic` bundle spliced in |
| no upstream | the **self-contained shell** — steps + variant set from the immutable revision, no outbound fetch at all |

Name one either way (they must agree, or `create` refuses):

```
publish {action: "create", walkthrough: {...}, upstream: "https://shop.example.com/cart"}
publish {action: "create", walkthrough: {..., "upstream": {"url": "https://shop.example.com/cart"}}}
```

### READ THIS FIRST: only the document is proxied, so a proxied demo renders unstyled

The public CSP confines subresources to `'self'` — `script-src` is hash-only,
`style-src` is `'self'` with **no nonce on this path**, `img-src` is `'self'
data:` — and **there is no subresource proxy route**. So the upstream's own CSS,
JS, fonts, and images **do not load**. What a viewer sees is the upstream's markup
with your walkthrough running over it, unstyled.

This is a known gap with a follow-up filed for the subresource route; each proxied
subresource is another guarded outbound fetch and another content-type surface on
an anonymous route, so it is deliberately its own slice. Until it lands: expect
naked HTML, and prefer a self-contained artifact when presentation matters more
than liveness.

### What else the proxied path does

- **No viewer data reaches the upstream.** The outbound request is built from
  nothing but the published URL — no `Cookie`, no `Referer` (which would carry the
  share token, INV-9), no `X-Forwarded-*`, no session scope (INV-1).
- **Not one upstream response header is copied.** Every header is composed
  wholesale by `writeHeaders`, so a hostile origin's cookies, CORS grants, and CSP
  cannot reach a viewer.
- **CSP is a wholesale replace, now load-bearing rather than defensive.** INV-11 /
  INV-12 activated with this epic: `Header.Del` the upstream
  `Content-Security-Policy` **and** `Content-Security-Policy-Report-Only`, then
  `Header.Set` ours. A merge would preserve a hostile upstream's `script-src
  'unsafe-inline'` — which is exactly what the dev proxy's `stripFrameDenyHeaders`
  strip-merge does, and why that path must never be reused here.
- **A refusal is a loud `502 upstream unavailable`, never a fallback.** A share
  that names an upstream is a demo *of* that upstream; quietly serving the
  self-contained shell in its place would be a silent failure, not degradation.
  The guard's verdict goes to the debug log, never to the viewer.
- **Revoke still beats the fetch.** The token is verified before either branch, so
  a revoked share `404`s before any outbound request is made (INV-4).

Fetch bounds — each one **refuses**, never truncates or serves a partial:

| Bound | Value |
|---|---|
| Scheme | `https:` only |
| Document size | 4 MiB (tighter than the dev plane's 16 MiB — this path is driven by anonymous requests) |
| Redirect hops | 5, then fail closed |
| Whole fetch (chain included) | 15s; 5s per TCP connect |
| Upstream status | must be `200` |
| Upstream content type | must be HTML — anything else is refused rather than relayed |

### The self-contained path (unchanged)

The shell carries **no inline script** (the CSP forbids one) and inlines neither
the token nor the walkthrough. Startup is owned by `public-boot.js`: it reads the
share token from `window.location`, fetches `/s/{token}/walkthrough.json`
same-origin, and calls `__walkthroughViewer.create(...).start()`. It is
double-gated — on the `RolePublic`-only version marker and on the `/s/{token}`
route shape — so it ships inert in every dev bundle. A revoked/unknown token, a
network failure, or a step-less artifact renders a plain text notice rather than
leaving the visitor on a blank page; revoked and unknown are deliberately
indistinguishable in that message (no existence oracle).

---

## 6a. Upstream origin allowlist (SSRF / open-relay hygiene, INV-13)

Proxying a publisher-named origin makes the daemon fetch an arbitrary URL and hand
back the bytes. Unconstrained, that is a relay into whatever network the daemon can
see — your LAN, a CI runner's VPC, a cloud instance's metadata service. Every
outbound fetch is therefore guarded (`internal/publish/ssrf.go`, spec §4a).

**Refused, loudly, before any socket opens:** loopback, RFC1918 (`10/8`,
`172.16/12`, `192.168/16`), carrier-grade NAT (`100.64/10`), link-local
(`169.254/16` — including `169.254.169.254`), multicast, reserved/broadcast,
`0.0.0.0/8`; IPv6 unspecified/loopback, unique-local `fc00::/7` (including
`fd00:ec2::254`), `fe80::/10`, multicast; the IPv4-embedding prefixes 6to4,
Teredo, and both NAT64 prefixes; and `100.100.100.200` (Alibaba metadata).

Three properties that make it hold rather than merely look right:

1. **Every ambiguity denies.** A missing resolver, a resolver error, an empty
   answer, a numeric-looking host that does not decode, an interface-scoped zone
   (`fe80::1%eth0`) — all refusals. Alternate IPv4 spellings (`2130706433`,
   `0177.0.0.1`, `0x7f000001`, `127.1`) are decoded *before* the check, so they
   cannot be laundered past it. **Every** resolved address must pass; one public
   answer among private ones does not make a host safe.
2. **Pin-and-dial.** The connection goes to the exact address the guard validated,
   never to a re-resolution of the hostname — that re-resolution is the
   DNS-rebinding hole. TLS still verifies against the URL's hostname, so pinning
   does not weaken certificate validation. No connection pooling, and `Proxy` is
   explicitly nil so `HTTPS_PROXY` cannot route the fetch to an unvalidated
   address.
3. **Every redirect hop is re-checked.** A chain whose last hop lands on
   `169.254.169.254` is refused at that hop; exceeding the hop cap fails closed.

The same guard covers publish-time `addScript src` fetches, not just the upstream
document.

**What this is not:** an anti-phishing control. A publisher dressing up a
legitimate public site is explicitly out of the threat model — the publisher holds
the dev session and is trusted by construction, and an allowlist could not work
here anyway, since attackers use public origins, which are exactly what this
permits. The honest mitigation for deception is disclosure — §6b.

---

## 6b. The demo indicator (INV-14) — always on, no off switch

Every public artifact response, proxied **and** self-contained, renders a
disclosure badge reading:

> **agnt** Demo walkthrough of a proxied site — not the live site.

It is a **mandatory** member of the `RolePublic` bundle
(`internal/proxy/scripts/demo-indicator.js`), not an opt-in module. There is **no
config key, no op, no query parameter, no feedback field, and no close
affordance** — the module reads no input at all beyond the marker that tells it it
is on the public plane. A disclosure that can be switched off protects exactly the
viewers who need it least.

It mounts in a **closed shadow root** (page script holds no handle on it), pins
itself to the top of the stacking order, and declares its `:host` geometry
`!important` so an important declaration from the page cannot reposition or
collapse it. Styling is CSSOM-only (a constructed `CSSStyleSheet` adopted into the
shadow root) — CSSOM rules are not subject to `style-src`, which is what lets it
work on the proxied path where **no** inline style is authorised. Never introduce a
style element or attribute into that module; the proxied response would refuse it.

**Be clear about its limits.** It resists ordinary page CSS; it is not
adversarially tamper-proof. A benign SPA that replaces `document.body` currently
removes it, because the mount is idempotent but not re-armed by an observer — a
follow-up is filed. The end-to-end "a variant's raw CSS/HTML cannot hide it"
adversarial gate is likewise its own slice (spec §11 → P10). Treat the badge as
honest disclosure to a cooperating page, not as a control that survives a
determined publisher.

---

## 7. Incident response

On a suspected token leak or abuse:

1. **Contain.** `publish {action: "rotate", id: "<share-id>"}` if you want the
   share to stay up under a new token, or `publish {action: "revoke", id}` to kill
   it outright. Revoke takes effect immediately across every route (INV-4); the
   `max-age=0, must-revalidate` cache policy means no browser serves a cached copy
   past revoke.
2. **Observe abuse.** `publish {action: "feedback", id, ...}` returns `total`
   (stored rows) and `dropped` (cumulative rate-limit-shed count). A climbing
   `dropped` is the signal that the per-(share, IP) limiter is throttling a
   flood — the abuse is being shed, not crashing anything (excess is `429`).
3. **Correlate via hash prefix.** Logs and `status` output carry only
   `token_hash_prefix` (`hash[:8]`) — never the token. Match a log line to a share
   by that prefix.
4. **Distinguish the failed store.** Publish-store corruption emits
   `publish_store_load_failed` and prevents the public handler/listener from
   starting. Feedback-store corruption emits `feedback_store_load_failed` but
   leaves existing shares and the configured listener active: feedback POSTs are
   accepted and dropped, and control reads are empty. If you must prevent silent
   feedback loss, manually unset `AGNT_PUBLIC_ADDR` and restart the daemon; repair
   the feedback store and confirm it loads before restoring the listener.
5. **Kill the public surface entirely** by unsetting `AGNT_PUBLIC_ADDR` and
   restarting the daemon — the public listener is not stood up without it, while
   the control plane and dev proxy stay up.

---

## 8. Config example

A KDL `feedback` block that parses against `internal/config` (all keys optional;
omitted/invalid values fall back to the spec defaults shown):

```kdl
feedback {
    rate-per-minute 10
    burst 5
    max-body-bytes 4096
    max-rows-per-share 500
    retention-days 90
}
```

Opt-in the public listener via the environment (there is no KDL key for it):

```sh
export AGNT_PUBLIC_ADDR=":8899"   # bind the anonymous public plane; unset = no public port
```

Full config reference:
[`configuration.md`](configuration.md#public-walkthrough-feedback-internalconfigfeedbackgo-feedback-in-agntkdl).
