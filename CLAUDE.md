# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is **devtool-mcp**, an MCP (Model Context Protocol) server that provides development tooling capabilities to AI assistants. It enables project type detection, script execution, long-running process management with output capture, and reverse proxy with traffic logging and frontend instrumentation.

**MCP Server Name**: `devtool-mcp`
**Version**: 0.4.0
**Protocol**: MCP over stdio
**Language**: Go 1.24.2

**Binaries** (all are copies of the same `agnt` binary):
- `agnt`: Primary CLI tool - the only binary that is actually built
- `devtool-mcp`: Copy of `agnt` for MCP backwards compatibility
- `agnt-daemon`: Copy of `agnt` for daemon auto-start
- `devtool-mcp-daemon`: Copy of `agnt` for daemon auto-start

**Install Strategy**:
Only `agnt` is compiled from `cmd/agnt/`. All other binaries are copies:
```
agnt (built) ──┬── devtool-mcp (copy, for MCP registration)
               ├── agnt-daemon (copy, for daemon auto-start)
               └── devtool-mcp-daemon (copy, for daemon auto-start)
```

**Why binary copies instead of self-exec?**
The daemon auto-start needs to spawn a background process. Some sandboxed environments
(like Claude Code) prevent a binary from fork/exec'ing itself. By having separate
`{binary}-daemon` copies, the auto-start can exec a different file path, bypassing
the fork prevention restriction.

**MCP Registration** (claude_desktop_config.json):
```json
"devtool": {
  "command": "devtool-mcp",
  "args": ["serve"]
}
```

Note: `devtool-mcp` without args also works (auto-detects non-terminal and runs serve),
but explicit `serve` is recommended for clarity.

**Why `agnt run` exists (MCP notification workaround)**:
MCP servers cannot push notifications to clients like Claude Code - they can only
respond to tool calls. The `agnt run` command is a workaround:

1. `agnt run claude` wraps Claude Code (or any AI tool) in a PTY
2. The overlay server (port 19191) receives events from the devtool-mcp proxy
3. Events (like panel messages, sketches) are injected as synthetic stdin to the PTY
4. This makes it appear as if the user typed the message

```
Browser Indicator ──► devtool-mcp Proxy ──► HTTP POST ──► agnt overlay ──► PTY stdin ──► Claude Code
     (click Send)        (WebSocket)         (/event)      (port 19191)     (inject)      (sees input)
```

This allows the floating indicator in the browser to send messages that get typed
into Claude Code as if the user typed them - working around MCP's lack of server push.

**Core Features**:
- Project type detection (Go, Node.js, Python)
- Process management with output capture
- Reverse proxy with HTTP traffic logging
- Frontend error tracking and performance monitoring
- WebSocket-based metrics collection
- Daemon architecture for persistent state
- Floating indicator with panel for sending messages to MCP
- Sketch mode for wireframing (Excalidraw-like)
- Agent overlay for PTY wrapper around AI tools

## Build & Development Commands

```bash
# Build binaries
make build          # Produces ./devtool-mcp binary
make build-agent    # Produces ./agnt binary
make all            # Build both binaries

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
make install        # Install all binaries to $GOPATH/bin
make install-local  # Install all binaries to ~/.local/bin

# Testing a single package
go test -v ./internal/process
go test -v -run TestSpecificFunction ./internal/project

# Testing with race detector
go test -race ./...
```

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
./devtool-mcp              # Normal mode: MCP server with daemon backend
./devtool-mcp daemon       # Daemon mode: Run only the background daemon
./devtool-mcp --legacy     # Legacy mode: Original behavior without daemon
./devtool-mcp --socket /tmp/my-devtool.sock  # Custom socket path
```

**Protocol**: Client-daemon communication uses a simple text-based protocol:
- Commands: `VERB [SUBVERB] [ARGS...] [LENGTH]\r\n[DATA]\r\n`
- Responses: `OK|ERR|JSON|DATA|CHUNK|END [message|length]\r\n[data]\r\n`

## Agent CLI

The `agnt` binary provides a PTY wrapper for running AI coding tools with overlay features. It allows injecting synthetic input from external sources (like devtool proxy events).

**Usage**:
```bash
# Run Claude Code with overlay
agnt run claude --dangerously-skip-permissions

# Run any AI tool
agnt run gemini
agnt run copilot
agnt run opencode

# Run as MCP server (same as devtool-mcp)
agnt serve
agnt serve --legacy  # Legacy mode without daemon

