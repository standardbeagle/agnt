# MCP Tools Reference

Full tool catalog, per-tool parameters, and output formats. CLAUDE.md carries
only the summary table + handler pattern; this is the detailed reference.

## Tool Catalog

| Tool | Description |
|------|-------------|
| `detect` | Detect project type (Go/Node/Python) + scripts |
| `run` | Run scripts/commands (background/foreground/foreground-raw) |
| `proc` | Process management (status, output, stop, list, cleanup_port) |
| `proxy` | Reverse proxy (start, stop, status, list, exec) |
| `proxylog` | Query proxy logs (query, clear, stats) |
| `tunnel` | Tunnel management (cloudflare/ngrok) |
| `currentpage` | Page session tracking |
| `get_errors` | Unified error view across processes and proxies (legacy; superseded by `get_incidents`) |
| `get_incidents` | Incident inbox pull — cursor-based, priority-ordered, with remediation hints |
| `responsive_audit` | Responsive design audits across viewport sizes |
| `api_audit` | API efficiency audit (waterfall, N+1, duplicate, chatty-load) over the fetch/XHR buffer |
| `loading_audit` | Loading-UX audit (spinner cascade + concurrent fragmentation) over the spinner timeline |
| `snapshot` | Visual regression testing (baseline/compare screenshots) |
| `daemon` | Daemon management |
| `watch` | Get monitor command for streaming events (errors, interactions, process, all) |
| `channel_reply` | Send messages to developer's browser overlay (channel mode beta) |

**Session scoping & `global` flag**: query/list tools scoped to caller's session project by default (daemon-side session-scope chokepoint — see `.claude/rules/daemon-architecture.md` § Tool session-scoping). Every gated tool (`get_errors`, `proc`, `proxy`, `tunnel`, `session`, `daemon` startup_log) takes same `global: true` input for cross-project results. `get_incidents` (per-session isolated) and `watch` (monitor stream) intentionally omit it.

**Handler pattern**:
- Input/Output structs with JSON schema tags
- Return `(*mcp.CallToolResult, OutputStruct, error)`
- Errors as `CallToolResult{IsError: true}` (NOT Go errors)

## get_incidents Tool

Cursor-based incident inbox pull. When incident pipeline enabled (`alerts.incident-pipeline true`), this = authoritative tool for fetching errors and warnings from all signal sources. Returns incidents in priority order (critical → error → warning → info) with remediation hints and suggested next tools.

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cursor` | string | beginning | Opaque cursor from previous response for incremental pull |
| `limit` | int | 25 | Max incidents returned |
| `severity` | string | `warning` | Minimum severity: `info`, `warning`, `error`, `critical` |
| `source` | string | all | Filter by signal source (e.g. `browser_js`, `http_5xx`) |
| `raw` | bool | false | Return full JSON instead of compact text |

**Compact Output Format**:
```
=== Incidents (3) ===

[critical] process_crash — agnt-dev (2x, latest 3s ago)
  panic: runtime error: index out of range
  → internal/proxy/server.go:142
  remediation: proc {action:"output", process_id:"agnt-dev"} → get_incidents
  next_tools: proc, get_incidents

[error] browser_js — TypeError (1x, 8s ago)
  Cannot read property 'map' of undefined
  → src/components/List.tsx:42:15

[warning] http_4xx — GET /api/old-endpoint (1x, 30s ago)
```

**Key Files**: `internal/incident/get_incidents.go`, `internal/incident/routing.go`

## get_errors Tool (Legacy)

Superseded by `get_incidents` when `alerts.incident-pipeline true`. Kept for backwards compat and daemon-less (legacy) mode.

**Dual Mode**:
- **Daemon mode**: Full — process alerts via daemon IPC + proxy errors
- **Legacy mode** (no daemon): Proxy errors only, process alerts unavailable

**Key Files**: `internal/tools/get_errors.go`, `internal/tools/get_errors_test.go`

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

**Examples**:
```json
responsive_audit {proxy_id: "dev"}
responsive_audit {proxy_id: "dev", checks: ["layout", "overflow"]}
responsive_audit {proxy_id: "dev", viewports: [{name: "xs", width: 320, height: 568}]}
responsive_audit {proxy_id: "dev", raw: true}
```

**Compact Output Format**:
```
=== Responsive Audit: 3 viewports ===

MOBILE (375px) - 2 issues
  ! [layout] .header - collapsed content, element has text but zero height
  o [overflow] .sidebar - truncated text without title/tooltip

TABLET (768px) - 0 issues

DESKTOP (1440px) - 1 issues
  ! [layout] .fixed-nav - fixed element covers 45% of viewport

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

**Output**: audit-module shape (`score`/`grade`/`summary`/`findings`/`findingSelectors`); score = 100 minus weighted findings → grade A–F.

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

**Output**: same audit-module shape; score = 100 minus cascade (depth-weighted, +critical) and fragmentation deductions → grade.

**Key Files**: `internal/proxy/scripts/audit-loading.js` (`window.__devtool_audit_loading.auditLoading`), `internal/proxy/scripts/mutation.js` (spinner recorder), `internal/tools/loading_audit.go`

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
- `log(message, level, data)`, `screenshot(name)`, `isConnected()`
- `interactions.getHistory/getLastClick/getLastClickContext()`
- `mutations.getHistory/highlightRecent()`

**Indicator & Modes**:
- `indicator.show/hide/toggle/togglePanel()`
- `sketch.open/close/toggle/save/toJSON/fromJSON()`
- `design.start/stop/selectElement/next/previous/addAlternative/chat()`
- `responsive.open/close/toggle/setWidth/getState()` — responsive mode (4th indicator mode)

**Diagnostics** (categories):
- Element Inspection (9): getElementInfo, getPosition, getComputed, etc.
- Layout Diagnostics (3): findOverflows, findStackingContexts, findOffscreen
- Accessibility (5): getA11yInfo, auditAccessibility (3 modes), getContrast, etc.
- Quality Auditing (10+): auditDOMComplexity, auditPageQuality, auditCSS, etc.

**Scored audits (8)**: `auditAll` aggregates eight scored audits into a weighted overall grade — DOM, CSS, performance, security, SEO, accessibility, **API efficiency** (`__devtool_audit_api.auditAPIEfficiency`), and **loading/spinner** (`__devtool_audit_loading.auditLoading`). Weights: security 1.5, accessibility 1.3, performance 1.2, api 1.1, loading 1.1, seo 1.0, dom 0.8, css 0.7. Both new audits are guarded — `auditAll` degrades cleanly when a module is absent. The API + loading audits are temporal: they read the fetch/XHR buffer and spinner timeline, so they need a fresh page load to populate.

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
- **Wrap gating**: only genuine top-level navigations are wrapped. A nested
  browsing context (`Sec-Fetch-Dest: iframe/frame/embed/object`) and any request
  carrying the `__devtool_frame` marker are served unwrapped, so an app's own
  iframes are never shell-wrapped.
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
  emitting `frame_id`; `get_errors` dedup and `proxylog`'s `LogFilter.Frames`
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

Cloudflare/ngrok support for mobile testing:
```bash
proxy {action: "start", bind_address: "0.0.0.0", ...}
tunnel {action: "start", provider: "cloudflare", local_port: 12345, proxy_id: "dev"}
```
