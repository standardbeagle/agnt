# Orphan Process Cleanup Implementation

## Overview

This document describes the PID tracking and orphan cleanup system implemented to prevent zombie processes when the daemon crashes or is killed unexpectedly.

## Problem

When the daemon crashes or is killed without graceful shutdown:
1. Child processes (dev servers, test runners, etc.) keep running
2. These orphaned processes block ports (e.g., port 3000)
3. Next daemon startup fails with `EADDRINUSE` errors
4. User must manually kill orphaned processes

## Solution

### 1. PID Tracking (`internal/daemon/pidtracker.go`)

A persistent PID tracker that saves process information to disk:

**File**: `~/.local/state/devtool-mcp/pids.json`

**Structure**:
```json
{
  "daemon_pid": 12345,
  "processes": [
    {
      "id": "dev",
      "pid": 23456,
      "pgid": 23456,
      "project_path": "/home/user/project",
      "started_at": "2025-01-15T10:30:00Z"
    }
  ],
  "updated_at": "2025-01-15T10:30:05Z"
}
```

**Key Features**:
- Atomic file writes (temp file + rename)
- Tracks daemon PID for crash detection
- Stores PGID for process group kills
- Per-project process tracking

### 2. Process Group Management (`internal/process/lifecycle_unix.go`)

**Changed behavior**: All child processes now inherit the daemon's process group:

```go
cmd.SysProcAttr = &syscall.SysProcAttr{
    Setpgid: false, // Inherit parent's PGID (was: true)
}
```

**Before** (separate groups):
```
Daemon (PGID=1000)
├── dev server (PGID=2000) ← NEW group
│   ├── vite (PGID=2000)
│   └── rollup (PGID=2000)
└── test (PGID=3000) ← NEW group
```

**After** (shared group):
```
Daemon (PGID=1000)
├── dev server (PGID=1000) ← SAME group
│   ├── vite (PGID=1000)
│   └── rollup (PGID=1000)
└── test (PGID=1000) ← SAME group
```

**Benefits**:
- Killing daemon's process group kills ALL children
- Simpler lifecycle management
- No orphans on daemon death (Linux)

**Trade-offs**:
- Less isolation between processes
- Signals to daemon affect all children
- All processes in same terminal session

### 3. Integration (`internal/process/manager.go`, `internal/daemon/daemon.go`)

**ProcessManager changes**:
- Added `PIDTracker` interface for dependency injection
- Calls `tracker.Add()` when process starts
- Calls `tracker.Remove()` when process exits
- Best-effort tracking (errors ignored)

**Daemon lifecycle**:

**On startup**:
```go
func (d *Daemon) cleanupOrphans() {
    killedCount, err := d.pidTracker.CleanupOrphans(os.Getpid())
    // Logs: "cleaned up N orphaned process(es) from previous crash"
}
```

**On shutdown** (graceful):
```go
func (d *Daemon) Stop(ctx context.Context) error {
    // ... kill all processes ...
    d.pidTracker.Clear() // Remove tracking file
}
```

## Cleanup Algorithm

```go
func CleanupOrphans(currentDaemonPID int) (killedCount int, err error) {
    tracking := load()

    // Same daemon PID = clean restart, skip cleanup
    if tracking.DaemonPID == currentDaemonPID {
        return 0, nil
    }

    // Different PID = crash recovery
    for _, proc := range tracking.Processes {
        if isProcessAlive(proc.PID) {
            // Kill process group (includes all children)
            syscall.Kill(-proc.PGID, syscall.SIGKILL)
            killedCount++
        }
    }

    // Clear old tracking, set new daemon PID
    tracking.Processes = nil
    tracking.DaemonPID = currentDaemonPID
    save(tracking)

    return killedCount, nil
}
```

## Scenarios

### Scenario 1: Clean Restart
```
1. Daemon starts dev server (PID 23456, tracked)
2. User stops daemon gracefully
3. Daemon kills PID 23456
4. Daemon clears tracking file
5. Daemon restarts
6. Tracking file empty → no cleanup needed
```

### Scenario 2: Crash Recovery
```
1. Daemon starts dev server (PID 23456, tracked)
2. Daemon crashes (SIGKILL, OOM, etc.)
3. Dev server keeps running (orphaned)
4. Daemon restarts (new PID 12999)
5. Reads tracking file: old daemon PID 12345
6. Detects crash (PID mismatch)
7. Checks if PID 23456 alive → YES
8. Kills PID 23456 and process group
9. Updates tracking file with new daemon PID
```

### Scenario 3: Port Conflict
```
1. Daemon crashes, dev server orphaned on port 3000
2. Daemon restarts, cleans up orphan
3. User starts new dev server
4. Success! No port conflict
```

## Testing

See `internal/daemon/pidtracker_test.go` for comprehensive tests:

- ✅ Basic operations (Add, Remove, Load, Clear)
- ✅ Daemon PID tracking
- ✅ Orphan cleanup with same daemon (no-op)
- ✅ Orphan cleanup with different daemon (kills processes)
- ✅ Multiple projects support
- ✅ Persistence across restarts
- ✅ Timestamp tracking

## File Locations

**PID Tracking**:
- Primary: `$XDG_STATE_HOME/devtool-mcp/pids.json`
- Fallback: `~/.local/state/devtool-mcp/pids.json`
- Last resort: `/tmp/devtool-mcp-pids.json`

**State Persistence** (proxies):
- Primary: `$XDG_STATE_HOME/devtool-mcp/state.json`
- Fallback: `~/.local/state/devtool-mcp/state.json`

## Comparison to Windows

**Windows** (already implemented):
- Uses Job Objects with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`
- Automatic child process cleanup on daemon death
- No PID tracking needed

**Linux/macOS** (this implementation):
- Uses process groups + PID tracking
- PID tracking provides crash recovery
- Process groups provide signal propagation

## Future Enhancements

### Linux-Specific: PDEATHSIG
Could use `Pdeathsig: syscall.SIGKILL` to auto-kill children when daemon dies:

```go
//go:build linux

cmd.SysProcAttr = &syscall.SysProcAttr{
    Setpgid: true,                // Separate group (isolation)
    Pdeathsig: syscall.SIGKILL,   // Auto-kill on daemon death
}
```

**Pros**:
- Kernel-enforced cleanup
- No PID tracking needed
- Maintains process isolation

**Cons**:
- Linux-only (not macOS/BSD)
- Requires platform-specific builds

### Session Persistence
Could persist `agnt run` sessions for auto-restart:

**Challenges**:
- Cannot reconnect stdio/PTY
- Overlay state would be lost
- Scheduled messages would fail

**Verdict**: Not practical, PID cleanup is sufficient

## Summary

The PID tracking system provides robust orphan cleanup with minimal overhead:

1. **Tracks PIDs** when processes start
2. **Removes PIDs** when processes exit cleanly
3. **Cleans orphans** on daemon startup after crash
4. **Clears tracking** on graceful shutdown

Combined with shared process groups on Linux, this ensures no orphaned processes block ports or waste resources.
