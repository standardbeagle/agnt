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

**Diagnostics** (categories):
- Element Inspection (9): getElementInfo, getPosition, getComputed, etc.
- Layout Diagnostics (3): findOverflows, findStackingContexts, findOffscreen
- Accessibility (5): getA11yInfo, auditAccessibility (3 modes), getContrast, etc.
- Quality Auditing (10+): auditDOMComplexity, auditPageQuality, auditCSS, etc.

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
