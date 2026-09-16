# MCP Tools Reference

Full tool catalog, per-tool parameters, and output formats. CLAUDE.md carries
only the summary table + handler pattern; this is the detailed reference.

## Agent call order

Use the broadest existing operation that can answer the question:

1. Read `get_incidents`, `currentpage`, or process output for current state.
2. Run the matching MCP audit: `responsive_audit`, `api_audit`, `loading_audit`,
   or `snapshot`. For release QA, begin with one compact
   `__devtool.auditPageQuality()` call.
3. Drill into failed areas only. Use `proxy exec` `search`, then `describe`, then
   one targeted `__devtool.*` helper.
4. Use raw JavaScript only when the catalog has no helper. Do not rebuild an
   audit with selectors or many piecemeal REPL calls.

One audit plus targeted evidence is the default. Independent full audits are
for an explicit audit request, not routine debugging.

## Tool Catalog

| Tool | Description |
|------|-------------|
| `detect` | Detect project type (Go/Node/Python) + scripts |
| `run` | Run scripts/commands (background/foreground/foreground-raw) |
| `proc` | Process management (status, output, stop, list, cleanup_port) |
| `proxy` | Reverse proxy (start, stop, restart, status, list, exec, navigate, resize, toast, chaos) |
| `proxylog` | Query proxy logs (query, summary, clear, stats) |
| `tunnel` | Tunnel management (cloudflare/ngrok/tailscale) |
| `currentpage` | Inner/content-page inspection: framework triage (default) + layout diagnostics + list/get/summary/clear; responses identify `execution_context` and `frame_id` |
| `get_incidents` | The error/incident surface — cursor-based, priority-ordered, with remediation hints and retention actions |
| `verify_change` | Recheck only the session's recorded findings — re-runs each finding's producer in-process, reports resolved/persist/new, merges the outcome back into the Investigation |
| `responsive_audit` | Responsive design audits across viewport sizes |
| `diagnose` | Dead-click triage in one call (`action:"click"`), or layout composite (`action:"layout"`: layout diagnose + current-viewport responsive risk + stacking/container causes, `screenshot_recommended` when a visual finding exists) |
| `api_audit` | API efficiency audit (waterfall, N+1, duplicate, chatty-load) over the fetch/XHR buffer |
| `loading_audit` | Loading-UX audit (spinner cascade + concurrent fragmentation) over the spinner timeline |
| `snapshot` | Visual regression testing (baseline/compare screenshots) |
| `daemon` | Daemon management |
| `watch` | Get monitor command for streaming events (errors, interactions, process, all) |
| `channel_reply` | Send messages to developer's browser overlay (channel mode beta) |
| `publish` | Public walkthrough shares — create/status/list/revoke/rotate + owner-scoped feedback read |
| `demo` | Narrated demo-video authoring — list/record/assemble via the in-repo engine as a daemon-managed process (repo-checkout capability) |

**Session scoping & `global` flag**: query/list tools use the project's `scope.default-global` setting (default `false`; daemon-side session-scope chokepoint — see `.claude/rules/daemon-architecture.md` § Tool session-scoping). Every gated tool (`proc`, `proxy`, `tunnel`, `session`, `daemon` startup_log) accepts an optional `global`; explicit `true` or `false` overrides project config in either direction, while omission uses config. `get_incidents` (per-session isolated) and `watch` (monitor stream) intentionally omit it.

**Handler pattern**:
- Input/Output structs with JSON schema tags
- Return `(*mcp.CallToolResult, OutputStruct, error)`
- Errors as `CallToolResult{IsError: true}` (NOT Go errors)

## get_incidents Tool

Cursor-based pull from the always-active incident inbox. This is the authoritative tool for fetching errors and warnings from all signal sources. Returns incidents in priority order (critical → error → warning → info) with remediation hints and suggested next tools. `alerts.push` changes interrupts, not inbox population.

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `severity` | string[] | all | Filter: `critical`, `error`, `warning`, `info` |
| `since` | string | beginning | Cursor from a prior pull (RFC3339) or duration like `5m` |
| `fingerprints` | string[] | — | Retrieve specific incident fingerprints |
| `sources` | string[] | all | Filter by source (e.g. `browser_js`, `http_5xx`) |
| `proxy_id` / `process_id` | string | — | Filter to a specific proxy/process |
| `detail` | string | `summary` | `full` hydrates from the caller session's bounded blob store; evicted payloads fall back to summary |
| `mark_read` | bool | false | Advance cursor and mark returned incidents read. With an active `profile`, the daemon projects the page first, so exactly the rendered rows are marked read and dropped rows raise `truncated` instead of being swept past the cursor |
| `limit` | int | 20 (max 100) | Max incidents returned |
| `raw` | bool | false | Return full JSON instead of compact text (adds per-incident `next` and `producer` fields) |
| `profile` | string | `bug` | Triage lens: `bug` (top 5 by severity), `changed` (unread since cursor), `release` (all severities grouped), `full` (legacy complete output, byte-identical to a pre-profile pull). When a profile drops rows, the returned cursor sits below the OLDEST dropped row (not the newest rendered one, which severity sorting would strand them behind), so a follow-up `since=<cursor>` with `profile:"changed"` returns exactly the dropped rows |
| `action` | string | `query` | Retention verb: `pin`, `unpin`, `clear` |
| `error_id` | string | — | Pin/unpin target: the fingerprint from a prior result |
| `tag` | string | — | Note stored with a pin, returned on the pinned item |

There is no `global` flag: the inbox is per-session hard-isolated (numbered
contract 1), which is a stronger guarantee than project scoping.

**Retention actions** (`action` param; default `query`):

| Action | Params | Effect |
|--------|--------|--------|
| `pin` | `error_id`, optional `tag` | Exempts the incident from band eviction and from every retention clear until unpinned. Bounded by `incident.MaxPinnedEntries`; pinning past the bound fails loud rather than silently evicting an older pin. |
| `unpin` | `error_id` | Releases the pin; normal retention applies again. |
| `clear` | — | Retires the caller session's unpinned incidents, routed through the bus's FIFO control-clear so an incident published just before the request cannot outlive a boundary it predates. |

Automatic retention (config: `alerts.retention`, see `docs/configuration.md`):
build success retires a process's earlier errors (timestamp-bounded, FIFO with
in-flight incident events), explicit `proc stop/restart` starts a fresh slate,
and a project's last session disconnecting clears its ring. Crash restarts
never clear.

