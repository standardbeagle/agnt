# CLAUDE.md

Project guidance for Claude Code when working with this repository.

## Project Overview

**agnt** - Browser superpowers for AI coding agents. Bridges AI agents and the browser for real-time debugging, UI wireframing, and visual feedback.

**Key Info**:
- **Version**: 0.12.51
- **Language**: Go 1.24.2
- **Protocol**: MCP over stdio
- **Repository**: https://github.com/standardbeagle/agnt

**Binaries**:
- `agnt`: Primary CLI tool (only binary actually built)
- `agnt-daemon`: Copy for daemon auto-start (workaround for fork prevention in sandboxed environments)
- `devtool-mcp`: Legacy alias (backwards compatibility)

**CLI Subcommands**: `mcp` (MCP server), `run` (PTY wrapper), `monitor` (event stream), `ai` (interactive AI), `setup-project`

**Core Architecture Decisions**:

1. **Binary copies instead of self-exec**: Sandboxed environments (like Claude Code) prevent binaries from fork/exec'ing themselves. Using separate binary copies bypasses this restriction.

2. **`agnt run` workaround for MCP notifications**: MCP servers can't push notifications to clients. `agnt run` wraps AI tools in a PTY and injects browser events as synthetic stdin:
   ```
   Browser → Proxy → HTTP POST → Overlay (port 19191) → PTY stdin → AI Tool
   ```

3. **System prompt injection**: Auto-injects agnt context when starting AI agents:
   - Claude Code: Uses `--append-system-prompt` flag
   - Others (Gemini, Copilot, Aider, etc.): Sends initial stdin message after 500ms delay

**Core Features**:
- Browser debugging (screenshots, DOM inspection, error capture)
- Floating indicator for browser-to-agent messaging
- Sketch mode (Excalidraw-like wireframing)
- Design mode (AI-assisted UI iteration)
- Process/proxy management with daemon persistence
- PTY overlay for terminal integration

## Installation

```bash
# Install binary
go install github.com/standardbeagle/agnt/cmd/agnt@latest
# or: make install-local

# Register MCP
claude mcp add agnt -s user -- agnt mcp
```

The Claude Code plugin has been moved to a standalone marketplace repo — this
repository ships the `agnt` binary + MCP server only.

**MCP Config** (`claude_desktop_config.json`):
```json
"agnt": {"command": "agnt", "args": ["mcp"]}
```

**Project Setup**:
```bash
/agnt:setup-project  # Auto-detects project, configures auto-start
```

## Build Commands

```bash
make build          # Build agnt binary
make all            # Build + create binary copies
make test           # All tests
make test-coverage  # Generate coverage.html
make install-local  # Install to ~/.local/bin

# Single package tests
go test -v ./internal/process
go test -race ./...
```

## Architecture

### Five-Layer Design

1. **MCP Tools** (`internal/tools/`): Expose daemon-aware MCP tools
2. **Daemon** (`internal/daemon/`): Background service, persistent state, socket IPC
3. **Protocol** (`internal/protocol/`): Text-based IPC protocol (commands/responses)
4. **Business Logic** (`internal/project/`, `internal/process/`, `internal/proxy/`): Project detection, process management, reverse proxy
5. **Infrastructure** (`internal/process/ringbuf.go`, `internal/config/`): RingBuffer, config

### Critical Design: Lock-Free Process Management

**ProcessManager**:
- `sync.Map` for process registry (lock-free)
- `atomic.Int64` for metrics (activeCount, totalStarted, totalFailed)
- `atomic.Bool` for shutdown coordination

**ManagedProcess**:
- All state fields use atomics: `atomic.Uint32` (state), `atomic.Int32` (PID/exitCode), `atomic.Pointer[time.Time]` (timestamps)
- Single `sync.Mutex` only in RingBuffer for boundary writes

### Process Lifecycle State Machine

```
Pending → Starting → Running → Stopping → Stopped/Failed
              ↓                     ↓
          Failed ←──────────────────┘
```

