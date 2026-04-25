# Daemon Architecture for devtool-mcp

## Overview

This document describes the migration from an in-process MCP server to a daemon-based architecture that supports persistent state across agent disconnections.

## Current Architecture

```
┌──────────────┐  stdio/MCP   ┌──────────────────────────────────┐
│  AI Agent    │◄────────────►│         MCP Server               │
│ (Claude Code)│              │  ┌────────────────────────────┐  │
└──────────────┘              │  │ ProcessManager (sync.Map)  │  │
                              │  ├────────────────────────────┤  │
                              │  │ ProxyManager (sync.Map)    │  │
                              │  ├────────────────────────────┤  │
                              │  │ TrafficLogger (ring buffer)│  │
                              │  └────────────────────────────┘  │
                              └──────────────────────────────────┘
                                        ▲
                                        │ State lost when
                                        │ connection ends
                                        ▼
```

**Problems:**
- All state lost when agent disconnects
- Can't recover running processes after reconnect
- No way to hand off between agents
- Orphaned processes lose tracking

## Target Architecture

```
┌──────────────┐  stdio/MCP   ┌──────────────┐  socket/text   ┌──────────────┐
│  AI Agent    │◄────────────►│  MCP Client  │◄──────────────►│   Daemon     │
│ (Claude Code)│              │  (thin shim) │                │ (stateful)   │
└──────────────┘              └──────────────┘                └──────────────┘
                                     │                              │
                              Translates MCP to             Owns all state:
                              text protocol              - ProcessManager
                                     │                   - ProxyManager
                              Auto-starts daemon         - TrafficLogs
                              if not running             - PageTracker
```

**Benefits:**
- State persists across agent connections
- Multiple agents can share state
- Debug with netcat: `echo "PROC LIST" | nc -U /tmp/devtool.sock`
- Clean separation of concerns
- Same binary, different modes (subcommand)

## Component Design

### 1. Daemon Process (`devtool-mcp daemon`)

The daemon owns all stateful components:

```go
// internal/daemon/daemon.go
type Daemon struct {
    pm       *process.ProcessManager
    proxym   *proxy.ProxyManager
    listener net.Listener
    sockPath string

    // Connection tracking
    clients  sync.Map // clientID -> *ClientConn

    // Shutdown coordination
    shutdown chan struct{}
    wg       sync.WaitGroup
}
```

**Lifecycle:**
1. Check for existing daemon (try connect to socket)
2. If socket exists but dead → remove stale socket
3. Bind socket with exclusive lock
4. Initialize ProcessManager, ProxyManager
5. Accept client connections
6. Handle graceful shutdown on SIGTERM/SIGINT