**Pin lifetime is session-scoped by design.** A pin survives band eviction and
every retention clear, but not the session that made it: pins live in the
per-session inbox (`internal/incident/inbox.go`) and are torn down with the
session pipeline (`MPSCBus.RemoveSession`). An agent that pins an incident, ends
its session, and starts a new one does **not** see the pin again. This is a
deliberate narrowing from the retired `get_errors` tool (removed in
01KYZ0XHQEDR9FS8F4R9VZ1RH4), whose pins lived in a daemon-lifetime store and so
spanned sessions. The narrowing follows the owner's ruling that `get_errors`'
cross-session reach was a debugging affordance, not a production contract — the
same ruling that dropped its cross-project `global` flag. A daemon-lifetime pin
store would either weaken the inbox's per-session hard-isolation (numbered
contract 1 in `.claude/rules/daemon-architecture.md`) or need a carefully-scoped
exception, and neither is warranted for pin metadata. Session-scoped pins are the
correct behaviour; the contract is pinned by `TestBus_PinDiesWithSession`
(`internal/incident/inbox_test.go`).

> **Lifetime is out of scope for schema/projection parity.** The
> `get_errors`→`get_incidents` retirement gate verified that every field projects
> and every value is reachable (`open == 0`); it did **not** model how long a pin
> survives, and that harness is now gone with `get_errors`. `open == 0` was
> necessary, not sufficient — the pin lifetime above is a deliberate, tested
> contract, not a parity guarantee. A future parity/oracle check that reasons
> only about schema and reachability must not read `open == 0` as evidence that
> two surfaces agree on lifetime.

**`collection_warnings`**: names every way the returned view is known to be
partial — events the bus dropped before reaching any inbox, and `detail:"full"`
payloads the blob store could not hydrate. Non-empty means incidents are
missing from the answer, and it renders above the incident list so a degraded
view cannot read as an all-clear.

**Compact Output Format** (`detail:"full"` payloads and the aggregate `=== Next ===` block render in compact mode too, not only under `raw:true`):
```
=== Incidents (2) === [inbox: crit=1 err=1 warn=0 info=0 new=2]

[critical:process_crash] panic (2x, 3s ago)
  id: 9f3a1c2e
  runtime error: index out of range
  payload: goroutine 1 [running]: main.serve(...)   // only when detail:"full"
  next: proc action=output process_id=agnt-dev
  skill: agnt-process-proxy

[error:browser_js] TypeError (1x, 8s ago)
  id: 41bd07f0
  Cannot read property 'map' of undefined
  → http://localhost:3000/list
  next: currentpage action=triage proxy_id=dev
  skill: agnt:browser-debug

=== Next ===
tool: proc action=output process_id=agnt-dev
skill: agnt-process-proxy
replay_cursor: 2026-07-06T01:20:00Z
```

**Key Files**: `internal/tools/get_incidents.go`, `internal/incident/remediation.go`

## publish Tool

Trusted, session-scoped control plane for **public walkthrough shares**. A share
publishes an immutable walkthrough revision behind an unguessable token; the
token-gated **public plane** (anonymous viewers) is a separate HTTP handler and
is NOT reachable through this tool. Every action is project-scoped: a session
can only address shares owned by its own project (a foreign share id is reported
not-found — no cross-project leak).

Security spec: `docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md`.
Operator guide (lifecycle, feedback, incident response): [public-walkthroughs.md](public-walkthroughs.md).

**Actions**:
| Action | Inputs | Reads / Writes |
|--------|--------|----------------|
| `create` | `walkthrough` (JSON, validated before publish) | Mints a share + token; **returns the plaintext token exactly ONCE** plus a viewer-safe `id` and the `/s/{token}` URL |
| `status` | `id` | Share state (title, steps, digest, revoked flag, token **hash prefix** only) — never the token |
| `list` | — | This project's shares (no tokens) |
| `revoke` | `id` | Kills a share immediately (token stops verifying at once) |
| `rotate` | `id` | Mints a fresh token (old dies immediately); returns the new token ONCE |
| `feedback` | `id`, `cursor?`, `limit?` | **Owner-scoped read** of anonymous viewer feedback rows + observability counts (`total`, `dropped`) — never the token |

**Token rule**: the plaintext share token is returned **only** from `create` and
`rotate`, exactly once, and is never stored, re-derivable, logged, or emitted in
any event. `status`/`list`/`feedback` and all arrival events carry only a hash
prefix for correlation. Lost token ⇒ `rotate`.

