# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is **devtool-mcp**, an MCP (Model Context Protocol) server that provides development tooling capabilities to AI assistants. It enables project type detection, script execution, long-running process management with output capture, and reverse proxy with traffic logging and frontend instrumentation.

**MCP Server Name**: `devtool-mcp`
**Version**: 0.1.0
**Protocol**: MCP over stdio
**Language**: Go 1.24.2

**Core Features**:
- Project type detection (Go, Node.js, Python)
- Process management with output capture
- Reverse proxy with HTTP traffic logging
- Frontend error tracking and performance monitoring
- WebSocket-based metrics collection
- **Daemon architecture for persistent state** (new)

## Daemon Architecture

The MCP server uses a daemon-based architecture that separates the MCP protocol handler from state management:

```
┌─────────────────────┐       ┌─────────────────────────────────────┐
│  Claude Code        │       │           devtool-mcp               │
│  (MCP Client)       │◄─────►│                                     │
│                     │ stdio │  ┌────────────────┐                 │
│                     │  MCP  │  │  MCP Server    │                 │
└─────────────────────┘       │  │  (thin client) │                 │
                              │  └───────┬────────┘                 │
                              │          │                          │
                              │          │ socket/pipe              │
                              │          │ (text protocol)          │
                              │          ▼                          │
                              │  ┌────────────────────────────────┐ │
                              │  │           Daemon               │ │
                              │  │  ┌──────────────────────────┐  │ │
                              │  │  │    ProcessManager        │  │ │
                              │  │  │    (processes, output)   │  │ │
                              │  │  └──────────────────────────┘  │ │
                              │  │  ┌──────────────────────────┐  │ │
                              │  │  │    ProxyManager          │  │ │
                              │  │  │    (proxies, logs)       │  │ │
                              │  │  └──────────────────────────┘  │ │
                              │  └────────────────────────────────┘ │
                              └─────────────────────────────────────┘
```

**Key Benefits**:
- **Persistent state**: Processes and proxies survive MCP client disconnections
- **Session handoff**: Multiple MCP clients can interact with the same daemon
- **Fast reconnect**: New sessions reconnect to existing daemon instantly
- **Auto-start**: Daemon starts automatically on first MCP tool call

**Running Modes**:
```bash
# Normal mode (default): MCP server with daemon backend
./devtool-mcp

# Daemon mode: Run only the background daemon
./devtool-mcp daemon

# Legacy mode: Original behavior without daemon
./devtool-mcp --legacy

# Custom socket path
./devtool-mcp --socket /tmp/my-devtool.sock
```

**Protocol**: The client-daemon communication uses a simple text-based protocol similar to memcache:
- Commands: `VERB [SUBVERB] [ARGS...] [LENGTH]\r\n[DATA]\r\n`
- Responses: `OK|ERR|JSON|DATA|CHUNK|END [message|length]\r\n[data]\r\n`

## Build & Development Commands

```bash
# Build the server
make build          # Produces ./devtool-mcp binary

# Run tests
make test           # All tests with verbose output
make test-coverage  # Generate coverage.html report
make bench          # Run benchmarks

# Code quality
make fmt            # Format code
make vet            # Run go vet
make lint           # Run golangci-lint (must be installed)

# Development
make run            # Build and run server on stdio
make install        # Install to $GOPATH/bin

# Testing a single package
go test -v ./internal/process
go test -v -run TestSpecificFunction ./internal/project

# Testing with race detector
go test -race ./...
```

## Architecture Overview

### Five-Layer Architecture

**1. MCP Tools Layer** (`internal/tools/`)
- Exposes MCP tools: `detect`, `run`, `proc`, `proxy`, `proxylog`, `currentpage`, `daemon`
- `daemon_tools.go`: Daemon-aware handlers that communicate via socket protocol
- `daemon_management.go`: Daemon management tool (status, start, stop, restart)
- Handles JSON schema validation and error responses

