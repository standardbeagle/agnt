---
sidebar_position: 3
---

# proc

Manage running processes: status, output, stop, list, and port cleanup.

## Synopsis

```json
proc {action: "<action>", ...params}
```

## Actions

| Action | Description |
|--------|-------------|
| `list` | List all managed processes |
| `status` | Get status of a specific process |
| `output` | Get process output with filtering |
| `stop` | Stop a running process |
| `cleanup_port` | Kill processes using a specific port |

## list

List all managed processes.

```json
proc {action: "list"}
```

Response:
```json
{
  "processes": [
    {
      "id": "dev",
      "state": "running",
      "runtime": "5m32s",
      "pid": 12345
    },
    {
      "id": "build",
      "state": "stopped",
      "exit_code": 0,
      "runtime": "45s"
    }
  ],
  "active_count": 1,
  "total_count": 2
}
```

## status

Get detailed status of a specific process.

```json
proc {action: "status", process_id: "dev"}
```

Parameters:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `process_id` | string | Yes | Process ID |

Response:
```json
{
  "id": "dev",
  "state": "running",
  "pid": 12345,
  "runtime": "5m32s",
  "started_at": "2024-01-15T10:30:00Z"
}
```

## output

Retrieve process output with optional filtering.

```json
proc {action: "output", process_id: "dev"}
```

Parameters:
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `process_id` | string | Yes | - | Process ID |
| `stream` | string | No | `combined` | `stdout`, `stderr`, or `combined` |
| `tail` | integer | No | - | Last N lines |
| `head` | integer | No | - | First N lines |
| `grep` | string | No | - | Filter lines matching regex |
| `grep_v` | boolean | No | false | Invert grep (exclude matches) |

### Examples

```json
// All output (combined stdout/stderr)
proc {action: "output", process_id: "dev"}

// Only stdout
proc {action: "output", process_id: "build", stream: "stdout"}

// Last 20 lines
proc {action: "output", process_id: "dev", tail: 20}

// First 50 lines
proc {action: "output", process_id: "build", head: 50}

// Lines containing "ERROR"
proc {action: "output", process_id: "test", grep: "ERROR"}

// Lines NOT containing "debug"
proc {action: "output", process_id: "dev", grep: "debug", grep_v: true}

// Combined: last 10 error lines
proc {action: "output", process_id: "test", grep: "FAIL", tail: 10}
```

Response:
```json
{
  "output": "...",
  "lines": 150,
  "truncated": false
}
```

Filter order: grep → head → tail

## stop

Stop a running process.

```json
proc {action: "stop", process_id: "dev"}
```

Parameters:
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `process_id` | string | Yes | - | Process ID |
| `force` | boolean | No | false | Force kill (skip SIGTERM grace period) |

### Examples

```json
// Graceful stop (SIGTERM, then SIGKILL after 5s)
proc {action: "stop", process_id: "dev"}

// Force kill immediately
proc {action: "stop", process_id: "dev", force: true}
```

Response:
```json
{
  "id": "dev",
  "state": "stopped",
  "exit_code": 0,
  "message": "Process stopped gracefully"
}
```

## cleanup_port

Kill all processes listening on a specific port.

```json
proc {action: "cleanup_port", port: 3000}
```

Parameters:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `port` | integer | Yes | Port number to clean up |

Response:
```json
{
  "port": 3000,
  "killed_pids": [12345, 12346],
  "message": "Killed 2 processes on port 3000"
}
```

### Use Cases

- Dev server crashed without cleanup
- OAuth callback server left running
- Multiple dev sessions on same machine
- Port conflicts on restart

## Process States

| State | Description |
|-------|-------------|
| `pending` | Created but not started |
| `starting` | Process spawn in progress |
| `running` | Actively executing |
| `stopping` | Graceful shutdown in progress |
| `stopped` | Completed (exit code 0) |
| `failed` | Crashed or non-zero exit |

## Output Truncation

- Each stream (stdout/stderr) has 256KB buffer
- When buffer fills, oldest output is discarded
- `truncated: true` indicates data loss
- Use `grep`/`tail` for large outputs

## Error Responses

### Process Not Found

```json
{
  "error": "process not found",
  "process_id": "nonexistent"
}
```

### Invalid Action

```json
{
  "error": "unknown action",
  "action": "invalid"
}
```

### Stop Timeout

```json
{
  "error": "timeout waiting for process to stop",
  "process_id": "stubborn"
}
```

## Real-World Patterns

### Development Workflow

```json
// Start dev server
run {script_name: "dev", id: "app"}

// Check it started
proc {action: "status", process_id: "app"}

// Monitor for errors
proc {action: "output", process_id: "app", grep: "error", tail: 10}

// When done
proc {action: "stop", process_id: "app"}
```

### Debugging Test Failures

```json
// Run tests
run {script_name: "test", mode: "foreground"}
→ {exit_code: 1}

// Find failures
proc {action: "output", process_id: "test", grep: "FAIL"}

// Get context
proc {action: "output", process_id: "test", grep: "UserService", tail: 30}
```

### Port Conflict Resolution

```json
// Try to start dev server
run {script_name: "dev"}
→ {error: "EADDRINUSE: port 3000 already in use"}

// Clean up the port
proc {action: "cleanup_port", port: 3000}

// Retry
run {script_name: "dev"}
→ {state: "running"}
```

## See Also

- [run](/api/run) - Start processes
- [Process Management Feature](/features/process-management)
