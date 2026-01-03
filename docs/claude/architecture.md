# Architecture Deep Dive

Detailed implementation documentation for agnt internals. For overview, see main [CLAUDE.md](../../CLAUDE.md).

## Five-Layer Architecture

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

## Lock-Free Process Management

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

## Process Lifecycle State Machine

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

## Reverse Proxy Architecture

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

**TrafficLogger** (`internal/proxy/logger.go`):
- Circular buffer storage (default 1000 entries)
- Fourteen log entry types (see [reference.md](reference.md#log-entry-types))
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

## Output Capture with RingBuffer

**Problem**: Long-running processes can generate unbounded output.
**Solution**: Fixed-size circular buffer that discards oldest data when full.

**RingBuffer** (`internal/process/ringbuf.go:11-28`):
- Thread-safe via single mutex (only for boundary writes)
- Tracks overflow with `atomic.Bool`
- `Read()` returns consistent snapshot + truncation flag
- Default 256KB per stream (stdout/stderr separate)

## Project Detection System

**Auto-detection hierarchy** (`internal/project/detector.go:59-76`):
1. **Go projects**: Presence of `go.mod` → parses module name
2. **Node projects**: Presence of `package.json` → detects package manager (pnpm > yarn > bun > npm)
3. **Python projects**: Checks `pyproject.toml` → `setup.py` → `setup.cfg` → `requirements.txt`

**Command definitions** (`internal/project/commands.go`):
- Each project type has default commands (test, build, lint, etc.)
- Node.js commands vary by package manager detected from lockfiles

## Graceful Shutdown

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

## PTY Output Protection

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