**2. Daemon Layer** (`internal/daemon/`)
- **Daemon** (`daemon.go`): Background service managing persistent state
- **Connection** (`connection.go`): Client connection handler with command dispatch
- **Handler** (`handler.go`): Command handlers for all tools
- **Client** (`client.go`): Client for communicating with daemon from MCP tools
- **Socket** (`socket.go`, `socket_windows.go`): Platform-specific socket/pipe management
- **AutoStart** (`autostart.go`): Auto-start daemon logic for seamless operation

**3. Protocol Layer** (`internal/protocol/`)
- **Commands** (`commands.go`): Command types and constants for IPC protocol
- **Responses** (`responses.go`): Response types and formatting functions
- **Parser** (`parser.go`): Parser and writer for protocol messages

**4. Business Logic Layer** (`internal/project/`, `internal/process/`, `internal/proxy/`)
- **Project Detection** (`internal/project/`): Multi-language project type detection (Go/Node/Python)
- **Process Management** (`internal/process/`): Lock-free process lifecycle management
- **Reverse Proxy** (`internal/proxy/`): HTTP proxy with traffic logging and frontend instrumentation

**5. Infrastructure Layer** (`internal/process/ringbuf.go`, `internal/config/`)
- **RingBuffer**: Thread-safe circular buffer for bounded output capture (256KB default)
- **Config**: KDL configuration support (future expansion)

### Lock-Free Process Management

**Critical Design**: Uses `sync.Map` and atomics throughout to avoid mutex contention.

**ProcessManager** (`internal/process/manager.go:44-78`):
- `sync.Map` for process registry (lock-free reads/writes)
- `atomic.Int64` for metrics (activeCount, totalStarted, totalFailed)
- `atomic.Bool` for shutdown coordination
- Health check goroutine with configurable period

**ManagedProcess** (`internal/process/process.go:48-97`):
- All state fields use atomics: `atomic.Uint32` for state, `atomic.Int32` for PID/exitCode
- `atomic.Pointer[time.Time]` for timestamps
- Single `sync.Mutex` only in RingBuffer for boundary writes

### Process Lifecycle State Machine

```
StatePending → StateStarting → StateRunning → StateStopping → StateStopped/StateFailed
                     ↓                             ↓
                 StateFailed ←──────────────────────┘
```

**State transitions** (`internal/process/lifecycle.go`):
- `Start()`: Pending → Starting → Running
- `Stop()`: Running → Stopping → Stopped (graceful SIGTERM → SIGKILL after timeout)
- `StopProcess()`: Convenience wrapper for Start+Stop

**Critical invariant**: State transitions are atomic using `CompareAndSwapState()`.

**Child process cleanup** (`internal/process/lifecycle.go:174-190`):
- Uses `Setpgid: true` to create process groups on Linux/macOS
- `signalProcessGroup()` sends signals to entire process group (parent + children)
- Returns errors for failed signal operations (previously silently ignored)
- SIGTERM sent first for graceful shutdown, SIGKILL after timeout
- Ensures child processes are properly cleaned up when parent is stopped

### Reverse Proxy with Frontend Instrumentation

**Architecture**: Transparent reverse proxy that logs all HTTP traffic and injects JavaScript for frontend monitoring.

**ProxyServer** (`internal/proxy/server.go:25-48`):
- Based on `httputil.ReverseProxy` for efficient proxying
- Injects instrumentation JavaScript into HTML responses
- WebSocket server for receiving frontend metrics
- Lock-free design using `sync.Map` for proxy registry
- **Auto-port discovery**: If requested port is in use, automatically finds an available port
- **Auto-restart on crash**: Automatically restarts if HTTP server fails (max 5 restarts per minute)
- Returns actual listening address in response (important when port auto-assigned)

**Three-part system**:
1. **HTTP Proxy**: Forwards requests, logs traffic, modifies responses
2. **JavaScript Injection**: Adds error tracking and performance monitoring to HTML pages
3. **WebSocket Server**: Receives metrics from instrumented frontend

