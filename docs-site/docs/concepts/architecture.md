---
sidebar_position: 1
---

# Architecture

agnt follows a clean three-layer architecture designed for performance, reliability, and extensibility.

## System Overview

```
┌─────────────────────────────────────────────────────────────┐
│                     AI Assistant                             │
│                   (Claude, Cursor)                           │
└─────────────────────────────────────────────────────────────┘
                              │
                         MCP Protocol
                           (stdio)
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                      agnt                             │
│  ┌─────────────────────────────────────────────────────────┐│
│  │                    MCP Tools Layer                       ││
│  │  detect │ run │ proc │ proxy │ proxylog │ currentpage   ││
│  └─────────────────────────────────────────────────────────┘│
│  ┌─────────────────────────────────────────────────────────┐│
│  │                  Business Logic Layer                    ││
│  │   ProjectDetector │ ProcessManager │ ProxyManager        ││
│  └─────────────────────────────────────────────────────────┘│
│  ┌─────────────────────────────────────────────────────────┐│
│  │                   Infrastructure Layer                   ││
│  │          RingBuffer │ TrafficLogger │ PageTracker        ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
              ▼               ▼               ▼
         OS Processes    HTTP Traffic    Dev Server
```

## Layer Responsibilities

### MCP Tools Layer

Location: `internal/tools/`

Handles:
- MCP protocol communication
- JSON schema validation
- Input/output serialization
- Error formatting for MCP clients

Each tool is a thin wrapper that:
1. Validates input against JSON schema
2. Delegates to business logic
3. Formats response for MCP protocol

```go
// Example: detect tool handler
func handleDetect(input DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
    // Delegate to ProjectDetector
    info, err := detector.Detect(input.Path)

    // Return MCP-formatted result
    return formatResult(info, err)
}
```

### Business Logic Layer

Location: `internal/project/`, `internal/process/`, `internal/proxy/`

Contains the core functionality:

| Package | Responsibility |
|---------|---------------|
| `project` | Project type detection, command mapping |
| `process` | Process lifecycle, output capture, graceful shutdown |
| `proxy` | HTTP proxying, traffic logging, JS injection |

This layer is independent of MCP - it can be used programmatically.

### Infrastructure Layer

Location: `internal/process/ringbuf.go`, `internal/proxy/logger.go`

Provides low-level utilities:

- **RingBuffer** - Thread-safe circular buffer for output capture
- **TrafficLogger** - Circular buffer for HTTP traffic
- **PageTracker** - Groups requests by page session

## Key Design Decisions

### Lock-Free Concurrency

agnt uses lock-free data structures wherever possible:

```go
// ProcessManager uses sync.Map and atomics
type ProcessManager struct {
    processes    sync.Map           // Lock-free process registry
    activeCount  atomic.Int64       // Atomic counter
    shuttingDown atomic.Bool        // Atomic shutdown flag
}
```

Benefits:
- No mutex contention under load
- Excellent concurrent read performance
- Simple reasoning about correctness

See [Lock-Free Design](/concepts/lock-free-design) for details.

### Bounded Memory

All buffers have fixed sizes:

| Buffer | Default Size | Purpose |
|--------|-------------|---------|
| Process output | 256KB per stream | Prevent memory exhaustion |
| Traffic log | 1000 entries | Bound log storage |
| Page sessions | 100 sessions | Limit active tracking |

When buffers fill, oldest data is discarded. This ensures predictable memory usage regardless of process output or traffic volume.

### Graceful Degradation

The system handles failures gracefully:

- **Process crashes**: Detected by monitor goroutine, state updated
- **Proxy crashes**: Auto-restart (max 5/minute)
- **WebSocket disconnect**: Frontend auto-reconnects with backoff
- **JS injection failure**: Page loads normally without instrumentation

### Stdio Transport

MCP communication uses stdio (stdin/stdout):

```
AI Assistant ←──stdin/stdout──→ agnt
                                     │
                                     └──→ stderr (logs)
```

This is the MCP standard for local tools. All logging goes to stderr to avoid corrupting the MCP protocol stream.

## Component Interactions

### Process Lifecycle

```
run {script_name: "dev"}
         │
         ▼
    ProcessManager
         │
    ┌────┴────┐
    │ Register │ (sync.Map)
    └────┬────┘
         │
    ┌────┴────┐
    │  Start  │ (exec.Command)
    └────┬────┘
         │
    ┌────┴────┐
    │ Monitor │ (goroutine)
    └────┬────┘
         │
         ▼
    State: Running
         │
         ▼
    (process completes)
         │
         ▼
    State: Stopped
```

### Proxy Traffic Flow

```
Browser Request
      │
      ▼
┌─────────────┐
│   Proxy     │──────────────────────┐
│  (port 8080)│                      │
└─────┬───────┘                      │
      │                              ▼
      │ (forward)             TrafficLogger
      │                       (capture req/res)
      ▼
┌─────────────┐
│ Dev Server  │
│ (port 3000) │
└─────┬───────┘
      │
      │ (response)
      ▼
┌─────────────┐
│  Injector   │ (add JS to HTML)
└─────┬───────┘
      │
      ▼
    Browser
      │
      │ (WebSocket)
      ▼
┌─────────────┐
│ Metrics WS  │ (errors, perf)
└─────────────┘
```

### Page Session Tracking

```
HTML Request ───────────────────────► Create Session
                                            │
Resource Request (JS, CSS, img) ────────────┤
                                            │
WebSocket: Error ───────────────────────────┤
                                            │
WebSocket: Performance ─────────────────────┤
                                            │
                                            ▼
                                      Page Session
                                    (grouped view)
```

## Extension Points

### Adding New Project Types

1. Add detection logic to `internal/project/detector.go`
2. Add default commands to `internal/project/commands.go`
3. Update `detect` tool to handle new type

### Adding New MCP Tools

1. Create handler in `internal/tools/`
2. Define input/output structs with JSON schema tags
3. Register in `cmd/agnt/main.go`

### Adding New Diagnostics

1. Add function to injected JavaScript (`internal/proxy/injector.go`)
2. Implement in frontend API style (return `{error: ...}` on failure)
3. Document in frontend API reference

## Configuration

Currently hardcoded in `cmd/agnt/main.go`:

```go
ManagerConfig{
    DefaultTimeout:    0,                  // No timeout
    MaxOutputBuffer:   256 * 1024,        // 256KB
    GracefulTimeout:   5 * time.Second,   // SIGTERM grace
    HealthCheckPeriod: 10 * time.Second,  // Zombie detection
}
```

Future: KDL-based configuration file support.

## Error Handling

MCP tools return errors in the response, not as Go errors:

```go
// Bad: returns Go error
return nil, output, fmt.Errorf("process not found")

// Good: returns MCP error result
return &mcp.CallToolResult{IsError: true}, output, nil
```

This allows the AI assistant to understand and handle errors gracefully.

## Next Steps

- Learn about [Lock-Free Design](/concepts/lock-free-design)
- Understand [Graceful Shutdown](/concepts/graceful-shutdown)
