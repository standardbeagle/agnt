# Doctor Diagnostics

## Problem

When processes fail, ports are orphaned, or the daemon is unhealthy, there's no single place to diagnose what's wrong. Users have to manually check ports, process lists, and daemon status separately.

## Design

### Two-layer doctor

**Layer 1: OS-level (works without daemon)**
- Read PID tracker file from disk
- Cross-reference with live OS process table
- Find orphaned processes (tracked but no longer managed)
- Find port conflicts (declared ports held by unmanaged processes)
- Check daemon process health (is it running, responsive)

**Layer 2: Daemon-level (richer diagnostics when connected)**
- Script health: failed scripts, crash-looping, missing auto-restart
- Proxy health: each proxy reachable, target responding
- Session health: stale sessions, heartbeat timeouts
- Config health: .agnt.kdl parse errors, missing cwd directories
- Port conflicts: declared ports vs actual listeners
- Startup log: recent errors and warnings

### Enhanced PID tracking (go-cli-server)

Extend `TrackedProcess` with descendant PIDs:
```go
type TrackedProcess struct {
    ID             string    `json:"id"`
    PID            int       `json:"pid"`
    PGID           int       `json:"pgid"`
    ProjectPath    string    `json:"project_path"`
    StartedAt      time.Time `json:"started_at"`
    DescendantPIDs []int     `json:"descendants,omitempty"`
    LastScanAt     time.Time `json:"last_scan_at,omitempty"`
}
```

Background goroutine scans `/proc/<pid>/children` every 5s, updates `DescendantPIDs`. On process stop, the full tree is killed. Persisted to disk for crash recovery.

### DOCTOR protocol verb

```
DOCTOR [-- {directory: "..."}]
```

Returns structured report:
```json
{
  "status": "warning",
  "checks": [
    {
      "name": "orphan_processes",
      "status": "warning",
      "message": "2 orphaned processes found on port 5000",
      "details": [{"pid": 12345, "port": 5000, "command": "dotnet"}],
      "fix": "proc {action: \"cleanup_port\", port: 5000}"
    },
    {
      "name": "daemon_health",
      "status": "ok",
      "message": "daemon v0.12.20, uptime 5m, 3 clients"
    }
  ]
}
```

### Surfaces

- **Overlay**: "d" shortcut from overview runs doctor, shows report in a panel
- **MCP tool**: `daemon {action: "doctor"}` returns the report
- **CLI**: `agnt doctor` runs standalone (reads PID file, no daemon needed)

### Check list

| Check | Layer | Description |
|-------|-------|-------------|
| orphan_processes | OS | PIDs in tracker file but not managed by daemon |
| port_conflicts | OS | Declared ports held by unmanaged processes |
| daemon_health | OS | Daemon process alive and responsive |
| script_health | Daemon | Failed/crash-looping scripts |
| proxy_health | Daemon | Proxies reachable, targets responding |
| session_health | Daemon | Stale sessions |
| config_health | Daemon | .agnt.kdl validity, missing directories |
| startup_errors | Daemon | Recent startup log errors |
| process_tree | OS | Descendant processes not in tracker |