#### Traffic Logging System

**TrafficLogger** (`internal/proxy/logger.go:78-98`):
- Circular buffer storage (default 1000 entries)
- Three log entry types: HTTP, Error, Performance
- Thread-safe with `sync.RWMutex` for read-heavy workloads
- Atomic counters for statistics

**Log entry types**:
- **HTTPLogEntry**: Request/response pairs with headers, body (truncated to 10KB), duration
- **FrontendError**: JavaScript errors with stack traces, source location
- **PerformanceMetric**: Page load times, paint metrics, resource timing

**Query capabilities** (`internal/proxy/logger.go:159-242`):
- Filter by type (http/error/performance)
- Filter by HTTP method, URL pattern, status code
- Time range queries (since/until)
- Limit results

#### JavaScript Injection

**Injection strategy** (`internal/proxy/injector.go:82-141`):
1. Detect HTML responses via Content-Type header
2. **Decompress response** if gzip or deflate encoded (`internal/proxy/server.go:390-407`)
3. Inject `<script>` tag before `</head>` (preferred)
4. Fallback: after `<head>`, after `<body>`, or prepend

**Compression handling** (`internal/proxy/server.go:383-433`):
- Automatically detects and decompresses gzip and deflate responses
- Injects JavaScript into decompressed content
- Returns uncompressed modified response (removes Content-Encoding header)
- Gracefully handles corrupt compressed data by skipping injection

**Injected capabilities**:
- **Error tracking**: `window.addEventListener('error')` and `unhandledrejection`
- **Performance metrics**: Navigation Timing API, Paint Timing API, Resource Timing
- **WebSocket connection**: Auto-reconnect with exponential backoff
- **Zero dependencies**: Pure JavaScript, no external libraries