**State transitions**: Atomic via `CompareAndSwapState()`
**Child cleanup**: Process groups (`Setpgid: true`) + `signalProcessGroup()` for parent + children

### Reverse Proxy Architecture

**ProxyServer** (`internal/proxy/server.go`):
- `httputil.ReverseProxy` base
- JavaScript injection into HTML responses
- WebSocket server for frontend metrics (`/__devtool_metrics`)
- Lock-free `sync.Map` registry
- Auto-port discovery, auto-restart (max 5/min)

**Four-part system**:
1. HTTP proxy (forwards, logs, modifies)
2. JS injection (error tracking, `__devtool` API)
3. WebSocket server (receives metrics)
4. JS execution (`proxy exec` for browser control)

**TrafficLogger** (`internal/proxy/logger.go`):
- Circular buffer (1000 entries default)
- 14 log types: HTTP, Error, Performance, Custom, Screenshot, Execution, Response, Interaction, Mutation, PanelMessage, Sketch, DesignState, DesignRequest, DesignChat
- Thread-safe `sync.RWMutex`
- `onLogEntry` callback fires after each logged entry (feeds StreamEvents hub)

### StreamEvents Hub

**AlertHub** (`internal/daemon/alert_hub.go`):
Three delivery sinks for alert/event routing:
- **OverlayAlertSink**: PTY stdin injection for terminal overlay
- **MCPAlertSink**: MCP session `Log()` notifications
- **StreamSink**: Channel-based streaming with filtering (for `agnt monitor`)

**Push channel config** (`alerts.push` in `.agnt.kdl`):
- Controls which delivery channels are active per session
- Presets: `claude-code` (MCP only), `universal` (all channels)
- Default (no config): all channels enabled

**STREAM-EVENTS** (`internal/daemon/hub_stream.go`):
- Daemon-side handler registers a `StreamSink` with type/proxy/process/severity/grep filters
- 30s keepalive heartbeat, chunked JSON output
- `BroadcastLogEntry()` and `BroadcastProcessOutput()` push filtered events to all matching sinks

### Startup Splash

**StartupSplash** (`internal/overlay/splash.go`):
- Displays rotating tip text between PTY start and first child output
- Auto-expires after 30s, clears instantly on `OnFirstActivity` callback from ActivityMonitor
- Message rotation on 2.5s timer
- Writes above the protected status bar row, uses cursor save/restore to avoid disturbing child cursor

### Animated Status Indicator

**Renderer** (`internal/overlay/render.go`):
- `animFrame` atomic counter incremented each `DrawIndicator` call
- `processStateIcon` cycles through pulse frames (filled/empty circle variants) for processes in "starting" or "restarting" state
- Frame-based animation distinguishes active startup from static states

### PTY Output Protection

**Output Chain**: `PTY → ProtectedWriter → OutputGate → os.Stdout`

**ProtectedWriter** (`internal/overlay/filter.go`):
- Parses ANSI sequences
- Blocks alt screen (`\x1b[?1049h`)
- Enforces scroll region (`\x1b[r` → `\x1b[1;Nr`)
- Clamps cursor moves to protected bottom row

**OutputGate** (`internal/overlay/gate.go`):
- Freeze/unfreeze for menu display
- Discards output when frozen (not buffered)

## MCP Tools

| Tool | Description |
|------|-------------|
| `detect` | Detect project type (Go/Node/Python) + scripts |
| `run` | Run scripts/commands (background/foreground/foreground-raw) |
| `proc` | Process management (status, output, stop, list, cleanup_port) |
| `proxy` | Reverse proxy (start, stop, status, list, exec) |
| `proxylog` | Query proxy logs (query, clear, stats) |
| `tunnel` | Tunnel management (cloudflare/ngrok) |
| `currentpage` | Page session tracking |
| `get_errors` | Unified error view across processes and proxies |
| `responsive_audit` | Responsive design audits across viewport sizes |
| `snapshot` | Visual regression testing (baseline/compare screenshots) |
| `daemon` | Daemon management |
| `watch` | Get monitor command for streaming events (errors, interactions, process, all) |
| `channel_reply` | Send messages to developer's browser overlay (channel mode beta) |

