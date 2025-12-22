---
sidebar_position: 7
---

# daemon

Manage the devtool daemon service that provides persistent state for processes and proxies.

## Overview

The daemon is a background process that maintains state across MCP client connections. This enables:

- **Session handoff**: Multiple MCP clients can interact with the same processes/proxies
- **Persistent state**: Processes and proxies survive client disconnections
- **Fast reconnect**: New sessions reconnect to existing daemon instantly

The daemon auto-starts when needed, so manual management is rarely required.

## Input Schema

```json
{
  "action": "status" | "info" | "start" | "stop" | "restart"
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Action to perform |

## Actions

### status

Check if daemon is running.

```json
daemon {action: "status"}
```

**Response:**
```json
{
  "running": true,
  "socket_path": "/tmp/agnt.sock",
  "message": "Daemon is running"
}
```

### info

Get detailed daemon information including uptime and statistics.

```json
daemon {action: "info"}
```

**Response:**
```json
{
  "running": true,
  "socket_path": "/tmp/agnt.sock",
  "version": "0.1.0",
  "uptime": "2h 15m 30s",
  "client_count": 2,
  "process_info": {
    "active": 3,
    "total_started": 15,
    "total_failed": 2
  },
  "proxy_info": {
    "active": 1,
    "total_started": 3
  }
}
```

### start

Start the daemon (if not already running).

```json
daemon {action: "start"}
```

**Response:**
```json
{
  "running": true,
  "socket_path": "/tmp/agnt.sock",
  "success": true,
  "message": "Daemon started successfully"
}
```

If already running:
```json
{
  "running": true,
  "socket_path": "/tmp/agnt.sock",
  "success": true,
  "message": "Daemon is already running"
}
```

### stop

Stop the daemon gracefully.

```json
daemon {action: "stop"}
```

**Response:**
```json
{
  "running": false,
  "socket_path": "/tmp/agnt.sock",
  "success": true,
  "message": "Daemon stopped successfully"
}
```

:::warning
Stopping the daemon will terminate all running processes and proxies managed by it.
:::

### restart

Restart the daemon (stop then start).

```json
daemon {action: "restart"}
```

**Response:**
```json
{
  "running": true,
  "socket_path": "/tmp/agnt.sock",
  "success": true,
  "message": "Daemon restarted successfully"
}
```

## Architecture

```
┌─────────────────────┐       ┌─────────────────────────────────────┐
│  Claude Code        │       │           agnt               │
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

## Running Modes

The agnt binary supports several modes:

```bash
# Normal mode (default): MCP server with daemon backend
./agnt

# Daemon mode: Run only the background daemon
./agnt daemon

# Legacy mode: Original behavior without daemon
./agnt --legacy

# Custom socket path
./agnt --socket /tmp/my-devtool.sock
```

## Auto-Start Behavior

The daemon auto-starts when:
- Any tool that requires state management is called (`run`, `proc`, `proxy`, etc.)
- No existing daemon is running on the socket path

This means you typically don't need to manually start the daemon - it happens automatically on first tool use.

## Examples

### Check System Health

```json
// Check if daemon is running
daemon {action: "status"}

// Get detailed information
daemon {action: "info"}
```

### Restart After Issues

```json
// If processes are stuck or daemon is unresponsive
daemon {action: "restart"}
```

### Clean Shutdown

```json
// Stop all processes and the daemon
daemon {action: "stop"}
```

## Common Patterns

### Pre-Session Check

Before starting work, verify daemon state:

```json
daemon {action: "info"}
```

This shows:
- How many processes/proxies are running from previous sessions
- Uptime and stability information
- Whether state from previous work is available

### Session Recovery

If an MCP client disconnects, the daemon preserves all state. A new client can:

1. Check what's running: `proc {action: "list", global: true}`
2. Query existing proxies: `proxy {action: "list", global: true}`
3. Continue working with existing processes

### Troubleshooting

If tools are unresponsive:

```json
// Check daemon status
daemon {action: "status"}

// If status fails, restart daemon
daemon {action: "restart"}
```

## Socket Path

The daemon uses a Unix socket (or named pipe on Windows) for communication:

- **Default**: `/tmp/agnt-{uid}.sock` (Unix) or `\\.\pipe\agnt-{user}` (Windows)
- **Custom**: Set via `--socket` flag

Multiple daemons can run on different socket paths for isolation.

## See Also

- [Architecture](/concepts/architecture) - System architecture overview
- [Process Management](/features/process-management) - Managing processes
- [Reverse Proxy](/features/reverse-proxy) - Proxy features