**Socket Location:**
- Linux/macOS: `/tmp/devtool-mcp-$UID.sock` (XDG_RUNTIME_DIR intentionally ignored — it's cleaned up by pam_systemd on logout)
- Windows: `\\.\pipe\devtool-mcp-$USERNAME`

### 2. MCP Client (`devtool-mcp` default mode)

Thin translator between MCP and daemon:

```go
// internal/client/client.go
type Client struct {
    conn     net.Conn
    sockPath string

    // Buffer for chunked responses
    reader   *bufio.Reader
    writer   *bufio.Writer
}

func (c *Client) EnsureConnected() error {
    // Try to connect to existing daemon
    if c.tryConnect() {
        return nil
    }

    // Start daemon and retry
    if err := c.startDaemon(); err != nil {
        return err
    }

    // Retry connection with backoff
    return c.connectWithRetry()
}
```

**Auto-start Logic:**
1. Tool handler called (e.g., `run`, `proc`, `proxy`)
2. Client checks if connected
3. If not connected, try to connect to socket
4. If socket doesn't exist, spawn daemon process
5. Wait for socket to become available (with timeout)
6. Connect and proceed with command

### 3. Daemon Management Tool

New MCP tool `daemon` for explicit control:

```
daemon {action: "status"}   → Check if daemon is running
daemon {action: "start"}    → Start daemon explicitly
daemon {action: "stop"}     → Stop daemon gracefully
daemon {action: "restart"}  → Restart daemon
daemon {action: "info"}     → Get daemon info (PID, uptime, socket path)
```

## Text Protocol Specification

### Design Principles

1. **Line-based**: Commands are single lines, responses may be multi-line
2. **Human-readable**: Debug with netcat, logs are inspectable
3. **Simple parsing**: No complex escaping, length-prefix for binary
4. **Stateless commands**: Each command is independent

### Command Format

```
VERB [SUBVERB] [ARGS...]\r\n
```

### Response Format

```
OK [data]\r\n                          # Success with optional inline data
ERR <error_code> <message>\r\n         # Error with code and message
DATA <length>\r\n<binary>\r\n          # Binary data with length prefix
CHUNK <length>\r\n<binary>\r\n         # Streaming chunk
END\r\n                                # End of chunked response
JSON <length>\r\n<json>\r\n            # JSON data with length prefix
```

### Command Reference

#### Process Commands

```
# Run a command
RUN <id> <project_path> <mode> <command> [args...]
→ OK <pid>
→ ERR process_exists <id>

# Run with JSON config (for complex args)
RUN-JSON <length>\r\n{"id":"test","path":".","mode":"background",...}\r\n
→ OK <pid>

# Get process status
PROC STATUS <id>
→ JSON <length>\r\n{"id":"test","state":"running","pid":12345,...}\r\n
→ ERR not_found <id>

# Get process output (chunked for streaming)
PROC OUTPUT <id> [stream=combined] [tail=N] [grep=pattern]
→ CHUNK <length>\r\n<output>\r\n
→ CHUNK <length>\r\n<output>\r\n
→ END\r\n

# Stop a process
PROC STOP <id> [force]
→ OK
→ ERR not_found <id>

# List processes
PROC LIST
→ JSON <length>\r\n[{"id":"test","state":"running",...},...]}\r\n

# Cleanup port
PROC CLEANUP-PORT <port>
→ JSON <length>\r\n{"killed_pids":[1234,5678]}\r\n
```

#### Proxy Commands

```
# Start proxy
PROXY START <id> <target_url> <port> [max_log_size]
→ OK <listen_addr>
→ ERR proxy_exists <id>
→ ERR port_in_use <port>

# Stop proxy
PROXY STOP <id>
→ OK
→ ERR not_found <id>

# Get proxy status
PROXY STATUS <id>
→ JSON <length>\r\n{"id":"dev","running":true,...}\r\n

# List proxies
PROXY LIST
→ JSON <length>\r\n[{"id":"dev","target":"http://...",...}]\r\n

# Execute JavaScript
PROXY EXEC <id> <length>\r\n<code>\r\n
→ JSON <length>\r\n{"success":true,"result":"..."}\r\n
→ ERR timeout <id>
```

#### Log Commands

```
# Query logs (with JSON filter)
PROXYLOG QUERY <proxy_id> <length>\r\n{"types":["http"],"limit":100}\r\n
→ JSON <length>\r\n[...entries...]\r\n

# Get log stats
PROXYLOG STATS <proxy_id>
→ JSON <length>\r\n{"total":1000,"available":850,...}\r\n

# Clear logs
PROXYLOG CLEAR <proxy_id>
→ OK

# Get current page sessions
CURRENTPAGE LIST <proxy_id>
→ JSON <length>\r\n[...sessions...]\r\n

CURRENTPAGE GET <proxy_id> <session_id>
→ JSON <length>\r\n{...session...}\r\n

CURRENTPAGE CLEAR <proxy_id>
→ OK
```

#### Project Detection

```
# Detect project type
DETECT <path>
→ JSON <length>\r\n{"type":"node","scripts":["test","build"]}\r\n
```

#### Daemon Control

```
# Ping (health check)
PING
→ PONG

# Get daemon info
INFO
→ JSON <length>\r\n{"pid":12345,"uptime":"5h32m","version":"0.1.0"}\r\n

# Shutdown daemon
SHUTDOWN
→ OK
```

### Error Codes

| Code | Meaning |
|------|---------|
| `not_found` | Process/proxy/session not found |
| `already_exists` | Process/proxy ID already in use |
| `invalid_state` | Invalid state for operation |
| `shutting_down` | Daemon is shutting down |
| `port_in_use` | Port already in use |
| `invalid_args` | Invalid command arguments |
| `timeout` | Operation timed out |
| `internal` | Internal daemon error |

## Implementation Phases

### Phase 1: Core Daemon Infrastructure
- Socket management (create, bind, cleanup stale)
- Connection handling (accept, read, write)
- Protocol parser (command parsing, response formatting)
- Daemon lifecycle (start, stop, signal handling)

### Phase 2: State Migration
- Move ProcessManager to daemon
- Move ProxyManager to daemon
- Move TrafficLogger to daemon
- Maintain existing interfaces

### Phase 3: MCP Client Shim
- Protocol client implementation
- Auto-start logic
- Connection retry with backoff
- MCP tool handlers that delegate to daemon

### Phase 4: Daemon Management Tool
- `daemon` MCP tool
- Status, start, stop, restart, info actions
- Socket path discovery

### Phase 5: Platform Support
- Unix socket implementation (Linux, macOS)
- Named pipe implementation (Windows)
- Platform-specific socket paths

### Phase 6: Testing & Documentation
- Protocol integration tests
- Multi-client concurrency tests
- Reconnection scenario tests
- Update documentation

## File Structure

```
devtool-mcp/
├── cmd/
│   └── devtool-mcp/
│       └── main.go              # Dispatches to daemon or client mode
├── internal/
│   ├── daemon/
│   │   ├── daemon.go            # Daemon main loop
│   │   ├── handler.go           # Command handlers
│   │   ├── protocol.go          # Protocol parser/formatter
│   │   └── socket.go            # Socket management
│   ├── client/
│   │   ├── client.go            # Protocol client
│   │   ├── autostart.go         # Daemon auto-start logic
│   │   └── retry.go             # Connection retry
│   ├── protocol/
│   │   ├── commands.go          # Command definitions
│   │   ├── responses.go         # Response types
│   │   └── parser.go            # Shared parsing logic
│   ├── process/                 # (unchanged)
│   ├── proxy/                   # (unchanged)
│   └── tools/
│       ├── process.go           # Updated to use client
│       ├── proxy_tools.go       # Updated to use client
│       ├── project.go           # Updated to use client
│       └── daemon.go            # NEW: daemon management tool
└── docs/
    └── daemon-architecture.md   # This document
```

## Backwards Compatibility

The MCP interface remains unchanged. Agents using:
- `run`, `proc`, `proxy`, `proxylog`, `currentpage`, `detect`

Will continue to work identically. The only visible difference:
- State persists across connections
- New `daemon` tool available for explicit control

## Security Considerations

1. **Socket Permissions**: Socket created with mode 0600 (owner only)
2. **Same-User Trust**: No authentication between client and daemon (same UID)
3. **No Network Exposure**: Unix socket only, no TCP listener
4. **Input Validation**: All command arguments validated before processing

## Performance Considerations

1. **IPC Overhead**: ~50-100μs per command round-trip (acceptable)
2. **Chunked Responses**: Large outputs streamed, not buffered entirely
3. **Lock-Free Internals**: ProcessManager/ProxyManager design unchanged
4. **Connection Pooling**: Single persistent connection per client

## Failure Modes

### Daemon Crashes
- Client detects disconnect on next command
- Client attempts to restart daemon
- Running processes continue (daemon doesn't own them, just tracks)
- Proxy servers continue (owned by daemon, will stop)

### Stale Socket
- Client tries connect, fails with ECONNREFUSED
- Client checks if PID file exists
- Client removes stale socket
- Client starts new daemon

### Client Crashes
- No effect on daemon
- Other clients unaffected
- Reconnecting client sees current state

## Future Enhancements (Not in Scope)

- State persistence to disk
- Multi-user support with authentication
- Remote daemon access over TCP
- Process adoption (reconnect to orphaned processes)