**WebSocket endpoint**: `/__devtool_metrics` (automatically uses current page's protocol/host/port)

#### Frontend Metrics Collection

**WebSocket message format**:
```json
{
  "type": "error" | "performance",
  "data": { ... },
  "url": "https://example.com/page"
}
```

**Error messages** include:
- Message, source file, line/column numbers
- Error object and stack trace
- Page URL where error occurred

**Performance messages** include:
- Navigation timing (DOM content loaded, load event)
- Paint timing (first paint, first contentful paint)
- Resource timing (up to 50 resources with duration/size)

#### Page Session Tracking

**PageTracker** (`internal/proxy/pagetracker.go`):
- Groups HTTP requests by page view for easier debugging
- Automatically identifies document (HTML) vs resource requests
- Associates errors and performance metrics with page sessions
- Lock-free design using `sync.Map`
- Session timeout: 5 minutes (configurable)
- Max sessions: 100 (configurable)

**Page session includes**:
- Initial HTML document request
- All associated resources (JS, CSS, images, fonts, etc.)
- Frontend errors that occurred on that page
- Performance metrics (page load time, paint timing)
- Automatic matching via Referer header or same-origin heuristics

**Use the `currentpage` tool** to view active page sessions:
```
currentpage {proxy_id: "dev"}  // List all active pages
currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}  // Get details
currentpage {proxy_id: "dev", action: "clear"}  // Clear all sessions
```

#### Proxy Manager

**ProxyManager** (`internal/proxy/manager.go:18-36`):
- Lock-free proxy registry using `sync.Map`
- Atomic counters for active/total proxies
- Graceful shutdown support
- Similar design to ProcessManager

**Proxy lifecycle**:
1. Create proxy with target URL and listen port
2. Start HTTP server and WebSocket handler
3. Proxy forwards traffic (including WebSocket upgrades) and logs entries
4. Stop proxy gracefully on shutdown

**WebSocket Support**:
- Transparently proxies WebSocket upgrade requests to the target server
- Implements `http.Hijacker` interface for protocol switching
- Detects WebSocket upgrades via `Upgrade: websocket` header
- Skips response body capture for WebSocket connections (long-lived)
- Works with frameworks like Next.js HMR (Hot Module Replacement)

### Output Capture with RingBuffer

**Problem**: Long-running processes can generate unbounded output.
**Solution**: Fixed-size circular buffer that discards oldest data when full.

**RingBuffer** (`internal/process/ringbuf.go:11-28`):
- Thread-safe via single mutex (only for boundary writes)
- Tracks overflow with `atomic.Bool`
- `Read()` returns consistent snapshot + truncation flag
- Default 256KB per stream (stdout/stderr separate)

**Usage pattern**:
```go
proc.Stdout()  // Returns ([]byte, truncated bool)
proc.Stderr()  // Returns ([]byte, truncated bool)
proc.CombinedOutput()  // Merges both streams
```

### Project Detection System

**Auto-detection hierarchy** (`internal/project/detector.go:59-76`):
1. **Go projects**: Presence of `go.mod` → parses module name
2. **Node projects**: Presence of `package.json` → detects package manager (pnpm > yarn > bun > npm)
3. **Python projects**: Checks `pyproject.toml` → `setup.py` → `setup.cfg` → `requirements.txt`

**Command definitions** (`internal/project/commands.go`):
- Each project type has default commands (test, build, lint, etc.)
- Node.js commands vary by package manager detected from lockfiles
- Commands include both name and actual executable + args

### MCP Tool Implementation Pattern

**Tool registration** (`cmd/devtool-mcp/main.go:54-56`):
```go
tools.RegisterProcessTools(server, pm)    // Adds run, proc tools
tools.RegisterProjectTools(server)        // Adds detect tool
tools.RegisterProxyTools(server, proxym)  // Adds proxy, proxylog, currentpage tools
```

**Handler pattern** (`internal/tools/*.go`):
- Input/Output structs with JSON schema tags
- Handlers return `(*mcp.CallToolResult, OutputStruct, error)`
- Errors returned as `CallToolResult{IsError: true}`, not Go errors
- Three run modes: `background` (default), `foreground`, `foreground-raw`

### Process Tool Usage

**Run a command**:
```
run {script_name: "test"}  // Background mode, returns process_id
run {script_name: "build", mode: "foreground"}  // Wait for completion
run {raw: true, command: "go", args: ["version"], mode: "foreground-raw"}  // Raw command with output
```

**List running processes**:
```
proc {action: "list"}
→ Shows all managed processes with state, runtime
```

**Check process status**:
```
proc {action: "status", process_id: "test"}
→ Returns state, exit code, runtime
```

**Get process output**:
```
proc {action: "output", process_id: "test"}  // All output (combined stdout/stderr)
proc {action: "output", process_id: "test", stream: "stdout"}  // Only stdout
proc {action: "output", process_id: "test", tail: 20}  // Last 20 lines
proc {action: "output", process_id: "test", grep: "ERROR"}  // Filter by pattern
```

**Stop a process**:
```
proc {action: "stop", process_id: "test"}
proc {action: "stop", process_id: "test", force: true}  // Force kill immediately
```

**Clean up orphaned processes by port** (useful when dev server crashes):
```
proc {action: "cleanup_port", port: 3000}
→ Finds and kills all processes listening on port 3000
→ Returns list of killed PIDs
→ Uses graceful SIGTERM first, then SIGKILL if needed
→ Perfect for clearing ports blocked by crashed OAuth/dev servers
```

### Proxy Tool Usage

**Start a reverse proxy**:
```
proxy {action: "start", id: "dev", target_url: "http://localhost:3000", port: 8080}
→ Tries to listen on :8080, forwards to localhost:3000
→ If port 8080 is busy, auto-assigns available port (e.g., :45123)
→ Returns actual listening address in response
→ Injects instrumentation into HTML responses
→ WebSocket server at actual_port/__devtool_metrics

proxy {action: "start", id: "dev", target_url: "http://localhost:3000", port: 0}
→ Explicitly requests auto-assigned port
→ Always assigns available port, never conflicts
```

**Query proxy logs**:
```
proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"], limit: 50}
proxylog {proxy_id: "dev", types: ["error"]}  // Frontend JavaScript errors
proxylog {proxy_id: "dev", types: ["performance"]}  // Page load metrics
proxylog {proxy_id: "dev", url_pattern: "/api", status_codes: [500, 502]}
proxylog {proxy_id: "dev", since: "5m"}  // Last 5 minutes
```

**View current page sessions**:
```
currentpage {proxy_id: "dev"}  // List all active page sessions
→ Shows which pages are currently loaded, with resource counts and errors
currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}
→ Detailed view: all resources, errors, performance metrics for that page
currentpage {proxy_id: "dev", action: "clear"}
→ Clear all page session tracking
```

**Get proxy status**:
```
proxy {action: "status", id: "dev"}
→ Running status, uptime, total requests, log stats
```

**Stop proxy**:
```
proxy {action: "stop", id: "dev"}
```

**Execute JavaScript in browser**:
```
proxy {action: "exec", id: "dev", code: "document.title"}
→ Executes JavaScript in all connected browser clients
→ Returns result with 30-second timeout
→ Logs execution and response for audit trail
```

### Frontend Diagnostic Primitives API

The proxy automatically injects `window.__devtool` into all HTML pages, providing ~50 primitive functions for comprehensive DOM inspection, layout analysis, visual debugging, and interactive diagnostics.

#### Design Principles

1. **Primitives over monoliths**: Small, focused functions (~20-30 lines each)
2. **Composability**: Functions return rich data structures that other functions consume
3. **Error resilient**: Return `{error: ...}` instead of throwing exceptions
4. **ES5 compatible**: Works in all modern browsers without transpilation

#### Quick Reference by Category

**Element Inspection** (9 functions):
- `getElementInfo(selector)` - Tag, ID, classes, attributes
- `getPosition(selector)` - Rect, viewport position, scroll offsets
- `getComputed(selector, properties)` - Computed styles
- `getBox(selector)` - Margin, border, padding, content box
- `getLayout(selector)` - Display, position, flexbox, grid properties
- `getContainer(selector)` - CSS containment info
- `getStacking(selector)` - Z-index, stacking context
- `getTransform(selector)` - Transform matrix decomposition
- `getOverflow(selector)` - Overflow state and scroll dimensions

**Tree Walking** (3 functions):
- `walkChildren(selector, depth, filter)` - Traverse child elements
- `walkParents(selector)` - Walk up to document root
- `findAncestor(selector, condition)` - Find first matching ancestor

**Visual State** (3 functions):
- `isVisible(selector)` - Visibility with reason (display, opacity, etc.)
- `isInViewport(selector)` - Viewport intersection
- `checkOverlap(selector1, selector2)` - Element overlap detection

**Layout Diagnostics** (3 functions):
- `findOverflows()` - Find all elements with overflow
- `findStackingContexts()` - Locate all stacking contexts
- `findOffscreen()` - Find elements outside viewport

**Visual Overlays** (3 functions):
- `highlight(selector, config)` - Visual highlight with custom colors
- `removeHighlight(highlightId)` - Remove specific highlight
- `clearAllOverlays()` - Clear all visual overlays

**Interactive** (4 functions):
- `selectElement()` - Interactive element picker (returns Promise)
- `measureBetween(sel1, sel2)` - Distance between elements
- `waitForElement(selector, timeout)` - Wait for dynamic elements (returns Promise)
- `ask(question, options)` - Show modal for user input (returns Promise)

**State Capture** (4 functions):
- `captureDOM()` - Full HTML snapshot with hash
- `captureStyles(selector)` - All computed and inline styles
- `captureState(keys)` - localStorage, sessionStorage, cookies
- `captureNetwork()` - Performance API resource entries

**Accessibility** (5 functions):
- `getA11yInfo(selector)` - ARIA attributes, role, tabindex
- `getContrast(selector)` - WCAG contrast ratio with AA/AAA pass/fail
- `getTabOrder(container)` - Document tab order
- `getScreenReaderText(selector)` - Screen reader announcement
- `auditAccessibility()` - Full page a11y scan with score

**Composite Functions** (3 high-level functions):
- `inspect(selector)` - Comprehensive element report (combines 8+ primitives)
- `diagnoseLayout()` - Find all layout issues
- `showLayout(config)` - Visual debugging overlay

#### Common Usage Patterns

**Comprehensive Element Inspection**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.inspect('#my-button')"}
// Returns complete analysis: position, box model, layout, stacking, visibility, etc.
```

**Interactive Element Selection**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.selectElement()"}
// User clicks element in browser, returns selector and element info
// Note: Returns a Promise - result comes when user selects or cancels
```

**Layout Debugging**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.diagnoseLayout()"}
// Finds all overflows, stacking contexts, offscreen elements
```

**Visual Highlight Before Screenshot**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.highlight('#problem-element', {color: 'rgba(255,0,0,0.3)', duration: 0})"}
// Highlight element persistently (duration: 0 means no auto-remove)
proxy {action: "exec", id: "dev", code: "window.__devtool.screenshot('highlighted-issue')"}
// Take screenshot with highlight visible
```

**Accessibility Audit**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.auditAccessibility()"}
// Returns: {errors: [...], warnings: [...], score: 85}
// Checks: missing alt text, unlabeled buttons, missing form labels, contrast issues
```

**Contrast Checking**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.getContrast('.my-button')"}
// Returns: {fg: '#fff', bg: '#2196f3', ratio: 4.52, passes: {AA: true, AAA: false}}
```

**Ask User Questions**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.ask('Which layout looks better?', ['Option A', 'Option B', 'Option C'])"}
// Shows modal, returns Promise with selected option
```

**Tree Analysis**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.walkChildren('.container', 2)"}
// Returns hierarchical structure of children up to depth 2
```

**Measure Distance**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.measureBetween('#header', '#footer')"}
// Returns: {distance: {x, y, diagonal}, direction: 'below'}
```

#### Testing

Use `test-diagnostics.html` for comprehensive testing:
- Serves as interactive playground for all primitives
- Includes test buttons for each category
- Examples of various layout patterns (flex, grid, stacking, overflow)
- Console usage examples

#### Performance Characteristics

- All primitives complete in <10ms on typical pages
- Synchronous operations are instant (getPosition, getBox, etc.)
- Async operations wait for user input (selectElement, ask)
- MutationObserver-based waiting (waitForElement)
- No external dependencies - pure browser APIs

### Graceful Shutdown

**Aggressive shutdown for Ctrl+C** (`cmd/devtool-mcp/main.go:58-80`):
1. Signal handler (SIGINT/SIGTERM) triggers shutdown
2. **2-second timeout** for total shutdown (changed from 30s for fast response)
3. ProcessManager detects tight deadline and uses **aggressive mode**
4. In aggressive mode: skips SIGTERM, sends **immediate SIGKILL** to all processes
5. Aggressive shutdown completes in <500ms typically (<1ms in tests)
6. ProxyManager stops all proxies in parallel

**Shutdown modes**:
- **Aggressive mode** (deadline <3s): Immediate SIGKILL to all processes, no graceful period
- **Normal mode** (deadline ≥3s): SIGTERM first, then SIGKILL after 5s graceful timeout

**Shutdown safety**:
- `sync.Once` prevents duplicate shutdown in both managers
- `atomic.Bool` shuttingDown prevents new process/proxy registration
- Health check goroutine stops on shutdown channel
- All managers support graceful context-aware shutdown
- Context cancellation during shutdown triggers immediate force kill

## Key Implementation Details

### Process Start Flow

1. **State transition**: Pending → Starting (atomic CAS)
2. **Register** process in sync.Map (fails if ID exists)
3. **Build exec.Cmd** with process group (`Setpgid: true`)
4. **Connect streams** to RingBuffers
5. **Start** OS process
6. **Launch monitor** goroutine (waits for completion, updates state)
7. **Transition** to Running state

### Run Tool Execution Modes

**Background mode** (default):
- Returns process_id immediately
- Process runs asynchronously
- Use `proc` tool to monitor status/output

**Foreground mode**:
- Waits for completion
- Returns exit_code, state, runtime
- Output accessible via `proc` tool

**Foreground-raw mode**:
- Waits for completion
- Returns exit_code, state, runtime + full stdout/stderr
- Use for short-lived commands where output is needed immediately

### Output Filtering in Proc Tool

**Supported filters** (`internal/tools/process.go:275-353`):
- `stream`: "stdout", "stderr", "combined" (default)
- `grep`: Regex filter (only matching lines)
- `grep_v`: Invert grep (exclude matching lines)
- `head`: First N lines
- `tail`: Last N lines

**Filter order**: grep → head → tail

## Testing Strategy

**Test coverage areas**:
- `internal/process/ringbuf_test.go`: RingBuffer thread safety, overflow behavior
- `internal/process/lifecycle_test.go`: Process state transitions, graceful shutdown
- `internal/project/detector_test.go`: Project type detection for all supported types
- `internal/proxy/logger_test.go`: Traffic logger circular buffer, filtering, concurrent writes
- `internal/proxy/injector_test.go`: JavaScript injection strategies, HTML detection

**Integration testing pattern**:
```go
// Use real ProcessManager with test config
pm := process.NewProcessManager(process.ManagerConfig{
    MaxOutputBuffer:   1024,
    GracefulTimeout:   100 * time.Millisecond,
    HealthCheckPeriod: 0, // Disable for tests
})
```

**Proxy testing considerations**:
- Logger tests verify circular buffer behavior (1000 entries default)
- Injector tests cover various HTML structures (with/without head/body tags)
- Concurrent write tests validate thread-safety
- No integration tests for full HTTP proxy (would require test server)

## Important Constraints

### MCP Protocol Requirements

- **Tool names**: Must match `^[a-zA-Z0-9_-]{1,128}$`
- **Transport**: Stdio only (logs to stderr)
- **Schema**: All inputs/outputs need JSON schema tags
- **Errors**: Return `CallToolResult{IsError: true}`, NOT Go errors to MCP framework

### Process Management

- **No timeout by default**: `DefaultTimeout: 0` (processes run until completion)
- **Output buffering**: 256KB per stream (stdout/stderr separate)
- **Graceful shutdown**: 5s SIGTERM → SIGKILL (normal mode)
- **Aggressive shutdown**: Immediate SIGKILL when deadline <3s (Ctrl+C mode)
- **Shutdown timeout**: 2s total for Ctrl+C (completes in <500ms typically)
- **Health checks**: 10s period (detects zombie processes)

### Reverse Proxy

- **Traffic log size**: 1000 entries default (circular buffer, oldest discarded)
- **Request/response truncation**: Bodies limited to 10KB in logs
- **WebSocket support**: Transparently proxies WebSocket upgrades (e.g., Next.js HMR)
- **WebSocket endpoint**: Reserved path `/__devtool_metrics` for metrics collection
- **CORS**: WebSocket accepts all origins (development use only)
- **JavaScript injection**: Only on `text/html` content type
- **Port auto-discovery**: If requested port is in use, automatically finds an available port
- **Port 0**: Explicitly request auto-assignment; actual port returned in `listen_addr`
- **Auto-restart**: Enabled by default; max 5 restarts per minute to prevent crash loops
- **Crash detection**: Errors stored in `last_error` field, visible in status

### Platform Support

- **Process groups**: Uses `Setpgid: true` (Linux/macOS, not Windows)
- **Signals**: SIGTERM/SIGKILL for graceful shutdown
- **Context**: All operations respect context cancellation

## Configuration

Currently uses hardcoded defaults in `main.go:31-36`. Future expansion planned with KDL config support (`internal/config/`).

**Default configuration**:
```go
ManagerConfig{
    DefaultTimeout:    0,                    // No timeout
    MaxOutputBuffer:   256 * 1024,          // 256KB
    GracefulTimeout:   5 * time.Second,
    HealthCheckPeriod: 10 * time.Second,
}
```

## Common Gotchas

### Process Management

1. **Process ID conflicts**: `Register()` returns `ErrProcessExists` if ID already used. Generate unique IDs or use auto-generated IDs.

2. **State validation**: Always check state before operations. Use `CompareAndSwapState()` for atomic transitions.

3. **Output truncation**: RingBuffer has fixed size. Check `truncated` flag when reading output. Consider filtering with `head`/`tail`/`grep` to reduce output.

4. **Shutdown race**: Don't start new processes during shutdown. Check `pm.IsShuttingDown()` before registration.

5. **Context cancellation**: All process operations respect context. Cancelled context stops processes gracefully.

6. **Project detection order**: Go → Node → Python. If multiple project markers exist, earlier detection wins.

7. **Port conflicts from orphaned processes**: If a dev server crashes or isn't stopped cleanly, it may leave processes holding onto ports. Use `proc {action: "cleanup_port", port: 3000}` to find and kill orphaned processes before starting new ones. This is especially common with OAuth callback servers. The cleanup tool automatically tries `lsof` first, then falls back to `ss` if `lsof` fails or returns no results (common in WSL2/containerized environments where `lsof` may not have visibility into all processes).

### Reverse Proxy

8. **Proxy ID conflicts**: `Create()` returns `ErrProxyExists` if ID already used. Use unique proxy IDs.

9. **Log buffer overflow**: Circular buffer stores only last N entries (default 1000). High-traffic sites will drop old logs. Check `dropped` count in stats.

10. **Large response bodies**: Response bodies >10KB are truncated in logs. Full response still proxied to client, only log is truncated.

11. **WebSocket reconnection**: Frontend reconnects with exponential backoff (max 5 attempts). If WebSocket fails, errors/metrics won't be reported but proxy still works. WebSocket URL automatically adapts to current page's host/port, so proxy restarts on different ports don't cause stale connections.

12. **JavaScript injection failures**: If HTML is malformed or has unusual structure, injection may fail silently. Instrumentation won't be added but page still loads normally.

13. **Port auto-discovery**: When requested port is in use, proxy automatically finds an available port. Always check `listen_addr` in response to get actual port. Use port 0 to explicitly request auto-assignment.

14. **CORS in production**: WebSocket allows all origins for development. NOT suitable for production use without CORS restrictions.

15. **Reserved endpoint**: `/__devtool_metrics` is reserved for WebSocket. Backend routes with this path will be shadowed by proxy.

## Future Expansion Points

### Process Management
- **Config file support**: KDL-based configuration (`internal/config/kdl.go`)
- **Process labels**: Already supported in `ManagedProcess.Labels`, not exposed to MCP yet
- **Metrics**: Manager tracks totalStarted/totalFailed, could expose via MCP tool
- **Process groups**: Could support stopping all processes with same label

### Reverse Proxy
- **Persistent logs**: Currently in-memory only, could add disk persistence
- **Request/response filtering**: Block or modify requests based on rules
- **SSL/TLS support**: Currently HTTP only, could add HTTPS termination
- **Custom JavaScript**: Allow injecting custom instrumentation beyond errors/performance
- **Request replay**: Save and replay HTTP requests for testing
- **HAR export**: Export traffic logs in HAR (HTTP Archive) format
- **WebSocket logging**: Could log WebSocket frame data (currently only logs upgrade request)