**Handler pattern**:
- Input/Output structs with JSON schema tags
- Return `(*mcp.CallToolResult, OutputStruct, error)`
- Errors as `CallToolResult{IsError: true}` (NOT Go errors)

### get_errors Tool

Unified error aggregation across all active processes and proxies. Collects, deduplicates, and formats errors from multiple sources into a single view.

**Error Sources**:
- **Process output** (daemon mode only): Compile errors, panics, exceptions detected by AlertScanner pattern matching
- **Browser JS errors**: Runtime exceptions captured by injected JS (`window.onerror`)
- **HTTP errors**: 4xx/5xx responses from proxied requests (4xx = warning, 5xx = error)
- **Proxy diagnostics**: Transport errors, connection failures
- **Custom logs**: Application-level `__devtool.log()` calls with error/warn level

**Parameters**:
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `process_id` | string | all | Filter to specific process |
| `proxy_id` | string | all active | Filter to specific proxy |
| `since` | string | none | Recency filter (RFC3339 or duration like `5m`, `1h`, `30s`) |
| `include_warnings` | bool | true | Include warnings alongside errors |
| `limit` | int | 25 | Max errors returned |
| `raw` | bool | false | Return full JSON instead of compact text |

**Built-in Intelligence**:
- Deduplicates identical errors by source+category+message+location (shows count)
- Reduces stack traces to first application code frame (skips node_modules, runtime, webpack)
- Filters noise: static asset 404s, redirects (301/302/304), HMR/WebSocket 404s
- Extracts error messages from JSON/HTML response bodies
- Sorts by severity (errors first) then recency

**Compact Output Format**:
```
=== Errors (2) ===

[browser:js] TypeError (3x, latest 5s ago)
  Cannot read property 'map' of undefined
  → src/components/List.tsx:42:15
  page: http://localhost:3000/dashboard

[proxy:http] 500 Internal Server Error (1x, 12s ago)
  POST /api/users → "database connection timeout"

=== Warnings (1) ===

[proxy:http] 404 Not Found (1x, 30s ago)
  GET /api/old-endpoint
```

**Dual Mode**:
- **Daemon mode**: Full functionality — process alerts via daemon IPC + proxy errors
- **Legacy mode** (no daemon): Proxy errors only, process alerts unavailable

**Key Files**: `internal/tools/get_errors.go`, `internal/tools/get_errors_test.go`

### responsive_audit Tool

Run responsive design audits across multiple viewport sizes. Detects layout issues, content overflows, and viewport-specific accessibility problems by loading the page in hidden iframes at target sizes.

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
- `mobileOnly`: Issues appearing only on mobile viewport
- `tabletOnly`: Issues appearing only on tablet viewport
- `crossViewport`: Issues appearing across all viewports

**Key Files**: `internal/tools/responsive_audit.go`, `internal/tools/responsive_audit_test.go`, `internal/proxy/scripts/responsive.js`

### watch Tool

Returns a shell command string for streaming daemon events via the `agnt monitor` CLI. Bridges MCP clients (which know the daemon socket path) to the Monitor tool.

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

**Output**: Returns a `command` string (e.g., `agnt monitor --socket /run/user/... --types error,diagnostic --format compact`) and a human-readable `description`.

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

## Key Features

### Floating Indicator
Draggable indicator (default visible) with:
- Connection status, position persistence
- Text input for messages, screenshot/element selection
- Quick access to sketch/design modes

### Sketch Mode
Excalidraw-like wireframing:
- Shape tools: rectangle, ellipse, line, arrow, freedraw, text
- Wireframe elements: button, input, note, image placeholder
- Full editing: select, move, resize, delete, undo/redo
- JSON export/import, MCP integration

**Shortcuts**: `Escape` (close), `Delete` (erase), `Ctrl+Z` (undo), `Ctrl+Shift+Z` (redo)

### Design Mode
AI-assisted UI iteration:
1. Select element (hover + click)
2. Context sent to AI agent
3. AI generates alternatives (3-5 designs)
4. Navigate alternatives, refine via chat

