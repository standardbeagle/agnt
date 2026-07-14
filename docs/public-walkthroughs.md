# Public Walkthrough Publishing

Operator guide for publishing a walkthrough as an anonymous, token-gated public
webapp. Covers secure defaults, the publish lifecycle (create / rotate / revoke),
anonymous feedback, and incident response.

- **Security spec (normative):** [`superpowers/specs/2026-07-13-public-walkthrough-publish-security.md`](superpowers/specs/2026-07-13-public-walkthrough-publish-security.md)
  — the threat model, security invariants (INV-1..INV-12), the concrete limits
  table, and the endpoint matrix. This guide summarizes what **ships today** and
  points at the spec for the full contract; it does not restate it.
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
| Injected bundle | full dev instrumentation (`__devtool` API, WS, exec, audits) | **stripped `RolePublic` bundle** — player + variant cycler + feedback widget, nothing else |
| Control surface | present (metrics WS, proxy exec, `__devtool/*`) | **absent** — never registered, `403`/`404` |
| Session scope | resolves a project scope | resolves **no** scope; a token maps to an immutable revision, never a project (INV-1) |
| Listener | dev proxy port(s) | separate, **opt-in** listener (`AGNT_PUBLIC_ADDR`) |
| Upstream | live reverse-proxies a dev-server origin | **none** — see §6 |

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
| `Content-Security-Policy` | `default-src 'self'; script-src 'self' 'sha256-<bundle>'; style-src 'self' 'nonce-<n>'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'` |
| `X-Frame-Options` | `DENY` |
| `Referrer-Policy` | `no-referrer` (token path never leaks to third-party subresource hosts) |
| `X-Content-Type-Options` | `nosniff` |
| `Cache-Control` (artifact/JSON) | `public, max-age=0, must-revalidate` (so revoke takes effect immediately) |
| `Cache-Control` (feedback) | `no-store` |
| `Set-Cookie` / `Access-Control-Allow-Origin` | **never** (deleted wholesale) |

No `unsafe-inline`, no `unsafe-eval`; the bundle is pinned to `self` + its content
hash (INV-6 / INV-11 / INV-12).

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
- **Corruption fails loud.** A store that fails to load emits a visible daemon
  startup error event and refuses to serve — it does not silently `404` or serve an
  empty artifact.
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

## 6. CSP / upstream caveats — what ships today vs. the spec

The security spec (§0) anticipates `agnt` reverse-proxying a **live third-party
upstream URL** and injecting the public bundle into it, with the accompanying CSP
wholesale-replace and SSRF concerns.

**The current implementation does NOT do that.** The published artifact is a
**self-contained shell**: the `PublishedWalkthrough` schema has no upstream-URL
field. `serveArtifact` (`internal/proxy/public_routes.go`) emits a small HTML shell
that loads only the `RolePublic` bundle and reads the steps + variant set from the
immutable published revision. Consequences:

- **No live upstream is proxied, so there is no SSRF surface today.** The public
  plane copies **no** upstream headers; every response header is built wholesale
  here.
- The **wholesale CSP replace** (INV-12: `Header.Del` any upstream CSP +
  `Content-Security-Policy-Report-Only`, then `Header.Set` agnt's own) is
  implemented and applied unconditionally as **defence in depth** — even though no
  upstream CSP can currently reach this path. It is the opposite of the
  `stripFrameDenyHeaders` strip-merge used by the dev proxy, which would preserve a
  hostile upstream's `script-src 'unsafe-inline'`.

**If/when live upstream proxying is added**, the spec's CSP wholesale-replace and
SSRF caveats (§4, §11, INV-11/INV-12) apply in full. Until then, treat the
artifact as self-contained.

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
4. **Trust the fail-loud behavior.** Store corruption surfaces a visible daemon
   startup error and refuses to serve the affected record; it never silently
   degrades. A bad per-record checksum tombstones that one record and surfaces the
   error, leaving healthy records serving.
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
