# Responsive frame as canonical interaction target — design + test plan

**Status:** IMPLEMENTED — all slices (1–8) of EPIC `01KVEX7751X30B4JTWPAYFDB8Y` landed.
**Audience:** implementers + maintainers. Sections below describe the shipped model;
Slice 7's §7 note records the one deliberate divergence from the original design.

## 1. Problem

`responsive-mode.js` opens a panel whose device preview is an `<iframe>` with
`iframe.src = window.location.href` (`internal/proxy/scripts/responsive-mode.js:325`).
Both frames share the **same proxied origin**, and the proxy injects the
instrumentation bundle into *every* `text/html` response with no frame awareness
(`internal/proxy/injector.go:174`, gated only by `ShouldInject` content-type at
`injector.go:196`). The bundle runs **twice** — outer shell and inner frame.

Consequences: two telemetry contexts (`core.js` opens a 2nd WebSocket;
`interaction.js:545` + error/fetch/xhr/mutation hooks fire from both); ambiguous
indicator (`indicator.js` guards UI behind `isNestedFrame()` at
`indicator.js:2030-2042`, but that makes the **top** frame canonical and forwards
nested hotkeys *up* — opposite of what we want); MCP exec/audits hit the wrong
frame (`ProxyServer.ExecuteJavaScript` broadcasts to **all** WS clients —
`files.go:128,150`); currentpage + proxy UI track the shell, not the content.

**This is a special case of a general gap: the proxy has no separation between
its own UI chrome and page content, and no way to address one frame among
several.**

## 2. Chosen architecture — always-wrap + frame registry

Two decisions, confirmed with the maintainer:

1. **The proxy always wraps proxied HTML in an outer chrome shell whose body is
   a content `<iframe>`.** Proxy UI (indicator, panels, palette, overlays) lives
   in the **outer shell**, permanently isolated from page content. Page content
   lives in **content frame(s)**. Responsive mode then just *resizes the existing
   content frame* — it no longer nests a second iframe.

2. **Support multiple content frames** (responsive preview, design preview, split
   views, genuine nested app iframes). A **frame registry** tracks every live
   content frame; an **active-target resolver** designates the one that
   telemetry/exec/audit/currentpage default to. Frames are addressable by
   `FrameID`, so a tool can target a specific frame when needed.

This subsumes the original "inner frame canonical" contract: the content frame is
*always* the interaction target; the shell is *always* chrome.

### 2.1 Why always-wrap (vs. suppress-one-context)
A guard that suppresses the duplicate context only fixes responsive mode and
leaves the shell/content boundary implicit. Always-wrap makes the boundary
structural and permanent: proxy UI can never collide with page DOM/CSS/JS,
exec/audit always have a stable content target, and multi-frame views compose
naturally. Cost: the wrap must handle navigation, redirects, and frame-deny
headers (§4).

## 3. The contract

For any proxied page (responsive mode on or off):

1. **One telemetry context per content frame** — emitted from the content frame,
   tagged with its `FrameID`. The shell emits none.
2. **One indicator**, owned by the shell (chrome), never duplicated per frame.
3. **MCP exec + visual/audit tools hit the active content frame** (or an
   explicitly addressed `FrameID`).
4. **Proxy UI + currentpage reflect the active content frame.**
5. **Clean lifecycle** — frames register on load, deregister on unload; closing
   responsive mode reverts the content frame to full size with no leaked
   listeners/WS/guards; the shell is stable across all of it.

## 4. Always-wrap mechanics

### 4.1 The wrap (server, Slice 2)
`InjectInstrumentationAndMeta` (`injector.go:174`) is replaced/extended: for a
top-level `text/html` navigation response, the proxy returns a **shell document**
— minimal HTML carrying the proxy bundle (chrome role) and a single content
`<iframe>` whose `src` is the requested path with a marker (e.g.
`?__devtool_frame=<id>` or an internal route) so the proxy serves the **real**
content there, injected with the bundle in `content` role.

The shell is served only for **top-level navigations**. Sub-resource and
content-frame requests get today's behavior (content + bundle, no re-wrap).
Distinguishing top-level vs. content request: the content-frame request carries
the `__devtool_frame` marker; absence ⇒ wrap.

### 4.2 Navigation sync (Slice 2)
Slice 2 **adds** shell-side listeners for content-frame `popstate`/`hashchange`/
`load` (these do not exist today — net-new code). Same-origin direct reach makes
this viable and is already load-bearing elsewhere (the shell reads
`iframe.contentWindow`/`contentDocument` directly — `responsive-mode.js:406,463`).
On each content-frame navigation the shell syncs the browser URL bar via the
History API so back/forward and bookmarks work.

