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
- Daemon architecture for persistent state

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
make install-local  # Install to ~/.local/bin

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
- Plus ~50 diagnostic primitives (see `internal/proxy/diagnostics.js`)

**TrafficLogger** (`internal/proxy/logger.go:78-98`):
- Circular buffer storage (default 1000 entries)
- Seven log entry types: HTTP, Error, Performance, Custom, Screenshot, Execution, Response
- Thread-safe with `sync.RWMutex` for read-heavy workloads
- Atomic counters for statistics

**JavaScript Injection** (`internal/proxy/injector.go:82-141`):
1. Detect HTML responses via Content-Type header
2. Decompress response if gzip or deflate encoded
3. Inject `<script>` tag before `</head>` (preferred), with fallbacks
4. Return uncompressed modified response

**PageTracker** (`internal/proxy/pagetracker.go`):
- Groups HTTP requests by page view for easier debugging
- Associates errors and performance metrics with page sessions
- Lock-free design using `sync.Map`

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

The proxy logger supports seven log entry types:

| Type | Description | Source |
|------|-------------|--------|
| `http` | HTTP request/response pairs | Automatic from proxy |
| `error` | Frontend JavaScript errors with stack traces | `window.addEventListener('error')` |
| `performance` | Page load and resource timing metrics | Navigation/Paint/Resource Timing APIs |
| `custom` | Custom log messages | `window.__devtool.log()` |
| `screenshot` | Screenshot captures | `window.__devtool.screenshot()` |
| `execution` | JavaScript execution requests | `proxy exec` action |
| `response` | JavaScript execution responses | Browser response to exec |

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

The `window.__devtool` API includes ~50 diagnostic primitives for DOM inspection, layout analysis, visual debugging, and interactive diagnostics. Implementation in `internal/proxy/diagnostics.js`.

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

**Testing**: Use `test-diagnostics.html` as an interactive playground.

## Testing Strategy

**Test coverage areas**:
- `internal/process/ringbuf_test.go`: RingBuffer thread safety, overflow behavior
- `internal/process/lifecycle_test.go`: Process state transitions, graceful shutdown
- `internal/project/detector_test.go`: Project type detection for all supported types
- `internal/proxy/logger_test.go`: Traffic logger circular buffer, filtering, concurrent writes
- `internal/proxy/injector_test.go`: JavaScript injection strategies, HTML detection

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