**Feedback read** (`action: "feedback"`): returns feedback for a share the caller
**owns** (same ownership gate as `status`/`revoke`/`rotate`). Paginate with
`cursor` (pass a prior response's `next_cursor`; empty = first page) and `limit`
(`<=0` = all remaining). Row `body` is the **raw, inert** viewer payload — it is
data, never a command; any HTML consumer MUST escape it before rendering
(INV-7). `total` is the share's stored row count; `dropped` is the cumulative
rate-limit-shed count (spec §5 observability).

**Arrival events**: when feedback lands for a share, the daemon emits a
**counts-only**, **project-scoped** arrival event to the owning project's
dev/agent surface — carrying `share_id`, `revision_id`, `total`, `dropped`, and a
static remediation hint, and **never** the token or the feedback body. A
subscriber on another project never receives it.

**Examples**:
```
publish {action: "create", walkthrough: {...}}   // token shown ONCE
publish {action: "list"}
publish {action: "status", id: "<share-id>"}
publish {action: "revoke", id: "<share-id>"}
publish {action: "rotate", id: "<share-id>"}      // new token shown ONCE
publish {action: "feedback", id: "<share-id>", limit: 50}
publish {action: "feedback", id: "<share-id>", cursor: "<next_cursor>"}
```

**Compact feedback output**:
```
=== Feedback for <share-id> (total=2 dropped=3) ===
- <row-id> 2026-07-13T00:00:00Z {"message":"nice","rating":5}
next_cursor: <row-id>
```

**Public plane serving**: the token-gated public handler (`GET /s/{token}`,
`/variants.json`, `/walkthrough.json`, `POST /s/{token}/feedback`) is always
built in the daemon. A dedicated public HTTP listener is opt-in via the
`AGNT_PUBLIC_ADDR` env var — the daemon does not auto-bind a public port. The dev
control surface is structurally absent from the public handler (INV-1/INV-2).

**Self-contained artifact**: the published artifact is a self-contained HTML shell
— `serveArtifact` emits steps + variant set from the immutable revision and loads
only the `RolePublic` bundle. The `PublishedWalkthrough` schema has **no
upstream-URL field**, so the current implementation does **not** live-proxy an
external upstream and has no SSRF surface. The wholesale CSP replace (INV-11/INV-12)
is applied as defence in depth. If upstream proxying is added later, the spec's
CSP/SSRF caveats apply. See [public-walkthroughs.md §6](public-walkthroughs.md).

**Key Files**: `internal/tools/publish_tools.go`, `internal/daemon/hub_publish.go`,
`internal/daemon/publish_public.go`, `internal/daemon/feedback_events.go`,
`internal/proxy/public_routes.go`, `internal/publish/feedback_store.go`

## demo Tool

Author narrated demo videos with the in-repo demo engine
(`docs-site/screenshots/engine/demo.mjs`). This is a **repo-checkout
capability**, not a feature of the installed `agnt` binary: the engine ships in
the agnt repository's `docs-site/screenshots` tree. Running `demo` from a
project that has no engine checkout returns a loud error naming the requirement
(`IsError`), never a silent no-op.

Actions:
- `list` — Enumerate demos under `docs-site/screenshots/demos`, each with a
  segment breakdown (id + type) and a narration summary (voice + segment count).
  Resolved client-side from the project path; no daemon roundtrip.
- `record` — Start a recording as a **daemon-managed process** (via `PROC RUN`)
  and return a `process_id` immediately. The recording survives and reports like
  any managed script: observe with `proc {action:"output"|"status"}` and stop
  with `proc {action:"stop"}`. Recordings never auto-restart. Pass
  `only:"seg1,seg2"` to record specific segments (engine `--only`).
- `assemble` — Re-mux an already-recorded demo from its segment captures
  (engine `--assemble-only`), also as a managed process returning a `process_id`.
- `inspect` / `publish` — Reserved cut-point and demo-publish actions. **Not yet
  available**: the engine has no such subcommand today, so these return a loud
  not-yet-available error pending follow-up engine wiring.

The managed process is addressed by a stable id: `demo-<name>` for `record`,
`demo-assemble-<name>` for `assemble`.

Examples:
```
demo {action: "list"}
demo {action: "record", name: "incident-inbox"}
demo {action: "record", name: "incident-inbox", only: "card-intro,fix"}
demo {action: "assemble", name: "incident-inbox"}
proc {action: "output", process_id: "demo-incident-inbox"}
proc {action: "stop", process_id: "demo-incident-inbox"}
```

**Key Files**: `internal/tools/demo.go` (tool + engine resolution + list),
records/assembles via `internal/daemonclient` `PROC RUN`
(`internal/daemon/hub_proc.go`). Engine: `docs-site/screenshots/engine/demo.mjs`.
Session-scoping classified in `.claude/rules/daemon-architecture.md` §
Tool session-scoping (Client-side project-scoped).

## responsive_audit Tool

Run responsive design audits across multiple viewport sizes. Detects layout issues, content overflows, viewport-specific accessibility problems by loading page in hidden iframes at target sizes.

**Default Viewports**:
- Mobile: 375x667 (iPhone SE)
- Tablet: 768x1024 (iPad)
- Desktop: 1440x900

**Checks Available**:
| Check | Description |
|-------|-------------|
| `layout` | Collapsed content, fixed element coverage, margin/padding squeeze |
| `overflow` | Horizontal scroll, clipped content, truncated text, squeezed images |
| `a11y` | Touch target size (mobile), iOS zoom triggers, readability issues |

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `proxy_id` | string | required | Proxy ID to run audit on |
| `viewports` | array | mobile/tablet/desktop | Custom viewports `[{name, width, height}]` |
| `checks` | array | all | Checks to run: `["layout", "overflow", "a11y"]` |
| `timeout` | int | 10000 | Load timeout per viewport (ms) |
| `raw` | bool | false | Return full JSON instead of compact text |
| `profile` | string | `full` | Finding projection: `bug` (top 5 by severity), `release`, or `full` |

**Profiles** (compact output projection, applied Go-side over the raw finding
JSON): `bug` renders only the top 5 findings by severity (critical > warning >
info, stable within a severity) — the triage view; `release` and `full` render
every finding.

**Examples**:
```json
responsive_audit {proxy_id: "dev"}
responsive_audit {proxy_id: "dev", checks: ["layout", "overflow"]}
responsive_audit {proxy_id: "dev", viewports: [{name: "xs", width: 320, height: 568}]}
responsive_audit {proxy_id: "dev", profile: "bug"}
responsive_audit {proxy_id: "dev", raw: true}
```

**Compact Output Format** — every finding line is followed by its stable `id:`
and one `next:` remediation line (a concrete `__devtool` helper call with the
finding's selector: horizontal scroll → `getContainer`, fixed-position trap →
`getStacking`, clipped/truncated → `getBox`, image overflow → `inspect`):
```
=== Responsive Audit: 3 viewports ===

MOBILE (375px) - 2 issues
  ! [layout] .header - collapsed content, element has text but zero height #a1b2c3d4
    id: a1b2c3d4
    next: __devtool.inspect('.header')
  o [overflow] .sidebar - truncated text without title/tooltip #e5f60718
    id: e5f60718
    next: __devtool.getBox('.sidebar')

TABLET (768px) - 0 issues

DESKTOP (1440px) - 1 issues
  ! [layout] .fixed-nav - fixed element covers 45% of viewport #29a3b4c5
    id: 29a3b4c5
    next: __devtool.getStacking('.fixed-nav')

SUMMARY: 3 issues (1 critical, 2 minor)
PATTERNS: 1 mobile-only, 0 tablet-only, 1 cross-viewport
```

**JSON Output Format** (with `raw: true`):
```json
{
  "viewports": {
    "mobile": {
      "width": 375,
      "issues": [
        {"type": "layout", "severity": "critical", "selector": ".header", "message": "..."}
      ]
    }
  },
  "summary": {"total": 3, "critical": 1, "minor": 2},
  "patterns": {"mobileOnly": 1, "tabletOnly": 0, "crossViewport": 1}
}
```

**Issue Severities**:
- `critical`: Horizontal scroll, collapsed content (breaks layout)
- `warning`: Touch targets too small, fixed elements covering 25-40% of viewport
- `info`: Truncated text without tooltip, small font sizes on mobile

**Pattern Detection**:
- `mobileOnly`: Issues only on mobile viewport
- `tabletOnly`: Issues only on tablet viewport
- `crossViewport`: Issues across all viewports

**Key Files**: `internal/tools/responsive_audit.go`, `internal/tools/responsive_audit_test.go`, `internal/proxy/scripts/responsive.js`

## diagnose Tool

Dead-click triage in one call. `action:"click"` composes **existing** evidence
— no new detection: the last click from the interaction ring buffer
(`__devtool_interactions.getLastClick` — full ring buffer, never a fixed
event window), the element and any obstruction at the
click point (`__devtool.getElementInfo` + `document.elementFromPoint`), the
stacking root (`__devtool.getStacking`), the fixed/containing-block trap
(`__devtool.getContainer`), and `browser_js` incidents recorded since the
click. The browser half is `__devtool.diagnoseClick(opts)` in
`internal/proxy/scripts/diagnostics.js`; the Go side owns the verdict and the
compact rendering.

### action:"layout"

Layout triage in one call — composes **existing** producers only: layout.js
`diagnose()` (containing-block traps, ineffective z-index, click interception,
clipped descendants; parsed Go-side only via `parseLayoutDiagnostics`, the
same parser as `currentpage action:"layout"`), the responsive-risk scan
(`__devtool_responsive_risk.checkResponsiveRisk`) for the **current viewport
only** — no multi-viewport sweep, that stays with `responsive_audit` — and,
for each offscreen/overflow/clipped finding, the stacking or container cause
via `__devtool.getStacking`/`__devtool.getContainer`. The browser half is
`__devtool.diagnoseLayoutComposite(opts)` in `diagnostics.js`.

Every finding carries a stable id (`finding.StableID("layout", selector,
type)`) and, when one was found, its cause inline in the evidence.

`selector` narrows the diagnosis to one subtree (findings outside it are not
returned). `viewport {width, height}` resizes the viewport via the existing
proxy resize action before diagnosing and restores the prior state
afterwards — including when the diagnosis exec fails. The prior state is read
from the shell's content-frame style: a full-bleed frame is restored via the
`__devtool_resize_content(0,0)` reset, a prior explicit resize is re-applied
as px. No raw `innerWidth` exec is ever issued against the inner frame.

**screenshot_recommended** — a single block populated ONLY when at least one
finding is visual (`Visual=true`: overflow, clipped, offscreen, overlap). It
names the selector to frame and the exact call:
`__devtool.screenshot({selector: '<sel>', name: 'diagnose_layout'})`. A page
with only ineffective-z-index findings yields no block, in compact and raw
output alike.

```json
diagnose {action: "layout", proxy_id: "dev"}
diagnose {action: "layout", proxy_id: "dev", selector: ".sidebar"}
diagnose {action: "layout", proxy_id: "dev", viewport: {"width": 375, "height": 667}}
```

```
=== diagnose layout (dev) ===
findings: 1

[error] Element causes horizontal scroll without overflow-x setting; container cause: trapped by ".page-shell" (transform)
  id: 4d5e6f70
  next: proxy exec __devtool.getContainer('.wide-table')

screenshot_recommended: __devtool.screenshot({selector: '.wide-table', name: 'diagnose_layout'})
  frame: .wide-table
```

Verdicts: `issues_found`, `no_issues`, `insufficient_evidence` (helper absent
— old bundle — or an unresolvable `selector`; the exact reason is carried,
never a fabricated diagnosis).

**Key Files**: `internal/tools/diagnose.go`, `internal/tools/diagnose_layout.go`, `internal/tools/diagnose_test.go`, `internal/tools/diagnose_layout_test.go`, `internal/proxy/scripts/diagnostics.js`

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `action` | string | required | `click` (dead-click triage) or `layout` (layout + responsive-risk composite) |
| `proxy_id` | string | required | Proxy ID (alias: `id`) |
| `selector` | string | — | `click`: diagnose this element instead of the last recorded click; `layout`: narrow to this subtree |
| `viewport` | object | — | `layout` only: `{width, height}` to resize before diagnosing; restored afterwards |
| `target` | string | `inner` | Frame in the always-wrap model |
| `frame_id` | string | active | Diagnose a specific content frame by id |
| `raw` | bool | false | Return full JSON instead of compact text |

Per-session like `get_incidents`: intentionally no `global` flag.

**Named-element path** (`selector`): the recorded click's position drives the
hit-test only when that click targeted the same element; otherwise the helper
hit-tests the named element's own center — a stale click point never produces
a false obstructor. The helper reports which point was tested
(`hitTestPoint: "click"|"center"`).

**Fail-honest**: any evidence gap — interactions module missing, an
unresolvable selector (surfacing as a `getElementInfo`/`getStacking`/
`getContainer` error), or no hit-test performed — yields
`insufficient_evidence` carrying the exact reason, never `handler_missing`
or `no_click_recorded`.

**Examples**:
```json
diagnose {action: "click", proxy_id: "dev"}
diagnose {action: "click", proxy_id: "dev", selector: ".save-btn"}
diagnose {action: "click", proxy_id: "dev", raw: true}
```

**Compact output per verdict**:

`obstructed` — another element is hit-testable at the click point:
```
=== diagnose click (dev) ===
verdict: obstructed

[error] elementFromPoint at the click point resolves to ".modal-overlay", not the target
  id: 1a2b3c4d
  next: proxy exec __devtool.getStacking('.modal-overlay')
```

`container_trap` — a `position:fixed` element captured by an ancestor
transform/filter/etc.:
```
=== diagnose click (dev) ===
verdict: container_trap

[error] position:fixed element's containing block is ".transformed-parent" via transform
  id: 5e6f7081
  next: proxy exec __devtool.getContainer('.floating-cta')
```

`handler_missing` — the click reached its target and nothing intercepted it:
```
=== diagnose click (dev) ===
verdict: handler_missing

[error] no obstruction at the click point and no containing-block trap
  id: 9a0b1c2d
  next: get_incidents sources=[browser_js]
```

`no_click_recorded` — interaction history empty; no cause is fabricated:
```
=== diagnose click (dev) ===
verdict: no_click_recorded

[warning] interaction ring buffer holds no click event
  id: 3e4f5a6b
  next: watch {events:"interactions"}
```

`insufficient_evidence` — e.g. `__devtool.diagnoseClick` absent (old bundle);
carries the exact reason, never a fabricated diagnosis:
```
=== diagnose click (dev) ===
verdict: insufficient_evidence

[warning] __devtool.diagnoseClick not available on this page (old injected bundle — reload the page through the proxy)
  id: 7c8d9e0f
  next: proxy action=status
```

Finding ids are stable (`finding.StableID("click", selector, cause)`) so
identical evidence reproduces the same id.

**Key Files**: `internal/tools/diagnose.go`, `internal/tools/diagnose_test.go`, `internal/proxy/scripts/diagnostics.js`

## api_audit Tool

7th scored audit. Reads the always-on `api-tracker.js` fetch/XHR buffer (`window.__devtool_api.getCalls()`) — no new recorder. Temporal: needs a fresh page load to populate; empty buffer → "reload page then re-run".

**Four detectors**:
- **waterfall / serial-chain** — B starts ≈ A ends; sums wasted time vs parallel-possible
- **N+1** — URL→template normalization; ≥5 calls sharing one `{id}` template
- **redundant / duplicate** — identical method+url within 2s
- **chatty-load** — call count in the first-3s load window

**Data limitation (fail-honest)**: the call buffer has no response-size field, so over-fetch by payload size is not measurable — the audit emits an explicit `over-fetch-unavailable` info note instead of fabricating a size. A future content-length capture in `api-tracker.js` would unlock real payload-bloat detection.

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `proxy_id` | string | required | Proxy ID to run audit on |
| `raw` | bool | false | Return full JSON instead of compact text |
| `profile` | string | full | Finding projection: `bug` (top 5 by severity), `release`, or `full` |

**Output**: audit-module shape (`score`/`grade`/`summary`/`findings`/`findingSelectors`); score = 100 minus weighted findings → grade A–F. Every compact finding line carries its stable `id:` and one exact `next:` drill-down action (raw JSON findings carry the same `next`):

```
n-plus-one (1)
  [warning] https://x/api/items/1 — 7× GET /api/items/{id} → batch
  id: aa000002
  next: proxylog {action:"query", proxy_id:"dev", url_pattern:"/api/items/"}
```

`next` mapping: `n-plus-one` / `duplicate-call` / `chatty-load` → `proxylog {action:"query", url_pattern:<from finding>}` (n-plus-one uses the finding's template path truncated before the first `{id}` — e.g. `GET /api/items/{id}` → `/api/items/` — so the pattern substring-matches every recorded call URL in the group); `waterfall` → `proxylog {action:"summary", proxy_id}`.

**Key Files**: `internal/proxy/scripts/audit-api.js` (`window.__devtool_audit_api.auditAPIEfficiency`), `internal/tools/api_audit.go`

## loading_audit Tool

8th scored audit. Reads a spinner timeline recorded by a self-contained observer in `mutation.js` (`window.__devtool_spinners.getTimeline()`). Detects loading indicators via `aria-busy`, `role=progressbar|status`, `<progress>`, class/id/aria match `spin|load|skeleton|shimmer|pending|placeholder`, and spin-like `animationName`; correlates each spinner's active window with overlapping api-tracker calls. Temporal: empty timeline → 100/A + "reload page".

**Two detectors**:
- **spinner-cascade** — B appears after A disappears (gap ≤400ms), B in A's region (ancestorPath overlap), A had a resolved API → serial chain depth ≥2; reports serial span vs parallel-possible
- **spinner-fragmentation** — ≥3 spinners active simultaneously under one common ancestor → "consolidate to one master loader"

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `proxy_id` | string | required | Proxy ID to run audit on |
| `raw` | bool | false | Return full JSON instead of compact text |
| `profile` | string | full | Finding projection: `bug` (top 5 by severity), `release`, or `full` |

**Output**: same audit-module shape; score = 100 minus cascade (depth-weighted, +critical) and fragmentation deductions → grade. Compact findings carry `id:` and a `next:` line; spinner findings drill into the page layout:

```
spinner-cascade (1)
  [warning] #content .spinner — 3 loaders fired serially over 2400ms
  id: bb000001
  next: currentpage {action:"layout", proxy_id:"dev"}
```

**Key Files**: `internal/proxy/scripts/audit-loading.js` (`window.__devtool_audit_loading.auditLoading`), `internal/proxy/scripts/mutation.js` (spinner recorder), `internal/tools/loading_audit.go`

## verify_change Tool

Recheck only the findings this session already recorded — the narrow follow-up
after a fix, instead of a broad re-audit. Reads the session Investigation
(`INVESTIGATION GET`), resolves the target set (`finding_ids` when given, else
every finding in the record), and re-runs each target's recorded **Producer**
(the exact tool+args that produced it: `get_incidents`, `responsive_audit`,
`api_audit`, `loading_audit`, `diagnose`) **in-process** by calling the same
handler functions. It never runs a broad/full audit and never re-reads
`currentpage`.

**Comparison by id**: `resolved` (id absent now), `persist` (id still
reported), `new` (ids the same producer reports that were not targets **and
not already recorded** — a recorded non-target id left out by a narrowed
`finding_ids` is never re-reported as new or appended again).
Audit/diagnose dispatchers always re-request raw output internally, so ids
are compared even though the original producer call was compact. A handler
that returns an `IsError` result (for example its audit module not loaded)
is a producer error: its targets count as persist, never resolved.
Screenshots via the existing `__devtool.screenshot` path are captured **only**
for `Visual=true` findings that resolved or changed; the last ref is recorded
as the Investigation's `visual_baseline_ref`.

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `proxy_id` | string | investigation's active proxy | Proxy used for conditional visual-baseline screenshots |
| `finding_ids` | []string | all recorded findings | Finding ids/fingerprints to recheck |
| `since` | string | none | Cursor forwarded to producers that accept one (e.g. `get_incidents`) |
| `raw` | bool | false | Return full JSON instead of compact text |

**Header semantics**: the first line is
`verify_change: PASS|FAIL (<n> resolved, <m> persist, <k> new)`. The header is
**FAIL whenever any target persists** — PASS is impossible while a target is
still reported. A producer error counts as persist (never a false PASS) and
adds a `collection_warnings` line.

**Stale ids**: a `finding_ids` entry that is not in the record (and has no
recorded producer) cannot be rechecked. It produces one
`stale_finding_id: "<id>" ...` entry in `collection_warnings` and is skipped;
the remaining ids are still verified — a per-id failure never aborts the call.

**Merge-back**: after the call the Investigation no longer lists resolved ids
and lists new ones (read back via the next `verify_change` or
`INVESTIGATION GET`); resolved removals run before appends so a re-added id
survives.

**Key Files**: `internal/tools/verify_change.go`, `internal/finding/finding.go` (Producer), `internal/protocol/commands.go` (Investigation/Merge)

## watch Tool

Returns shell command string for streaming daemon events via `agnt monitor` CLI. Bridges MCP clients (which know daemon socket path) to Monitor tool.

**Targets**:
| Target | Description | Required Params |
|--------|-------------|-----------------|
| `errors` | Error and diagnostic events | Optional `proxy_id` |
| `interactions` | User interactions (panel messages, clicks, sketch) | Optional `proxy_id` |
| `process` | Process output stream | Required `process_id` |
| `all` | All daemon events (default) | None |

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `target` | string | `all` | What to watch: `errors`, `interactions`, `process`, `all` |
| `proxy_id` | string | none | Filter to specific proxy |
| `process_id` | string | none | Filter to specific process (required for `process` target) |

**Output**: Returns `command` string (e.g., `agnt monitor --socket /tmp/devtool-mcp-1000.sock --types error,diagnostic --format compact`) and human-readable `description`.

**Key Files**: `internal/tools/watch.go`, `internal/tools/watch_test.go`

## Frontend API

**`window.__devtool`** (~50 diagnostic primitives):

**Core**:
- `log(message, level, data)` (returns `{sent: boolean}`), `screenshot(name)`, `isConnected()`
- `interactions.getHistory/getLastClick/getLastClickContext()`
- `mutations.getHistory/highlightRecent()`
- `window.__devtool_vitals` — buffered PerformanceObserver accumulator registered at
  injection time (content frame only): `{lcp, cls, inp, longTasks:[{start,duration}]
  (capped 50), longTasksCapped}`. Unsupported metrics stay `null`.

**Interaction helpers** (JSON-safe returns, no live DOM nodes):
- `fill(selector, value)` — React-safe form fill (native value setter + input/change events;
  handles input/textarea/select/checkbox/contenteditable)
- `clickElement(selector)` — realistic pointerdown→mousedown→pointerup→mouseup→click sequence
- `waitForElement/waitForVisible/waitForRemoved(selector, timeout?)` — Promise-based waits
  (single timeout path, observers always cleaned up)
- `scrollIntoView(selector)`

**Indicator & Modes**:
- `indicator.show/hide/toggle/togglePanel()`
- `sketch.open/close/toggle/save/toJSON/fromJSON()`
- `design.start/stop/selectElement/next/previous/addAlternative/chat()`
- `responsive.open/close/toggle/setWidth/getState()` — responsive mode (4th indicator mode)

**Diagnostics** (categories):
- Element Inspection (9): getElementInfo, getPosition, getComputed, etc.
- Layout Diagnostics (4): findOverflows, findStackingContexts, findOffscreen (all bounded:
  4000-element scan, 100 results, `total`/`capped` reported), diagnoseLayoutIssues
- Accessibility (5): getA11yInfo, auditAccessibility (3 modes), getContrast, etc.
- Quality Auditing (10+): auditDOMComplexity, auditPageQuality, auditCSS, etc.

**Scored audits (8)**: `auditAll` aggregates eight scored audits into a weighted overall grade — DOM, CSS, performance, security, SEO, accessibility, **API efficiency** (`__devtool_audit_api.auditAPIEfficiency`), and **loading/spinner** (`__devtool_audit_loading.auditLoading`). Weights: security 1.5, accessibility 1.3, performance 1.2, api 1.1, loading 1.1, seo 1.0, dom 0.8, css 0.7. Both new audits are guarded — `auditAll` degrades cleanly when a module is absent. The API + loading audits are temporal: they read the fetch/XHR buffer and spinner timeline, so they need a fresh page load to populate.

### Modern-CSS opportunities (advisory, inside `auditCSS`)

`auditCSS` runs one extra check, `modern-css-opportunities`, alongside its
hygiene checks. It answers a different question — *what does this page
hand-roll that CSS now has a primitive for* — and reports it under
`modernCSS.opportunities` (AI shape) / `modernCSS` (raw shape).

These findings are **advisory**: `severity: "info"`, `advisory: true`, and they
never move the CSS score. A page written before a primitive shipped is not
defective, so scoring it would mark down correct work.

| Detected shape | Suggested primitive | Support as of 2026-09 |
|---|---|---|
| one base color repeated once per opacity level | `alpha(var(--brand) / 60%)` relative color | Chrome 151+, Safari 27+, Firefox nightly |
| `calc((v - a) / (b - a))` normalisation | `progress(v, a, b)` (accepts mixed units) | Chrome/Edge/Safari; Firefox intent-to-ship |
| ≥3 rules differing only by an attribute value | `attr(data-size type(<length>), 1rem)` | Chrome/Edge/Safari; Firefox intent-to-ship |
| ≥3 `:nth-child(N)` rules differing only by index | `sibling-index()` / `sibling-count()` | interoperable (Chrome, Safari 26.2+, Firefox) |
| `line-height` plus asymmetric vertical padding | `text-box-trim: trim-both; text-box-edge: cap alphabetic` | Chrome (2025-02), Safari; Firefox intent-to-prototype |
| `width: fit-content` a wrapper must also hug | `max-content-sizing: shrink-to-fit` on the wrapper | newest of the set, not yet interoperable |

Every finding carries `baseline` and `fallback` for exactly one reason: these
primitives shipped at different times and one of them is interoperable
nowhere. A suggestion without its support reality invites a declaration the
engine drops on the floor — silent failure, which this project treats as worse
than no suggestion. Read `baseline` to decide between shipping it outright and
shipping it behind `@supports`, and `fallback` for what to keep beside it.

**Not detected, and deliberately so:**

- **`Promise.allKeyed({...})`** — the object-keyed sibling of `Promise.all`,
  which drops the positional-array dance (`const [user, posts] = await
  Promise.all([...])`). It is a JS-source pattern; the audits answer for the
  rendered page, not for source, so this one is guidance here rather than a
  finding.
- **`<camera>` / `<microphone>`** — prototype stage, no declarative benefit
  over `getUserMedia()` yet (you still wire up permission and stream events in
  JS). Do not recommend them in generated code.

**Coverage limits (fail-honest)**: cross-origin stylesheets cannot be read at
all, and the walk stops after 3000 rules so a huge sheet cannot stall the
inspected page. Both cases append to the audit's `note` field rather than
silently under-reporting. One further limit has no note because nothing is
lost: an engine folds purely numeric `calc()` before an audit can read it, so
`progress()` is only reported for the `var()`-bearing form — which is the form
the primitive actually helps with.

**Tests**: `internal/proxy/scripts/jstest` (vitest + jsdom, `make test-js`)
runs the shipped audit over real documents — every detection paired with the
near-miss that must stay silent. The Go side keeps an always-on contract guard
that needs no node install.

### CSS layering & positioning introspection (agent-targeted)

The hardest CSS bugs are non-textual: the decisive evidence is *computed*
stacking/containing-block state, not the source. These bugs are the
sweet spot where source-only LLM reasoning reliably suggests the wrong fix
(bump z-index, suppress, `!important`). Three helpers surface the runtime
cause directly. The CSS-trigger detection is centralized in
`utils.stackingContextTriggers` / `utils.containingBlockTrap`
(`internal/proxy/scripts/utils.js`) so `getStacking`, `getStackingChain`,
and `findStackingContexts` can never disagree on what creates a context.

- **`getStacking(selector)`** → `{zIndex, position, createsContext,
  selfTriggers:[{property,value}], stackingRoot, rootTrigger:{property,value},
  chain:[{selector,triggers}], opacity, transform, filter}`. `stackingRoot`
  is the nearest ancestor stacking context — z-index is only resolved against
  siblings inside that same root, so a child's `z-index:9999` is meaningless
  when its root is a sibling of the thing it wants to cover. `rootTrigger` is
  the exact CSS property (e.g. `transform: translateZ(0)`) that created the
  offending root — the thing to remove/relocate, instead of bumping z-index.
- **`getContainer(selector)`** → for `position:fixed`/`absolute` adds
  `{expectedContainingBlock, actualContainingBlock, trappedBy:{selector,
  property,value}|null, escaped}`. `trappedBy` is the distant-ancestor
  property (`transform`/`filter`/`will-change`/`contain`) that captures a
  `fixed` element so it scrolls/positions relative to the ancestor instead of
  the viewport — the invisible-in-source cause of "my fixed header scrolls
  away." `null` ⇒ correctly viewport-relative.
- **`findStackingContexts()`** → `{contexts:[{selector, zIndex,
  triggers:[{property,value}], reason:[string]}], count}`. Detects the **full
  spec trigger set** — positioned+z-index, opacity, transform, filter,
  backdrop-filter, perspective, clip-path, mask, mix-blend-mode,
  `isolation:isolate`, will-change, contain, and flex/grid children with
  z-index — not just the four the old implementation caught. `reason[]` is a
  flat property-name list kept for back-compat; `triggers[]` carries the
  removable cause with values.

- **`diagnoseLayoutIssues()`** (= `window.__devtool_layout.diagnose()`) → one
  bounded synchronous pass (~30-80ms; 4000-element budget, 15 findings per
  check, `capped` flag) over four cause→symptom layout-bug classes, each
  finding naming the offending ancestor (`cause`/`cause_property`), the correct
  `fix`, and the common wrong fix to `avoid`:
  - **containing-block-trap** — `position:fixed`/`absolute` captured by an
    ancestor `transform`/`perspective`/`filter`/`will-change`/`contain`, so the
    element resolves against that ancestor instead of the viewport/positioned
    parent the author expects.
  - **ineffective-zindex** — `z-index` on a `position:static`, non-flex/grid
    element: silently discarded, not losing a comparison.
  - **click-interception** — a visible interactive element whose center point
    resolves to a different element (a transparent overlay eats clicks/taps).
  - **clipped-descendant** — content cut off by the nearest ancestor with
    `overflow:hidden/clip`; only the boundary element is reported, not every
    descendant.
  Returns `{findings:[{check, severity, selector, cause, cause_property,
  detail, fix, avoid}], count, scanned, capped, by_check}`.

**Promotion** (findability): `getStacking`/`getContainer` are in the injected
cheat sheet (`internal/agntprompt/cheatsheet.go`) under a symptom→helper map,
and `internal/tools/exec_hints.go` redirects raw `z-index` writes →
`getStacking` and `position:fixed` debugging → `getContainer` when an agent
writes such JS through `proxy exec`. So the agent is steered to the runtime
evidence at the moment it is about to apply the wrong source-only fix.

### Always-Wrap & Content Frames

The proxy wraps every top-level HTML navigation in an outer **chrome shell** whose
body is a single content `<iframe>`; the real page loads inside that frame. Proxy
UI (indicator/panels/overlays) lives in the shell; page telemetry + the live
`window.__devtool` runtime live in the **content frame**. This isolates proxy
chrome from page content and gives a stable interaction target. Full design:
**`docs/responsive-canonical-target.md`**.

- **Roles** (resolved per frame into `window.__devtool_frame_role`): `chrome`
  (outer shell — UI only, no telemetry WS), `content` (the wrapped page — full
  runtime, tagged with a `frame_id`), `passive` (foreign embeds — silent).
- **Wrap gating**: only requests with `Sec-Fetch-Dest: document` are wrapped.
  Requests from `fetch()` (`Sec-Fetch-Dest: empty`), nested browsing contexts,
  headerless clients, and requests carrying the `__devtool_frame` marker are
  served unwrapped.
- **Frame registry**: the shell tracks live content frames + an active-target
  pointer (the last-interacted frame). `proxy exec` and the visual/audit tools
  (`responsive_audit`, `snapshot`, `screenshot`, `api_audit`, `loading_audit`)
  default to the **active content frame**; `proxy {action:"exec", frame_id:"…"}`
  targets a specific frame.
- **Outer vs inner exec target**: the shell and its content frame carry distinct
  ids (`chrome-<fid>` vs `<fid>`), so a REPL can be aimed at either.
  `proxy {action:"exec", target:"outer", code:"…"}` runs against the **chrome
  shell** (the proxy UI runtime / host) via the `@chrome` role token;
  `target:"inner"` (default) scripts the page content frame.
- **Drive the page from outside** (always-wrap makes these reload-free):
  - `proxy {action:"navigate", direction:"back|forward|reload"}` or
    `{direction:"goto", target_url:"…"}` — drives the page content frame; the
    navigation is deferred a microtask so the exec reply returns before unload.
  - `proxy {action:"resize", width:375[, height:…]}` — resizes the live content
    frame in place from the shell (no reload, page state preserved; `width:0`
    resets). Resize, then run `api_audit`/`loading_audit`/`responsive_audit`
    (they target the inner frame) to measure the page at that viewport.
- **Telemetry** (error/fetch/xhr/interaction/mutation) is tagged with the
  emitting `frame_id`; the incident fingerprint and `proxylog`'s `LogFilter.Frames`
  are frame-aware so the same error in two frames is not collapsed.

### Responsive Mode

Interactive responsive workbench (4th indicator mode beside sketch/design):
1. Opens a drawer hosting a live device-preview `<iframe>` of the current page.
   Under always-wrap the preview is sourced from the page URL **with the
   `__devtool_frame` marker** (not `location.href` verbatim, which would load
   another shell) so it loads unwrapped and registers as its own content frame.
2. Width control — slider, numeric input (320–1920), preset chips (375/768/1440), edge drag handle; every control funnels through one `applyWidth()` so human-driven and agent-driven (`setWidth`) changes share one source of truth
3. Programmatic layout-shift detection (debounced 250ms) reuses `responsive.js` detectors against the iframe at the current width; findings new at the current width are flagged `isNew` and overlaid as severity-colored boxes on the frame; returned via `getState().shifts/selectors`
4. `[Send to agent]` emits a `responsive_request` event `{width, shifts[], selectors[]}` → proxylog + overlay notifier + channel sink; agent fixes then re-verifies via `setWidth(w)`
5. `[Auto-sweep]` runs the headless multi-viewport `responsive.js` audit and lists all findings

**Event types**: `responsive_request` (channel-forwarded handoff), `responsive_state` (`{width, shiftCount}` on open/settle; proxylog/overlay only, intentionally NOT channel-forwarded to avoid per-settle spam).

**Key Files**: `internal/proxy/scripts/responsive-mode.js`, `indicator.js`, `api.js`

**Audit Output Modes**:
- **Default** (AI-optimized): Grouped issues by type, limited examples, token-efficient
- **Raw** (`raw: true`): Verbose detailed format with all issues and context

**Accessibility Modes**:
- **Standard** (axe-core): WCAG 2.1, 90+ rules, ~100-300ms
- **Fast**: Focus indicators, color schemes, ~50-100ms
- **Comprehensive**: State-specific contrast, responsive, ~500-2000ms
- **Basic**: Fallback, minimal checks, ~10-50ms

## Event Streaming (`agnt monitor`)

CLI subcommand streams daemon events to stdout real-time:
```bash
agnt monitor                           # All events
agnt monitor --types error,diagnostic  # Errors only
agnt monitor --proxy dev --format json # NDJSON for specific proxy
agnt monitor --process app             # Process output follow mode
```

Flags: `--types`, `--proxy`, `--process`, `--severity`, `--format` (compact/json), `--socket`
Auto-reconnects on daemon restart. Clean exit on SIGINT/SIGTERM.

## Tunnel Integration

Cloudflare/ngrok/tailscale support for mobile testing (`cloudflared`/`ngrok` =
public internet; `tailscale` = tailnet-private HTTPS at this node's MagicDNS
name, reachable only from your tailnet, one service per node at root `/`):
```bash
proxy {action: "start", bind_address: "0.0.0.0", ...}
tunnel {action: "start", provider: "cloudflare", local_port: 12345, proxy_id: "dev"}
tunnel {action: "start", provider: "tailscale",  local_port: 12345, proxy_id: "dev"}
```

For the tailnet alone, `bind_address: "tailscale"` needs no tunnel process: the
proxy listens on this node's tailnet IPv4 and keeps its own port, so it answers
at `http://<magicdns-name>:<port>`. It is the one non-loopback bind that does
not need `allow_external`, and it replaces the loopback listener rather than
adding to it. `.agnt.kdl` spells the same thing `bind "tailscale"`
(`docs/configuration.md` § Proxy Bind Address); `:tailscale` in the overlay
writes it. Use `tunnel tailscale` instead when you want HTTPS at the node root.

### Control WebSocket origins

The injected client opens `/__devtool_metrics` against whatever host the page
was loaded from. The proxy accepts that upgrade only from three origins
(`checkWSOrigin`, `internal/proxy/server.go`); anything else is refused with
403 and the overlay stays silent:

- same-origin on a loopback authority (`localhost`, `127.0.0.1`, `::1`);
- the proxy's configured `public_url` (set by `tunnel start ... proxy_id`, or
  `public-url` in `.agnt.kdl`);
- same-origin on one of this node's own tailnet identities (MagicDNS name or
  tailscale IP, read from `tailscale status --json` and cached for a minute).
  This covers a proxy bound to `0.0.0.0` and opened directly at
  `http://<node>.<tailnet>.ts.net:<port>` with no `tailscale serve`. Without a
  logged-in tailscale binary this branch fails closed.

