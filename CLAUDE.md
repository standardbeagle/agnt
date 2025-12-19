# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**agnt** - Give your AI coding agent browser superpowers.

agnt is a new kind of tool designed for the age of AI-assisted development. It acts as a bridge between AI coding agents and the browser, extending what's possible during vibe coding sessions. The tool enables agents to see what users see, receive messages directly from the browser, sketch ideas together, and debug in real-time.

**Primary Binary**: `agnt`
**Version**: 0.7.8
**Protocol**: MCP over stdio
**Language**: Go 1.24.2
**Repository**: https://github.com/standardbeagle/agnt

**Binaries**:
- `agnt`: **Primary CLI tool** - the only binary that is actually built
- `agnt-daemon`: Copy of `agnt` for daemon auto-start (fork prevention workaround)
- `devtool-mcp`: Legacy alias (copy of `agnt` for backwards compatibility)

**Install Strategy**:
Only `agnt` is compiled from `cmd/agnt/`. Other binaries are copies for specific purposes:
```
agnt (built) ──┬── agnt-daemon (copy, for daemon auto-start)
               └── devtool-mcp (copy, legacy backwards compatibility)
```

**Why binary copies instead of self-exec?**
The daemon auto-start needs to spawn a background process. Some sandboxed environments
(like Claude Code) prevent a binary from fork/exec'ing itself. By having separate
`agnt-daemon` copy, the auto-start can exec a different file path, bypassing
the fork prevention restriction.

**MCP Registration** (claude_desktop_config.json):
```json
"agnt": {
  "command": "agnt",
  "args": ["mcp"]
}
```

Note: `agnt mcp` runs as MCP server. The binary auto-detects non-terminal mode.
Note: `agnt serve` runs as a unix socket server..

**Why `agnt run` exists (MCP notification workaround)**:
MCP servers cannot push notifications to clients like Claude Code - they can only
respond to tool calls. The `agnt run` command is a workaround:

1. `agnt run claude` wraps Claude Code (or any AI tool) in a PTY
2. The overlay server (port 19191) receives events from the agnt proxy
3. Events (like panel messages, sketches, design requests) are injected as synthetic stdin to the PTY
4. This makes it appear as if the user typed the message

```
Browser Indicator ──► agnt Proxy ──► HTTP POST ──► agnt overlay ──► PTY stdin ──► Claude Code
     (click Send)      (WebSocket)     (/event)     (port 19191)     (inject)      (sees input)
```

This allows the floating indicator in the browser to send messages that get typed
into Claude Code as if the user typed them - working around MCP's lack of server push.

**Core Features**:
- **Browser Superpowers** - Screenshots, DOM inspection, visual debugging for AI agents
- **Floating Indicator** - Send messages from browser directly to your AI agent
- **Sketch Mode** - Draw wireframes directly on your UI (Excalidraw-like)
- **Design Mode** - AI-assisted UI iteration with live preview of design alternatives
- **Real-Time Error Capture** - JavaScript errors automatically available to agent
- **Extended Thinking Window** - Structured data and consolidated error summaries consume fewer tokens
- **Process Management** - Run and manage dev servers with output capture
- **Reverse Proxy** - HTTP traffic logging and frontend instrumentation
- **Daemon Architecture** - Persistent state survives client disconnections
- **Agent Overlay** - PTY wrapper for AI tools with browser-to-terminal messaging
- **Auto System Prompt** - When running `agnt run claude`, auto-injects context about running services

## Installation

### Claude Code Marketplace (Recommended)

The easiest way to install agnt is through the Claude Code marketplace:

```bash
# Install from marketplace (automatically configures MCP)
claude mcp add agnt --plugin agnt@agnt-marketplace
```

This single command:
1. Downloads the latest agnt binary
2. Registers it as an MCP server
3. Configures slash commands and resources

### Manual MCP Registration

If you prefer to install manually or build from source:

