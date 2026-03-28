# CLAUDE.md

Project guidance for Claude Code when working with this repository.

## Project Overview

**agnt** - Browser superpowers for AI coding agents. Bridges AI agents and the browser for real-time debugging, UI wireframing, and visual feedback.

**Key Info**:
- **Version**: 0.12.32
- **Language**: Go 1.24.2
- **Protocol**: MCP over stdio
- **Repository**: https://github.com/standardbeagle/agnt

**Binaries**:
- `agnt`: Primary CLI tool (only binary actually built)
- `agnt-daemon`: Copy for daemon auto-start (workaround for fork prevention in sandboxed environments)
- `devtool-mcp`: Legacy alias (backwards compatibility)

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

**Marketplace (Recommended)**:
```bash
claude mcp add agnt --plugin agnt@agnt-marketplace
```

**Manual**:
```bash
# Install binary
go install github.com/standardbeagle/agnt/cmd/agnt@latest
# or: make install-local

# Register MCP
claude mcp add agnt -s user -- agnt mcp
```

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

## Testing

**Coverage areas**:
- `internal/process/ringbuf_test.go`: Thread safety, overflow
- `internal/process/lifecycle_test.go`: State transitions, shutdown
- `internal/project/detector_test.go`: Project detection
- `internal/proxy/logger_test.go`: Circular buffer, filtering
- `internal/proxy/injector_test.go`: JS injection
- `internal/overlay/filter_test.go`: ANSI parsing, scroll region
- `internal/overlay/gate_test.go`: Freeze/unfreeze

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

### Reverse Proxy
- Default port: Hash-based (stable, 10000-60000 range)
- Traffic log: 1000 entries (circular)
- Request/response: 10KB max in logs
- WebSocket: Reserved `/__devtool_metrics`
- JS injection: Only `text/html` content type
- Auto-restart: Max 5/min

### Platform Support

**Linux/macOS**: `Setpgid: true`, SIGTERM/SIGKILL, `creack/pty`, SIGWINCH resize
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
