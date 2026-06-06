# Configuration Reference

Daemon defaults, port-conflict policy, autostart ordering, alert push channels,
and incident-pipeline keys. CLAUDE.md carries the short constraints list; this is
the detailed reference.

## Hardcoded defaults (`main.go:31-36`)

```go
ManagerConfig{
    DefaultTimeout:    0,                    // No timeout
    MaxOutputBuffer:   256 * 1024,          // 256KB
    GracefulTimeout:   5 * time.Second,
    HealthCheckPeriod: 10 * time.Second,
}
```

## Dev Server URL Tracking (`internal/daemon/urltracker.go`)

- Scans first 8KB of output
- Stores max 5 URLs per process
- Only localhost-like URLs with ports

## Port Conflict Pre-flight (`internal/daemon/port_preflight.go`, `daemon.go:RunAutostart`)

Before launching autostart scripts, daemon scans all declared `ports` from autostart scripts for unmanaged processes. Configurable via `port-conflict` in `project` node of `.agnt.kdl`:

```kdl
project {
    port-conflict "prompt"   // prompt (default) | auto-kill | skip | fail
}
```

| Policy | Behavior |
|--------|----------|
| `prompt` | Return conflicts to client, wait for `AUTOSTART CLEAR-PORTS` or `AUTOSTART CONTINUE` IPC |
| `auto-kill` | Kill blocking process trees automatically, log what killed |
| `skip` | Log warning, start scripts anyway (they'll fail on bind) |
| `fail` | Abort autostart entirely |

Kill uses `ProcessManager.KillProcessByPort()` with process-group SIGTERM → 3s wait → SIGKILL escalation + descendant tree walk. `AutostartResult` extended with `PortConflicts` and `PortsCleared` fields.

**Key files**: `port_preflight.go` (detect + kill), `daemon.go` (RunAutostart integration + pendingAutostarts), `hub_handlers.go` (AUTOSTART verb), `client.go` (AutostartClearPorts/Continue), `pty_common.go` (client prompt)

## Autostart Cleanup Ordering (`internal/daemon/daemon_autostart.go`)

1. **Duplicate scan** (sync, before autostart) — kills orphaned dev server processes using `collectManagedPIDs()` PPID chain walking to protect managed children
2. **Stale process cleanup** (sync, before autostart) — stops processes from previous sessions
3. **Starting** — launches autostart scripts in dependency order
4. **Started** — confirms all scripts launched

## Alert Push Channels (`internal/config/agnt.go`, `alerts.push` in `.agnt.kdl`)

Controls which delivery channels push alerts to AI client:

```kdl
alerts {
    push {
        mcp-notifications true   // MCP session.Log() notifications
        pty-injection false      // PTY stdin injection
    }
    // Or use a preset:
    preset "claude-code"   // MCP only, no PTY injection

    // Incident pipeline (opt-in, Phase A):
    incident-pipeline false  // true = route through internal/incident/ instead of AlertHub
    blob-budget 16777216     // per-session BlobStore cap in bytes (default 16MB)
    ping {
        mcp-notifications true   // Pinger → MCP session.Log() pings
        pty-injection false      // Pinger → PTY stdin injection
        channel true             // Pinger → channel push (requires channel.enabled true)
    }
}
```

| Preset | MCP Notifications | PTY Injection |
|--------|------------------|---------------|
| `claude-code` | enabled | disabled |
| `universal` | enabled | enabled |
| (none) | enabled | enabled |

**Incident pipeline keys** (only active when `incident-pipeline true`):
| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `incident-pipeline` | bool | `false` | Route alerts through incident pipeline |
| `blob-budget` | int | `16777216` | Per-session BlobStore cap (bytes) |
| `ping.mcp-notifications` | bool | `true` | Pinger → MCP notifications |
| `ping.pty-injection` | bool | `false` | Pinger → PTY stdin |
| `ping.channel` | bool | `true` | Pinger → channel push |