**Event types**: `design_state`, `design_request`, `design_chat`

### Tunnel Integration
Cloudflare/ngrok support for mobile testing:
```bash
proxy {action: "start", bind_address: "0.0.0.0", ...}
tunnel {action: "start", provider: "cloudflare", local_port: 12345, proxy_id: "dev"}
```

### Event Streaming (`agnt monitor`)

CLI subcommand that streams daemon events to stdout in real time:
```bash
agnt monitor                           # All events
agnt monitor --types error,diagnostic  # Errors only
agnt monitor --proxy dev --format json # NDJSON for specific proxy
agnt monitor --process app             # Process output follow mode
```

Flags: `--types`, `--proxy`, `--process`, `--severity`, `--format` (compact/json), `--socket`
Auto-reconnects on daemon restart. Clean exit on SIGINT/SIGTERM.

### Hook Dispatcher (`agnt hook`)

CLI subcommand that pushes Claude Code (or any agent) hook events into the
daemon's lock-free ring buffer for fan-out. The dispatcher is a
fire-and-forget alias designed to be installed in `~/.claude/settings.json`
hook entries: it must never break the agent loop, must complete in
single-digit milliseconds even when the daemon is wedged, and must always
exit 0 on any transient failure (daemon down, deadline exceeded, payload
errors). The only non-zero exit is `--message`/arg validation.

**Cost contract**: p99 cold exit ≤5ms, measured ~270µs in
`cmd/agnt/hook_bench_test.go`. The dispatcher uses a 50ms hard deadline on
the daemon round-trip, opens a dedicated short-lived client (no shared
state), and writes the entire enqueue path on the daemon side as a single
mutex push into a 1024-slot ring buffer.

**Supported events** (Claude Code hook nomenclature):