The loopback-only rule for arbitrary hostnames is deliberate: a hostname an
attacker rebinds to your listener would otherwise satisfy same-origin. A
node's own tailnet identity is never attacker-controlled, so it is safe to
trust.

### Bounds when you tunnel a listener

Tunnelling a listener does not relax its caps — the same bounds hold in every
posture, so exposing a listener is purely a routing decision and never doubles
as a hardening decision. Whatever you point `cloudflared`/`ngrok` at is hardened
to public standard:

- **Dev proxy + public plane + `agnt publish serve`** carry the transport +
  connection caps (`internal/httpcaps`): `ReadHeaderTimeout 10s`, `IdleTimeout
  120s`, `MaxHeaderBytes 1 MiB`, and **max 256 concurrent connections**
  (`netutil.LimitListener`). The public plane and `publish serve` also cap
  `ReadTimeout 30s` / `WriteTimeout 60s`; the dev proxy deliberately does **not**
  (it streams proxied bodies and hijacks WebSocket upgrades like Vite HMR, which
  a whole-request write deadline would sever — slowloris/fd exhaustion stay
  bounded by the header/idle/connection caps).
- **Public walkthrough plane** additionally rate-caps: artifact `GET` per
  `(share, IP)` ⇒ `429`, and guarded upstream fetches **per origin, across all
  shares** ⇒ `503` (the amplification bound). The **dev proxy is not
  rate-capped** — it forwards to your own backend, not a third party. Values,
  the `public-plane` KDL block, and the amplification rationale:
  [`configuration.md`](configuration.md#public-plane-block-request-rate-limits).