**1. Install the binary:**
```bash
# Option A: Install from Go
go install github.com/standardbeagle/agnt/cmd/agnt@latest

# Option B: Download release binary
curl -fsSL https://github.com/standardbeagle/agnt/releases/latest/download/agnt-linux-amd64 -o ~/.local/bin/agnt
chmod +x ~/.local/bin/agnt

# Option C: Build from source (creates all binary copies)
make install-local
```

**2. Register as MCP server:**
```bash
# Using Claude Code CLI
claude mcp add agnt -s user -- agnt mcp

# Or manually edit ~/.config/claude/claude_desktop_config.json
```

**Manual MCP Configuration** (`claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    }
  }
}
```

### Project Setup (/setup Command)

After installing agnt, use the `/agnt:setup-project` slash command to configure your project for optimal development:

```
/agnt:setup-project
```

This command:
1. **Detects your project type** (Go, Node.js, Python)
2. **Identifies available scripts** (test, build, lint, dev, etc.)
3. **Configures auto-start** for dev servers and proxies
4. **Sets up proxy routing** based on detected ports
5. **Saves configuration** to `.agnt/config.kdl`

**Example setup flow:**
```
> /agnt:setup-project

Detected: Node.js project (pnpm)
Available scripts: dev, build, test, lint

Recommended auto-start configuration:
- Run 'pnpm dev' on project open
- Start proxy for localhost:3000

Save this configuration? [Y/n]
```

**Manual setup alternative:**
```bash
# Create .agnt directory
mkdir -p .agnt

# Configure auto-start (example .agnt/config.kdl)
autostart {
  process "dev" {
    script "dev"
  }
  proxy "dev" {
    target "http://localhost:3000"
  }
}
```

### Verifying Installation

After installation, verify agnt is working:

```bash
# Check binary
agnt --version

# Check daemon
agnt daemon status

# In Claude Code, verify MCP tools are available
# The agent should have access to: detect, run, proc, proxy, proxylog, currentpage, daemon
```

## Build & Development Commands

```bash
# Build binaries
make build          # Produces ./agnt binary (primary)
make all            # Build agnt and create binary copies

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
│  Claude Code        │       │              agnt serve             │
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
                              │  │        agnt daemon             │ │
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
agnt mcp                 # Normal mode: MCP server with daemon backend
agnt daemon start          # Start only the background daemon
agnt serve --legacy        # Legacy mode: Original behavior without daemon
agnt serve --socket /tmp/my-agnt.sock  # Custom socket path
```

**Protocol**: Client-daemon communication uses a simple text-based protocol:
- Commands: `VERB [SUBVERB] [ARGS...] [LENGTH]\r\n[DATA]\r\n`
- Responses: `OK|ERR|JSON|DATA|CHUNK|END [message|length]\r\n[data]\r\n`

## agnt CLI

The `agnt` binary is the primary CLI tool for all agnt functionality.

**Usage**:
```bash
# Run Claude Code with browser superpowers
agnt run claude

# Run any AI tool with overlay
agnt run gemini
agnt run copilot
agnt run opencode

# Run as MCP server (for Claude Desktop / Claude Code integration)
agnt mcp

# Manage the background daemon
agnt daemon status
agnt daemon start
agnt daemon stop
agnt daemon info
```

**Subcommands**:
- `run <command> [args...]`: Run an AI coding tool with PTY wrapper and overlay
- `serve`: Run as shared server to manage processes and proxies
- `mcp`: Run as MCP server for Claude Code / Claude Desktop integration
- `daemon`: Manage the background daemon (status, start, stop, restart, info)

**Overlay Features**:
The overlay listens on port 19191 by default for WebSocket connections and HTTP endpoints:
- `/ws`: WebSocket for bidirectional communication
- `/health`: Health check endpoint
- `/type`: POST endpoint to type text into the PTY
- `/key`: POST endpoint to send key events
- `/event`: POST endpoint to receive events from agnt proxy

**Keyboard Shortcuts**:
- `Ctrl+P`: Toggle the overlay menu (shows actions like quit, toggle indicator)

**Event Flow**:
```
Browser Indicator → WebSocket → agnt Proxy → HTTP → agnt Overlay → PTY → AI Tool
```

This allows the floating indicator in the browser to send messages that get typed into Claude Code (or any AI tool) as user input.

**Overlay Registration**:
When `agnt run` starts, it automatically registers its overlay endpoint with the daemon using the `OVERLAY SET` protocol command. This tells the daemon where to forward proxy events (panel messages, sketches, etc.).

Protocol commands for overlay management:
- `OVERLAY SET <endpoint>`: Set overlay endpoint (e.g., "http://127.0.0.1:19191")
- `OVERLAY GET`: Get current overlay endpoint configuration
- `OVERLAY CLEAR`: Clear/disable overlay endpoint

The daemon forwards events from all proxies to the registered overlay endpoint. When an overlay is registered:
1. All new proxies are automatically configured with the overlay endpoint
2. All existing proxies are updated to use the new endpoint
3. Events (panel_message, sketch) are sent via HTTP POST to `/event`

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

**Available Tools** (8 total):

| Tool | Description |
|------|-------------|
| `detect` | Detect project type (Go/Node/Python) and available scripts |
| `run` | Run project scripts or raw commands (background/foreground/foreground-raw modes) |
| `proc` | Manage processes: status, output, stop, list, cleanup_port |
| `proxy` | Reverse proxy: start, stop, status, list, exec (JavaScript execution) |
| `proxylog` | Query proxy logs: query, clear, stats |
| `tunnel` | Tunnel management: start, stop, status, list (cloudflare/ngrok) |
| `currentpage` | Page session tracking: list, get, clear |
| `daemon` | Daemon management: status, info, start, stop, restart |

**Tool Registration** (`cmd/agnt/main.go`):

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

The proxy logger supports fourteen log entry types:

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
| `design_state` | Element selected for design iteration | Design mode selection |
| `design_request` | Request for new design alternatives | Design mode "Next" or chat |
| `design_chat` | Chat message about current design | Design mode chat input |

### Directory Filtering

The `proc list` and `proxy list` actions support directory filtering:

- **Default behavior**: Only show processes/proxies started from the current working directory
- **Global flag**: Set `global: true` to show all processes/proxies across all directories
- Each process/proxy stores its `project_path` when created for filtering

### Graceful Shutdown

**Aggressive shutdown for Ctrl+C** (`cmd/agnt/main.go`):
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

### Design Mode (AI-Assisted UI Iteration)

An interactive design exploration tool that lets you select any element on the page and generate premium design alternatives with AI assistance.

**How it works**:
1. Click the "Design" button in the floating indicator toolbar (or call `__devtool.design.start()`)
2. Hover over elements to see their selectors - click to select
3. The selected element's context is sent to the AI agent
4. AI generates design alternatives that you can preview and navigate
5. Use chat input to refine designs with natural language

**Features**:
- **Element Selection Overlay** - Visual hover highlighting with selector preview
- **Navigation Controls** - Browse through generated alternatives (Prev/Next)
- **Chat Input** - Describe changes you want in natural language
- **Rich Context Capture** - Sends original HTML, parent context, metadata to AI
- **XPath + CSS Selectors** - Robust element targeting across page changes
- **Live Preview** - See alternatives applied instantly to the page

**Usage**:
```javascript
__devtool.design.start()              // Start design mode (show selection overlay)
__devtool.design.stop()               // Exit design mode
__devtool.design.selectElement(el)    // Programmatically select an element
__devtool.design.next()               // Show next alternative
__devtool.design.previous()           // Show previous alternative
__devtool.design.addAlternative(html) // Add a new design alternative (for AI use)
__devtool.design.chat(message)        // Send refinement request
__devtool.design.getState()           // Get current design state
```

**Keyboard Shortcuts** (in design mode):
- `Escape`: Exit design mode

**Event Types** (forwarded to AI agent via overlay):
| Event | Description |
|-------|-------------|
| `design_state` | Initial state when element is selected (selector, HTML, metadata) |
| `design_request` | Request for new alternatives (includes chat history) |
| `design_chat` | Chat message about current design |

**AI Agent Instructions**: When design events are received, the agent is prompted to act as a "world-class UX designer" and generate 3-5 distinct, premium design alternatives using the `__devtool_design.addAlternative()` API.

### Tunnel Integration (Mobile Testing)

The proxy supports tunnel services for exposing local development servers publicly, enabling testing on real mobile devices.

**Supported Providers**:
- **Cloudflare** (`cloudflared`): Free quick tunnels via trycloudflare.com
- **ngrok** (`ngrok`): Popular tunneling service

**Proxy Configuration for Tunnels**:
```bash
# Start proxy on all interfaces (required for tunnels)
proxy {action: "start", id: "dev", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}

# With explicit public URL (if tunnel URL is known)
proxy {action: "start", id: "dev", target_url: "http://localhost:3000", bind_address: "0.0.0.0", public_url: "https://abc123.trycloudflare.com"}
```

**Automatic Tunnel Management**:
```bash
# Start tunnel with auto-configuration of proxy
tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 12345, proxy_id: "dev"}

# Check tunnel status
tunnel {action: "status", id: "dev"}

# List all tunnels
tunnel {action: "list"}

# Stop tunnel
tunnel {action: "stop", id: "dev"}
```

**How it works**:
1. Start proxy with `bind_address: "0.0.0.0"` to listen on all interfaces
2. Start tunnel pointing to the proxy's listen port
3. If `proxy_id` is specified, tunnel auto-updates the proxy's `public_url`
4. URL rewriting automatically uses the tunnel's HTTPS scheme

**BrowserStack Integration**:

BrowserStack provides their own official MCP server for automated testing on real devices. Use it alongside agnt for comprehensive mobile testing:

1. Install BrowserStack MCP Server: https://github.com/browserstack/mcp-server
2. Configure with your BrowserStack credentials
3. Use the tunnel tool with agnt to expose your dev server
4. Use BrowserStack MCP to run automated tests on the tunneled URL

```json
// claude_desktop_config.json - both MCP servers
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    },
    "browserstack": {
      "command": "npx",
      "args": ["@anthropic-ai/browserstack-mcp"],
      "env": {
        "BROWSERSTACK_USERNAME": "your_username",
        "BROWSERSTACK_ACCESS_KEY": "your_key",
        "BROWSERSTACK_LOCAL": "true"
      }
    }
  }
}
```

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

- **Default port**: Hash-based computation from target URL (stable, in range 10000-60000)
- **Traffic log size**: 1000 entries default (circular buffer)
- **Request/response truncation**: Bodies limited to 10KB in logs
- **WebSocket support**: Transparently proxies WebSocket upgrades
- **WebSocket endpoint**: Reserved path `/__devtool_metrics`
- **JavaScript injection**: Only on `text/html` content type
- **Port auto-discovery**: Auto-assigns port if requested port is in use
- **Auto-restart**: Max 5 restarts per minute to prevent crash loops

### Platform Support

**Linux/macOS**:
- **Process groups**: Uses `Setpgid: true` for child process management
- **Signals**: SIGTERM for graceful shutdown, SIGKILL for force kill
- **PTY**: Uses `github.com/creack/pty` for pseudo-terminal
- **Terminal resize**: SIGWINCH signal handling

**Windows** (Windows 10 1809+):
- **ConPTY**: Uses Windows Pseudo Console for `agnt run` command
- **Job Objects**: Process groups via Job Object API for child process management
- **Graceful termination**: `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT)` for graceful shutdown
- **Force kill**: `TerminateJobObject` kills all processes in the job
- **Terminal resize**: Polling-based resize detection (500ms interval)
- **Named Pipes**: Daemon IPC uses `\\.\pipe\devtool-mcp-<username>`

**Common**:
- **Context**: All operations respect context cancellation
- **Overlay**: Terminal overlay system works on both platforms via ANSI escape sequences

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
- there is a scripts/release.sh to manage all the version numbers