### 4.3 Frame-deny headers — pragmatic non-issue
A local dev proxy points at the dev's own app on the same proxied origin;
`X-Frame-Options` / CSP `frame-ancestors` framebusting effectively never bites in
that setting. If a proxied content response *does* carry them, strip them on the
proxied response (we are the origin serving it; the proxy already rewrites body +
`Content-Length` at `rewrite.go:96-104`). One-line strip — not a headline risk,
not a separate slice.

### 4.4 Fallback (fail-safe)
If a content frame genuinely cannot be framed (cross-origin that we can't rewrite,
or `iframe.contentWindow.document` access throws — pattern already handled at
`responsive-mode.js:407-410`), the wrap **degrades to unwrapped**: serve content
directly with the bundle in `canonical` role (today's behavior). No crash, no
double context. Logged so it's visible, not silent.

## 5. Frame registry + active-target resolver (the primitive)

### 5.1 Browser side (Slice 2)
- Each content frame, on bundle init, computes its **role** and **registers** with
  the shell (same-origin direct call to `window.top.__devtool_frames.register`):
  `{ frameId, url, role: 'content' }`. The shell is `chrome`; foreign frames we
  don't own stay `passive` (strengthens today's `isNestedFrame` guard).
- The shell holds `__devtool_frames`: a registry (`Map<frameId, FrameHandle>`) and
  an **active** pointer. Active = last-interacted content frame (updated on
  focus/pointer/responsive-open); single content frame ⇒ trivially active.
- `FrameID` is minted shell-side and passed to the content frame via the
  `__devtool_frame` marker so both sides agree.

Role table:

| Role | Frame | Behavior |
|------|-------|----------|
| `chrome` | outer shell | proxy UI (indicator/panels/overlays); holds frame registry + active pointer; **no** page telemetry/exec runtime |
| `content` | wrapped content iframe(s) | full runtime: telemetry WS, error/interaction/mutation/perf hooks, exec target, currentpage signal — all tagged with `frameId` |
| `passive` | foreign nested embed | no UI, no telemetry, no exec |

### Browser frame-context adapter

`internal/proxy/scripts/frames.js` is the sole interpreter of the always-wrap
topology. It exposes `window.__devtool_context`, which owns role/frame identity,
shell and active-content lookup, marker-bearing/clean URL construction, URL-bar
sync, reload, resize, and shell exports. Other first-party browser modules must
use this adapter rather than reading `window.parent`/`window.top`, frame-role
globals, the frame registry, marker parameter, or content iframe DOM ID.

This boundary keeps the two execution contexts explicit: `chrome` is the outer
proxy UI shell; `content` is the inner application page and the canonical
telemetry/currentpage context.

### 5.2 Server side (Slices 4–6)
With multiple live content frames, "only one frame answers" no longer holds, so
frame addressing becomes real (this was the reserved alternative in the prior
draft — now the design):

| Site | File:line | Change |
|------|-----------|--------|
| WS envelope | `ws_handler.go:216` (`msg.URL`, `SessionID`) | add `FrameID` field; content frames stamp it on every message |
| Log types | `logger.go:13-72` union, `URL` fields | add optional `FrameID` to the entry envelope (one field, not per-type) |
| Exec dispatch | `files.go:128-150` `ExecuteJavaScript`→`broadcastRaw` | carry target `frameId` in the `execute` message; content frame runs only if it matches (default = active) |
| Exec reply | `files.go:134,360`, `ws_handler.go:333` | reply tagged with `frameId`; resolve the right pending exec |
| PageTracker | `pagetracker.go:177` `ResolveSession` | resolve by `frameId` when present, URL fallback |
| Log filter | `logger.go:780-792` `LogFilter` | add `Frames []string` |
| Dedup | `get_errors.go:49` `dedupKey` | include `frameId` so same error in two frames isn't collapsed |

### 5.3 Active-target resolution for tools (Slice 5)
MCP `proxy exec` / `responsive_audit` / `snapshot` / `screenshot` / `api_audit` /
`loading_audit` default to the **active** content frame. Optional `frame_id`
parameter targets a specific frame. The shell exposes the active `frameId`; the
daemon/tool passes it down. Default keeps today's single-arg ergonomics.

## 6. Migration inventory (every site a later slice touches)