# Manage daemon
agnt daemon status
agnt daemon start
agnt daemon stop
```

**Subcommands**:
- `run <command> [args...]`: Run an AI coding tool with PTY wrapper and overlay
- `serve`: Run as MCP server (equivalent to `devtool-mcp`)
- `daemon`: Manage the background daemon (status, start, stop, restart, info)

**Overlay Features**:
The overlay listens on port 19191 by default for WebSocket connections and HTTP endpoints:
- `/ws`: WebSocket for bidirectional communication
- `/health`: Health check endpoint
- `/type`: POST endpoint to type text into the PTY
- `/key`: POST endpoint to send key events
- `/event`: POST endpoint to receive events from devtool-mcp proxy

**Event Flow**:
```
Browser Indicator → WebSocket → devtool-mcp Proxy → HTTP → Agent Overlay → PTY → AI Tool
```

This allows the floating indicator in the browser to send messages that get typed into Claude Code (or any AI tool) as user input.

### PTY Output Protection

The overlay uses a multi-layer protection system to prevent the child process from corrupting the indicator bar or menu:

**Output Chain**: `PTY → ProtectedWriter (filter) → OutputGate → os.Stdout`

**ProtectedWriter** (`internal/overlay/filter.go`):
- Parses ANSI escape sequences in PTY output stream
- Blocks alternate screen sequences (`\x1b[?1049h`, `\x1b[?47h`, `\x1b[?1047h`) to keep child on main screen
- Enforces scroll region by rewriting `\x1b[r` to `\x1b[1;Nr` (protects bottom row)
- Clamps cursor position moves that target protected bottom row
- Triggers redraw on clear screen (`\x1b[2J`) and terminal reset (`\x1bc`)
- Periodic diff-gated redraw as safety net (200ms interval)

**OutputGate** (`internal/overlay/gate.go`):
- Freeze/unfreeze mechanism for PTY output during menu display
- When frozen (menu open), all PTY output is discarded (not buffered)
- Prevents PTY output from corrupting the alternate screen where menu is drawn
- Overlay calls `gate.Freeze()` on menu open, `gate.Unfreeze()` on menu close

**Key Design Decisions**:
- Filter blocks alt screen instead of tracking it - simpler and keeps scroll region protection active
- Gate discards rather than buffers - avoids memory growth during long menu sessions
- Scroll region is re-enforced after resize and terminal reset events

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

### Reverse Proxy Architecture

**ProxyServer** (`internal/proxy/server.go:25-48`):
- Based on `httputil.ReverseProxy` for efficient proxying
- Injects instrumentation JavaScript into HTML responses
- WebSocket server for receiving frontend metrics
- Lock-free design using `sync.Map` for proxy registry
- Auto-port discovery if requested port is in use
- Auto-restart on crash (max 5 restarts per minute)
- `Ready()` returns a channel that closes when server is ready

**Four-part system**:
1. **HTTP Proxy**: Forwards requests, logs traffic, modifies responses
2. **JavaScript Injection**: Adds error tracking, performance monitoring, and `__devtool` API to HTML pages
3. **WebSocket Server**: Receives metrics from instrumented frontend at `/__devtool_metrics`
4. **JavaScript Execution**: Execute arbitrary JavaScript in connected browsers via `proxy exec`

**Frontend API** (`window.__devtool`):
- `log(message, level, data)`: Send custom log to server
- `debug/info/warn/error(message, data)`: Convenience log methods
- `screenshot(name)`: Capture screenshot and save to temp file
- `isConnected()`: Check WebSocket connection status
- `getStatus()`: Get detailed connection status
- `interactions.getHistory()`, `interactions.getLastClick()`, `interactions.getLastClickContext()`: User interaction tracking
- `mutations.getHistory()`, `mutations.highlightRecent()`: DOM mutation tracking with visual highlighting
- `indicator.show/hide/toggle()`: Floating indicator control
- `indicator.togglePanel()`: Toggle the indicator panel
- `sketch.open/close/toggle()`: Sketch mode (Excalidraw-like wireframing)
- `sketch.save()`: Save and send sketch to MCP
- `sketch.toJSON/fromJSON()`: Serialize/deserialize sketch data
- Plus ~50 diagnostic primitives (see `internal/proxy/scripts/`)

**TrafficLogger** (`internal/proxy/logger.go`):
- Circular buffer storage (default 1000 entries)
- Eleven log entry types: HTTP, Error, Performance, Custom, Screenshot, Execution, Response, Interaction, Mutation, PanelMessage, Sketch
- Thread-safe with `sync.RWMutex` for read-heavy workloads
- Atomic counters for statistics

**JavaScript Injection** (`internal/proxy/injector.go` + `internal/proxy/scripts/`):
1. Detect HTML responses via Content-Type header
2. Decompress response if gzip or deflate encoded
3. Inject `<script>` tag before `</head>` (preferred), with fallbacks
4. Scripts are organized as separate .js modules using `//go:embed`
5. Return uncompressed modified response

**PageTracker** (`internal/proxy/pagetracker.go`):
- Groups HTTP requests by page view for easier debugging
- Associates errors, performance metrics, interactions, and mutations with page sessions
- Lock-free design using `sync.Map`
- Tracks interaction counts (max 200 per session) and mutation counts (max 100 per session)

### Output Capture with RingBuffer

**Problem**: Long-running processes can generate unbounded output.
**Solution**: Fixed-size circular buffer that discards oldest data when full.

**RingBuffer** (`internal/process/ringbuf.go:11-28`):
- Thread-safe via single mutex (only for boundary writes)
- Tracks overflow with `atomic.Bool`
- `Read()` returns consistent snapshot + truncation flag
- Default 256KB per stream (stdout/stderr separate)

### Project Detection System

**Auto-detection hierarchy** (`internal/project/detector.go:59-76`):
1. **Go projects**: Presence of `go.mod` → parses module name
2. **Node projects**: Presence of `package.json` → detects package manager (pnpm > yarn > bun > npm)
3. **Python projects**: Checks `pyproject.toml` → `setup.py` → `setup.cfg` → `requirements.txt`

**Command definitions** (`internal/project/commands.go`):
- Each project type has default commands (test, build, lint, etc.)
- Node.js commands vary by package manager detected from lockfiles

### MCP Tools

**Available Tools** (7 total):

| Tool | Description |
|------|-------------|
| `detect` | Detect project type (Go/Node/Python) and available scripts |
| `run` | Run project scripts or raw commands (background/foreground/foreground-raw modes) |
| `proc` | Manage processes: status, output, stop, list, cleanup_port |
| `proxy` | Reverse proxy: start, stop, status, list, exec (JavaScript execution) |
| `proxylog` | Query proxy logs: query, clear, stats |
| `currentpage` | Page session tracking: list, get, clear |
| `daemon` | Daemon management: status, info, start, stop, restart |

**Tool Registration** (`cmd/devtool-mcp/main.go`):

```go
// Daemon mode (default) - tools communicate via socket to daemon
tools.RegisterDaemonTools(server, dt)      // detect, run, proc, proxy, proxylog, currentpage
tools.RegisterDaemonManagementTool(server, dt)  // daemon

// Legacy mode (--legacy flag) - direct process/proxy management
tools.RegisterProcessTools(server, pm)     // run, proc
tools.RegisterProjectTools(server)         // detect
tools.RegisterProxyTools(server, proxym)   // proxy, proxylog, currentpage
```

**Handler pattern** (`internal/tools/*.go`):
- Input/Output structs with JSON schema tags
- Handlers return `(*mcp.CallToolResult, OutputStruct, error)`
- Errors returned as `CallToolResult{IsError: true}`, not Go errors
- Structured error formatting for LLM-friendly messages (`formatDaemonError()`)

### Log Entry Types

The proxy logger supports eleven log entry types:

| Type | Description | Source |
|------|-------------|--------|
| `http` | HTTP request/response pairs | Automatic from proxy |
| `error` | Frontend JavaScript errors with stack traces | `window.addEventListener('error')` |
| `performance` | Page load and resource timing metrics | Navigation/Paint/Resource Timing APIs |
| `custom` | Custom log messages | `window.__devtool.log()` |
| `screenshot` | Screenshot captures | `window.__devtool.screenshot()` |
| `execution` | JavaScript execution requests | `proxy exec` action |
| `response` | JavaScript execution responses | Browser response to exec |
| `interaction` | User interactions (clicks, keyboard, scroll, etc.) | `window.__devtool_interactions` |
| `mutation` | DOM mutations (added, removed, modified elements) | `window.__devtool_mutations` |
| `panel_message` | Messages from floating indicator panel | Floating indicator "Send" button |
| `sketch` | Sketches/wireframes from sketch mode | Sketch mode "Save & Send" button |

### Directory Filtering

The `proc list` and `proxy list` actions support directory filtering:

- **Default behavior**: Only show processes/proxies started from the current working directory
- **Global flag**: Set `global: true` to show all processes/proxies across all directories
- Each process/proxy stores its `project_path` when created for filtering

### Graceful Shutdown

**Aggressive shutdown for Ctrl+C** (`cmd/devtool-mcp/main.go:58-80`):
1. Signal handler (SIGINT/SIGTERM) triggers shutdown
2. **2-second timeout** for total shutdown
3. ProcessManager detects tight deadline and uses **aggressive mode**
4. In aggressive mode: skips SIGTERM, sends **immediate SIGKILL** to all processes
5. Aggressive shutdown completes in <500ms typically

**Shutdown modes**:
- **Aggressive mode** (deadline <3s): Immediate SIGKILL to all processes
- **Normal mode** (deadline ≥3s): SIGTERM first, then SIGKILL after 5s

**Shutdown safety**:
- `sync.Once` prevents duplicate shutdown in both managers
- `atomic.Bool` shuttingDown prevents new process/proxy registration
- Context cancellation during shutdown triggers immediate force kill

### Frontend Diagnostic Primitives

The `window.__devtool` API includes ~50 diagnostic primitives for DOM inspection, layout analysis, visual debugging, and interactive diagnostics. Implementation in `internal/proxy/scripts/` (organized as separate ES5 modules with `//go:embed`).

**Design Principles**:
1. **Primitives over monoliths**: Small, focused functions (~20-30 lines each)
2. **Composability**: Functions return rich data structures
3. **Error resilient**: Return `{error: ...}` instead of throwing exceptions
4. **ES5 compatible**: Works in all modern browsers

**Categories**:
- Element Inspection (9): getElementInfo, getPosition, getComputed, getBox, getLayout, etc.
- Tree Walking (3): walkChildren, walkParents, findAncestor
- Visual State (3): isVisible, isInViewport, checkOverlap
- Layout Diagnostics (3): findOverflows, findStackingContexts, findOffscreen
- Visual Overlays (3): highlight, removeHighlight, clearAllOverlays
- Interactive (4): selectElement, measureBetween, waitForElement, ask
- State Capture (4): captureDOM, captureStyles, captureState, captureNetwork
- Accessibility (5): getA11yInfo, getContrast, getTabOrder, getScreenReaderText, auditAccessibility
- Quality Auditing (10+): auditDOMComplexity, auditPageQuality, auditCSS, auditSecurity, etc.
- Interaction Tracking: getHistory, getLastClick, getClicksOn, getMouseTrail, getLastClickContext
- Mutation Tracking: getHistory, getAdded, getRemoved, getModified, highlightRecent, pause/resume
- Floating Indicator: show, hide, toggle, togglePanel, destroy
- Sketch Mode: open, close, toggle, save, toJSON, fromJSON, setTool, undo, redo, clear

**Testing**: Use `test-diagnostics.html` as an interactive playground.

### Floating Indicator Bug

A draggable floating indicator that appears on proxied pages by default, providing quick access to DevTool features.

**Features**:
- **Shown by default** - Automatically visible on all proxied pages
- Draggable positioning (remembers position in localStorage)
- Connection status indicator (green/red dot)
- Visibility preference persisted (hide once to keep hidden)
- Expanding panel with:
  - Text input for messages/notes
  - Screenshot area selection
  - Element selection for logging
  - Quick access to sketch mode

**Usage**:
```javascript
__devtool.indicator.show()       // Show the indicator
__devtool.indicator.hide()       // Hide the indicator
__devtool.indicator.toggle()     // Toggle visibility
__devtool.indicator.togglePanel() // Toggle the expanded panel
```

**Panel Message Logging**: Messages sent from the panel are logged as `panel_message` type and can be queried via `proxylog`.

### Sketch Mode (Wireframing)

An Excalidraw-like drawing interface for creating wireframes and annotations directly on top of the UI.

**Features**:
- **Shape Tools**: Rectangle, ellipse, line, arrow, free-draw, text
- **Wireframe Elements**: Button, input field, sticky note, image placeholder (Balsamiq-style)
- **Sketchy Rendering**: Configurable roughness for hand-drawn look
- **Full Editing**: Selection, move, resize, delete, undo/redo
- **JSON Serialization**: Export/import sketches as JSON
- **MCP Integration**: Save sketches to proxy logs with image export

**Tools Available**:
| Tool | Description |
|------|-------------|
| select | Select and move elements |
| rectangle | Draw rectangles |
| ellipse | Draw ellipses/circles |
| line | Draw straight lines |
| arrow | Draw arrows |
| freedraw | Free-hand drawing |
| text | Add text labels |
| note | Sticky note (Balsamiq-style) |
| button | Button wireframe element |
| input | Input field wireframe |
| image | Image placeholder |
| eraser | Delete elements |

**Usage**:
```javascript
__devtool.sketch.open()           // Enter sketch mode
__devtool.sketch.close()          // Exit sketch mode
__devtool.sketch.toggle()         // Toggle sketch mode
__devtool.sketch.setTool('rectangle') // Select a tool
__devtool.sketch.save()           // Save and send to MCP
__devtool.sketch.toJSON()         // Export as JSON
__devtool.sketch.fromJSON(data)   // Import from JSON
__devtool.sketch.undo()           // Undo last action
__devtool.sketch.redo()           // Redo action
__devtool.sketch.clear()          // Clear all elements
```

**Keyboard Shortcuts** (in sketch mode):
- `Escape`: Close sketch mode
- `Delete/Backspace`: Delete selected elements
- `Ctrl+Z`: Undo
- `Ctrl+Shift+Z` or `Ctrl+Y`: Redo
- `Ctrl+A`: Select all
- `Ctrl+C`: Copy selection
- `Ctrl+V`: Paste

**Sketch Logging**: Sketches are logged as `sketch` type with both JSON data and PNG image.

## Testing Strategy

**Test coverage areas**:
- `internal/process/ringbuf_test.go`: RingBuffer thread safety, overflow behavior
- `internal/process/lifecycle_test.go`: Process state transitions, graceful shutdown
- `internal/project/detector_test.go`: Project type detection for all supported types
- `internal/proxy/logger_test.go`: Traffic logger circular buffer, filtering, concurrent writes
- `internal/proxy/injector_test.go`: JavaScript injection strategies, HTML detection
- `internal/overlay/filter_test.go`: ANSI sequence parsing, scroll region enforcement, cursor clamping
- `internal/overlay/gate_test.go`: Freeze/unfreeze behavior, callback invocation, concurrent access

**Integration testing pattern**:
```go
pm := process.NewProcessManager(process.ManagerConfig{
    MaxOutputBuffer:   1024,
    GracefulTimeout:   100 * time.Millisecond,
    HealthCheckPeriod: 0, // Disable for tests
})
```

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
- **Aggressive shutdown**: Immediate SIGKILL when deadline <3s
- **Health checks**: 10s period (detects zombie processes)

### Reverse Proxy

- **Traffic log size**: 1000 entries default (circular buffer)
- **Request/response truncation**: Bodies limited to 10KB in logs
- **WebSocket support**: Transparently proxies WebSocket upgrades
- **WebSocket endpoint**: Reserved path `/__devtool_metrics`
- **JavaScript injection**: Only on `text/html` content type
- **Port auto-discovery**: Auto-assigns port if requested port is in use
- **Auto-restart**: Max 5 restarts per minute to prevent crash loops

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

## Common Development Gotchas

1. **Process ID conflicts**: `Register()` returns `ErrProcessExists` if ID already used.

2. **State validation**: Always check state before operations. Use `CompareAndSwapState()` for atomic transitions.

3. **Output truncation**: RingBuffer has fixed size. Check `truncated` flag when reading output.

4. **Shutdown race**: Don't start new processes during shutdown. Check `pm.IsShuttingDown()` before registration.

5. **Context cancellation**: All process operations respect context. Cancelled context stops processes gracefully.

6. **Project detection order**: Go → Node → Python. If multiple project markers exist, earlier detection wins.

7. **Proxy ID conflicts**: `Create()` returns `ErrProxyExists` if ID already used.

8. **Log buffer overflow**: Circular buffer stores only last N entries. Check `dropped` count in stats.

9. **JavaScript injection failures**: If HTML is malformed, injection may fail silently. Page still loads.

10. **Port auto-discovery**: Always check `listen_addr` in response to get actual port.

11. **Reserved endpoint**: `/__devtool_metrics` is reserved for WebSocket. Backend routes with this path will be shadowed.

## Future Expansion Points

### Process Management
- **Config file support**: KDL-based configuration (`internal/config/kdl.go`)
- **Process labels**: Already supported in `ManagedProcess.Labels`, not exposed to MCP yet
- **Metrics**: Manager tracks totalStarted/totalFailed, could expose via MCP tool

### Reverse Proxy
- **Persistent logs**: Currently in-memory only
- **Request/response filtering**: Block or modify requests based on rules
- **SSL/TLS support**: Currently HTTP only
- **HAR export**: Export traffic logs in HAR format
- **WebSocket logging**: Could log WebSocket frame data
- remember this complicated install story
- remember the complicated way that agnt run is setup to workaround the notification issue and the binary rename is there for the fork prevention issue