| Event | When fired | Drain-side behavior |
|-------|-----------|---------------------|
| `pre-tool-use` | Before each tool call | StreamSink + heartbeat |
| `post-tool-use` | After each tool call | StreamSink + heartbeat |
| `notification` | On `notify`-style messages | StreamSink + heartbeat + per-proxy `BroadcastToast` (payload type/title/message) |
| `stop` | When the agent finishes responding | StreamSink + heartbeat + per-proxy `BroadcastToast` (`success`/"Claude Finished"/`last_assistant_message`, suppressed when `stop_hook_active=true`) |
| `stop-failure` | When the turn ends due to an API error (Claude Code's `StopFailure` event) | StreamSink + heartbeat + per-proxy `BroadcastToast` (`error`/"Claude Error"/`error` + `error_details`) |
| `subagent-stop` | When a subagent stops | StreamSink + heartbeat |
| `user-prompt-submit` | On user prompt submission | StreamSink + heartbeat |
| `session-start` | Session start | StreamSink + heartbeat |
| `session-end` | Session end | StreamSink + heartbeat |
| `pre-compact` | Before context compaction | StreamSink + heartbeat |

Any other event name is enqueued and fanned out the same way; the table
above is the canonical Claude Code set, not a whitelist. Custom event
names work transparently.

**Drain fan-out** (in cheapest-first order — see `drainHooks` →
`fanOutHookEvent` in `internal/daemon/hub_hook.go`):

1. Session heartbeat (in-memory `LastSeen` bump on the SessionRegistry —
   hook traffic counts as proof-of-life for the parent `agnt run` session)
2. StreamSink fan-out as a synthetic `LogEntry{Type: hook}` so
   `agnt monitor --types hook` streams events live
3. If event is `notification`, decode payload as
   `{type, title, message, duration}` and call `BroadcastToast` on every
   active proxy (back-compat for the legacy `agnt notify` path)
4. Typed `HookEventSink` fan-out via `BroadcastHookEvent` for direct
   subscribers (overlay panel, future MCP push)

The drain goroutine never blocks on a slow consumer: `BroadcastLogEntry`
uses channel-send-with-default, `BroadcastToast` errors are swallowed
per-proxy, and any malformed payload short-circuits at the decode step.
If a consumer stalls hard enough to wedge fan-out, ring buffer
overflow kicks in and `hookRing.OverflowCount()` surfaces the pressure.

**Sample `~/.claude/settings.json`**:
```json
{
  "hooks": {
    "preToolUse": [
      { "type": "command", "command": "agnt hook pre-tool-use --session-id $CLAUDE_SESSION_ID --project-path $PWD" }
    ],
    "postToolUse": [
      { "type": "command", "command": "agnt hook post-tool-use --session-id $CLAUDE_SESSION_ID --project-path $PWD" }
    ],
    "notification": [
      { "type": "command", "command": "agnt hook notification --session-id $CLAUDE_SESSION_ID" }
    ],
    "stop": [
      { "type": "command", "command": "agnt hook stop --session-id $CLAUDE_SESSION_ID" }
    ]
  }
}
```

**Streaming hook events live**:
```bash
agnt monitor --types hook                # All hook events, compact
agnt monitor --types hook --format json  # NDJSON for jq pipelines
```
The `--severity` filter is a no-op for hook events; type filter is the
active discriminator. Provenance hints (session ID, agent name, project
path) are included in the `Location` field of the JSON output so jq
pipelines can correlate events without unwrapping the payload.

**`agnt notify` compatibility**: `agnt notify --message "hi"` is preserved
as a thin alias for `agnt hook notification`. It marshals
`{type, title, message}` and calls `HookSend("notification", ...)`. The
daemon-side drain handles the per-proxy `BroadcastToast` loop, so the
browser surface is identical to the legacy implementation. The
client-side per-proxy iteration that used to live in `cmd/agnt/notify.go`
was removed in phase 3.

## Configuration

**Hardcoded defaults** (`main.go:31-36`):
```go
ManagerConfig{
    DefaultTimeout:    0,                    // No timeout
    MaxOutputBuffer:   256 * 1024,          // 256KB
    GracefulTimeout:   5 * time.Second,
    HealthCheckPeriod: 10 * time.Second,
}
```

**Dev Server URL Tracking** (`internal/daemon/urltracker.go`):
- Scans first 8KB of output
- Stores max 5 URLs per process
- Only localhost-like URLs with ports

**Port Conflict Pre-flight** (`internal/daemon/port_preflight.go`, `daemon.go:RunAutostart`):

Before launching autostart scripts, the daemon scans all declared `ports` from autostart scripts for unmanaged processes. Configurable via `port-conflict` in the `project` node of `.agnt.kdl`:

```kdl
project {
    port-conflict "prompt"   // prompt (default) | auto-kill | skip | fail
}
```

| Policy | Behavior |
|--------|----------|
| `prompt` | Return conflicts to client, wait for `AUTOSTART CLEAR-PORTS` or `AUTOSTART CONTINUE` IPC |
| `auto-kill` | Kill blocking process trees automatically, log what was killed |
| `skip` | Log warning, start scripts anyway (they'll fail on bind) |
| `fail` | Abort autostart entirely |

Kill uses `ProcessManager.KillProcessByPort()` with process-group SIGTERM → 3s wait → SIGKILL escalation + descendant tree walk. `AutostartResult` extended with `PortConflicts` and `PortsCleared` fields.

**Autostart Cleanup Ordering** (`internal/daemon/daemon_autostart.go`):
1. **Duplicate scan** (sync, before autostart) — kills orphaned dev server processes using `collectManagedPIDs()` PPID chain walking to protect managed children
2. **Stale process cleanup** (sync, before autostart) — stops processes from previous sessions
3. **Starting** — launches autostart scripts in dependency order
4. **Started** — confirms all scripts launched

**Key files**: `port_preflight.go` (detect + kill), `daemon.go` (RunAutostart integration + pendingAutostarts), `hub_handlers.go` (AUTOSTART verb), `client.go` (AutostartClearPorts/Continue), `pty_common.go` (client prompt)

**Alert Push Channels** (`internal/config/agnt.go`, `alerts.push` in `.agnt.kdl`):

Controls which delivery channels push alerts to the AI client:

```kdl
alerts {
    push {
        mcp-notifications true   // MCP session.Log() notifications
        pty-injection false      // PTY stdin injection
    }
    // Or use a preset:
    preset "claude-code"   // MCP only, no PTY injection
}
```

| Preset | MCP Notifications | PTY Injection |
|--------|------------------|---------------|
| `claude-code` | enabled | disabled |
| `universal` | enabled | enabled |
| (none) | enabled | enabled |

### Channel Mode (Beta — Claude Code only)

> **Beta / Experimental**: Channel mode uses `github.com/standardbeagle/go-sdk` (a fork of `modelcontextprotocol/go-sdk` that adds `ServerSession.Notify`) and the `--dangerously-load-development-channels` flag in Claude Code. The protocol, schema, and tool shapes may change before stabilization.

Push-based event forwarding via the MCP `claude/channel` protocol. When enabled, the daemon streams browser errors, diagnostics, and user interactions directly into Claude's context as `<channel>` events -- no PTY wrapper or `agnt run` required.

**When to use channel mode vs `agnt run`**:

| | Channel mode | `agnt run` |
|--|-------------|------------|
| Works with | Claude Code v2.1.80+ | Any terminal agent |
| Event delivery | Push (real-time XML tags in context) | Pull (poll `get_errors`, `proxylog`) or PTY stdin injection |
| Setup | Add `channel { enabled true }` to `.agnt.kdl` | Wrap agent: `agnt run claude` |
| Browser overlay | Yes (via `channel_reply` tool) | Yes (via PTY indicator) |
| Login requirement | claude.ai account (Console/API key not supported) | None |

**Enabling**:

1. Add a `channel` block to `.agnt.kdl`:

```kdl
channel {
    enabled true              // required to activate
    events "error" "diagnostic" "interaction"  // allowlist; omit for all types
    severity "warning"        // minimum severity to forward
    dedupe-window 2000        // per-event deduplication window (ms)
    reply-tool true           // register channel_reply MCP tool
}
```

| Field | KDL key | Type | Default | Description |
|-------|---------|------|---------|-------------|
| Enabled | `enabled` | bool | `false` | Activate channel event forwarding |
| Events | `events` | string list | (all) | Allowlist of event types: `error`, `diagnostic`, `interaction`, `http`, `custom`, `panel_message` |
| Severity | `severity` | string | `"warning"` | Minimum severity: `trace`, `debug`, `info`, `warning`, `error` |
| DedupeWindow | `dedupe-window` | int | `2000` | Per-event dedup window in ms; `0` disables |
| ReplyTool | `reply-tool` | bool | `true` | Register the `channel_reply` MCP tool |

2. During the research preview, Claude Code must be launched with the development-channels flag:

```bash
claude --dangerously-load-development-channels server:agnt
```

The normal MCP entry (`claude mcp add agnt -s user -- agnt mcp`) is unchanged. The `--dangerously-load-development-channels` flag tells Claude Code to process the `claude/channel` capability and render `<channel>` events in context.

**Event shape**:

Events arrive as XML-like tags injected into Claude's context:

```xml
<channel source="agnt" type="error" proxy="dev" severity="error">
TypeError: Cannot read property 'map' of undefined
  at ProductList (src/components/List.tsx:42:15)
</channel>
```

| Meta key | Description |
|----------|-------------|
| `source` | Always `"agnt"` |
| `type` | Event type: `error`, `diagnostic`, `interaction`, `process`, `panel_message`, `sketch`, `design` |
| `proxy` | Agnt proxy ID (stable per dev server) |
| `severity` | `trace`, `debug`, `info`, `warning`, `error` |

**`channel_reply` tool**:

When `reply-tool` is enabled (default), a `channel_reply` MCP tool is registered for sending messages from Claude to the developer's browser overlay:

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `content` | string | yes | Message body (markdown OK) |
| `title` | string | no | Toast title |
| `severity` | string | no | Toast style: `info` (default), `warning`, `error` |
| `proxy_id` | string | no | Target a specific proxy; omit to fan out to all active proxies |

Returns `{ "delivered": N, "message": "..." }` with count of proxies that received the toast.

```json
channel_reply {content: "Build succeeded, opening preview..."}
channel_reply {content: "Which layout?", title: "Choose", severity: "warning", proxy_id: "dev"}
```

**Forked go-sdk**:

Channel mode uses `ServerSession.Notify(ctx, method, params)` from `github.com/standardbeagle/go-sdk` (a fork of `modelcontextprotocol/go-sdk`). The fork adds this method which is pending upstream PR #898. When upstream merges and releases, swap imports back to `modelcontextprotocol/go-sdk` and bump the version.

## Testing

**Two-tier test suite**:

| Target | Command | Scope | Where to run |
|--------|---------|-------|--------------|
| Default | `make test` | Everything except `procisolation`-tagged files | Any platform. Safe on any host. |
| Isolated | `make test-isolated` | Only `procisolation`-tagged files, inside `unshare --user --pid --mount --fork --mount-proc` | Linux with user+pid namespaces (Ubuntu/Debian/Fedora/Arch/WSL2). Skips loud on other hosts. |

The isolated target exists because a subset of tests exercise host-global primitives — real `/proc` walks and real `kill(2)` syscalls against pgids whose leader is dead — which can reap unrelated processes owned by the same uid when run natively. Running them inside a PID namespace gives `/proc` a private view and makes host pids unreachable, so the tests exercise the real code without risk.

Files tagged `procisolation`:
- `internal/daemon/daemon_orphan_pgid_test.go` — calls `startupOrphanPGIDScan` directly
- `internal/platform/orphanpgid_unix_test.go` — calls `ScanOrphanedPGIDs` + `KillSessionPGID` directly

All other daemon tests run natively under `make test`. They call `daemon.Start()` dozens of times, but the scan inside `Start()` is gated by `DaemonConfig.OrphanScanEnabled`, which defaults to `false` (zero value). Any test using a literal `DaemonConfig{}` gets the safe default automatically — no explicit opt-out required. Production explicitly sets `OrphanScanEnabled: true` in `cmd/agnt/daemon.go`. This field is an internal test-safety knob — it must never be documented as a user-facing config knob or exposed in `.agnt.kdl`. The isolated target's procisolation tests set `d.config.OrphanScanEnabled = true` on the returned daemon so the scan actually runs under their namespace.

The field replaces the legacy `AGNT_DISABLE_ORPHAN_SCAN` env var fence; the env var has been deleted from the runtime code.

**Coverage areas**:
- `internal/process/ringbuf_test.go`: Thread safety, overflow
- `internal/process/lifecycle_test.go`: State transitions, shutdown
- `internal/project/detector_test.go`: Project detection
- `internal/proxy/logger_test.go`: Circular buffer, filtering
- `internal/proxy/injector_test.go`: JS injection
- `internal/overlay/filter_test.go`: ANSI parsing, scroll region
- `internal/overlay/gate_test.go`: Freeze/unfreeze
- `internal/tools/watch_test.go`: Watch command builder
- `cmd/agnt/monitor_test.go`: Monitor event formatting (compact + JSON)
- `internal/daemon/daemon_orphan_pgid_test.go`: Orphan pgid scan (procisolation)
- `internal/platform/orphanpgid_unix_test.go`: Orphan pgid primitives (procisolation)

**Pre-commit hook** (`.git/hooks/pre-commit`):
- Runs `gofmt`, `go vet ./...`, then `go test -count=1 -race -p 1` on staged packages
- **Adaptive flake detection**: if the first race pass completes in <10s, runs 2 more passes (`-count=2`) — total 3 runs catches transient PID-reuse races, timing flakes, scheduler jitter
- Slow packages (`internal/daemon` at ~90s) get only the single race pass; 3× would be ~270s
- Tests that start real OS processes (`sleep`, `echo`, agnt binary) must NOT use `t.Parallel()` — Go's `exec.CommandContext` PID-reuse race under high concurrency kills unrelated processes. Comment `// No t.Parallel(): starts real sleep process; PID-reuse kills it under high concurrency.` documents this pattern.

## Important Constraints

### MCP Protocol
- Tool names: `^[a-zA-Z0-9_-]{1,128}$`
- Transport: stdio only (logs to stderr)
- Schema: All I/O needs JSON schema tags
- Errors: `CallToolResult{IsError: true}` (NOT Go errors)

### Process Management
- No timeout by default (`DefaultTimeout: 0`)
- Output buffering: 256KB per stream
- Graceful shutdown: 5s SIGTERM → SIGKILL (normal)
- Aggressive shutdown: Immediate SIGKILL when deadline <3s
- Health checks: 10s period
- **Session pgid containment**: The PTY child's pgid is the session container. `CleanupSessionResources` kills the entire pgid (SIGTERM, 2s grace, SIGKILL) before touching managed processes — catches `npm run dev &` and other non-interactive-bash backgrounded jobs. Startup scans `/proc` for orphaned pgids from daemon crashes. Accepted escape hatches: `setsid &`, double-fork daemons, `systemd-run`, container runtimes. Full invariant and file ownership: `.claude/rules/daemon-architecture.md` § Session Containment.

### Reverse Proxy
- Default port: Hash-based (stable, 10000-60000 range)
- Traffic log: 1000 entries (circular)
- Request/response: 10KB max in logs
- WebSocket: Reserved `/__devtool_metrics`
- JS injection: Only `text/html` content type
- Auto-restart: Max 5/min

### Platform Support

**Linux/macOS**: `Setpgid: true`, SIGTERM/SIGKILL, `creack/pty`, SIGWINCH resize, PPID chain walking via `/proc/<pid>/stat` for descendant process tracking
**Windows**: ConPTY, Job Objects, `CTRL_BREAK_EVENT`, named pipes (`\\.\pipe\devtool-mcp-<username>`)
**Common**: Context cancellation respected, ANSI escape sequences for overlay

## Graceful Shutdown

**Aggressive mode** (Ctrl+C):
1. 2s timeout
2. ProcessManager detects tight deadline
3. Immediate SIGKILL to all processes
4. <500ms typical completion

**Modes**:
- Aggressive (deadline <3s): Immediate SIGKILL
- Normal (deadline ≥3s): SIGTERM first, SIGKILL after 5s

**Safety**: `sync.Once` (no duplicate), `atomic.Bool` (no new registrations), context cancellation → force kill

## Common Gotchas

1. **Process ID conflicts**: `Register()` → `ErrProcessExists`
2. **State validation**: Use `CompareAndSwapState()` for atomic transitions
3. **Output truncation**: Check `truncated` flag in RingBuffer
4. **Shutdown race**: Check `pm.IsShuttingDown()` before registration
5. **Context cancellation**: All ops respect context
6. **Project detection order**: Go → Node → Python (first match wins)
7. **Proxy ID conflicts**: `Create()` → `ErrProxyExists`
8. **Log buffer overflow**: Check `dropped` count in stats
9. **JS injection failures**: Silent fail if HTML malformed
10. **Port auto-discovery**: Check `listen_addr` in response
11. **Reserved endpoint**: `/__devtool_metrics` shadows backend routes

## Directory Filtering

`proc list` and `proxy list`:
- Default: Current directory only
- Global: Set `global: true` for all directories

## Dev Notes

- **Version management**: `scripts/release.sh` updates all version numbers
- **Binary copies**: Workaround for fork prevention in sandboxed environments
- **agnt run setup**: Complex PTY wrapper to overcome MCP notification limitations
- **Future**: KDL config support (`internal/config/`), persistent logs, HAR export, SSL/TLS, process labels

## Forked Dependencies

- **`github.com/standardbeagle/go-sdk`** (v1.5.0-agnt.2): Fork of `modelcontextprotocol/go-sdk` that adds `ServerSession.Notify(ctx, method, params)` for custom notification methods. Used directly (no `replace` directive) so `go install` works. When upstream merges PR #898, swap imports back and bump version.