### 6.1 Injection / wrap / self-init (browser + server)
| Site | File:line | Today | Target |
|------|-----------|-------|--------|
| Top-level injection | `injector.go:174`, `rewrite.go:27` | injects bundle into page, no wrap | wrap top-level nav in shell; serve content into iframe with `content` role |
| Frame-deny headers | `rewrite.go` response path | passed through | strip `X-Frame-Options` + `frame-ancestors` on proxied content |
| `core.js` WS/session | bundle eval | unconditional WS | WS only in `content` role; stamp `frameId` |
| `interaction.js` | `interaction.js:545` | attach at eval | attach only in `content` |
| `indicator.js` UI + hotkey | `indicator.js:2030` (init guard), `2055` (hotkey handler), `2069` (createUI) | top-canonical, nested forwards up | UI lives in `chrome` shell only; single hotkey routed by role |
| error/fetch/xhr/mutation/perf hooks | core/diagnostics | fire every frame | fire only in `content`, tagged `frameId` |
| `responsive-mode.js` | `responsive-mode.js:325` | creates a nested iframe | resize the **existing** content frame; no new iframe; update active pointer |

### 6.2 Telemetry / exec / audit / currentpage (server)
All covered by §5.2 / §5.3 (FrameID threading + active resolver).

### 6.3 Proxy UI (daemon/overlay)
`internal/overlay/*` palette/panels unaffected (daemon-side). Confirm no duplicate
proxy status slot (cf. commit f569c55).

## 7. Per-slice acceptance + regression tests

Every slice ends green: full `make test` + `go test -race` on affected packages.
Browser logic exercised via served-bundle assertions + the proxy exec/audit
harness; pure role/registry logic unit-tested via the embedded-bundle test pattern.

- **Slice 2 — Wrap + frame-target resolution primitive + tests.**
  Always-wrap top-level navs; shell + content iframe; frame registry + role
  resolver; nav-sync; frame-deny strip; unwrapped fallback. *Tests:* top-level
  `text/html` ⇒ shell document containing exactly one content iframe; content
  request (with marker) ⇒ unwrapped content + bundle; role table — shell=`chrome`,
  content=`content`, foreign=`passive`; `X-Frame-Options`/`frame-ancestors`
  stripped on content; cross-origin / access-throw ⇒ unwrapped fallback (logged);
  registry register/active-pointer table-driven. **URL-bar sync:** content-frame
  `pushState` ⇒ shell address bar reflects the new URL; browser back-button
  navigates the content frame, not a shell reload.
- **Slice 3 — Single-injection guard.**
  Gate core/interaction/indicator/hooks on role. *Tests:* one telemetry WS per
  content frame, one indicator (in shell), one interaction-listener set per
  content frame; foreign frame silent.
- **Slice 4 — Route error + telemetry to the content frame.**
  FrameID on WS envelope + log entry; dedup includes frameId. *Tests:* an
  error/fetch/xhr/interaction/mutation in a content frame logged **once** with its
  `frameId` + page URL; shell logs none; same error in two frames not collapsed.
- **Slice 5 — Route MCP exec + visual/audit to active target.**
  FrameID in exec message + reply; tools default to active frame, accept
  `frame_id`. *Tests:* `proxy exec` evaluates against the active content frame
  (assert on a value only in framed content); audits operate on it; explicit
  `frame_id` targets a specific frame; single tagged reply (no double-answer).
- **Slice 6 — Proxy UI + currentpage track the content frame.**
  *Tests:* currentpage reports the active content frame's URL/title/state;
  PageTracker resolves by `frameId`.
- **Slice 7 — Lifecycle teardown / clean revert.**
  *Tests:* content frame deregisters on unload; responsive open→close reverts to
  full size, no leaked listeners/WS/guards; shell stable; reopen idempotent.
  **Implemented divergence:** rather than re-parenting/resizing the shell's live
  content frame in place (re-parenting an iframe reloads it in all browsers,
  losing page state, and is a near-rewrite of the 600-line panel), the responsive
  device preview (`responsive-mode.js`) and the off-screen auto-sweep frames
  (`responsive.js`) source themselves from the page URL **with the
  `__devtool_frame` marker** (`responsiveContentSrc` / `markedContentSrc`). Each
  loads UNWRAPPED and registers as its own content frame; the preview reports
  active on focus so exec/audits target it; close removes the panel + iframe and
  `pagehide` deregisters. The shell's main content frame sits behind the panel,
  restored on close. This fixes the real shell-in-shell recursion and integrates
  with the frame registry with far less risk than in-place re-parenting.
- **Slice 8 — End-to-end + docs + memory.**
  *Tests:* full nav→interact→exec→audit→responsive→close E2E; update
  `docs/mcp-tools.md` + proxy docs; record always-wrap + frame-registry model in
  project memory.

## 8. Risks called out

- **History/URL-bar sync** is the fiddly piece: a content frame that does its own
  `pushState` must not desync the shell. Covered by Slice 2 nav-sync tests.
- **Frame-deny headers** are a non-issue for the local-dev-proxy case (§4.3); a
  one-line strip handles the rare exception. Not load-bearing.
