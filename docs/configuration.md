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

## Auth Breakout (internal/config/agnt.go, auth-breakout in .agnt.kdl; runtime: internal/proxy/authbreakout.go)

The proxy's always-wrap model renders every page inside a content `<iframe>`.
Identity providers (Microsoft Entra/MSAL, Figma OAuth, Google, GitHub, Okta,
Auth0) refuse to render framed, so OAuth flows dead-end. The top-level
`auth-breakout` block hijacks those flows: a content-frame navigation (or
server 3xx) to a matching URL is carried out in a top-level window, and after
the IdP redirects back to the app origin the callback URL — hash fragment and
query intact — is replayed into the content iframe. Same tab, so
sessionStorage auth state (MSAL request nonce etc.) survives and
`handleRedirectPromise`-style callback handling works unchanged.

```kdl
auth-breakout {
    enabled true          // optional; declaring the block is the opt-in
    mode "popup"          // "popup" (default) or "top"
    patterns "login.microsoftonline.com" "figma.com/oauth"
}
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` when block present | `enabled false` keeps the block but turns it off |
| `mode` | string | `"popup"` | `popup`: auth runs in a named popup, app iframe stays alive (falls back to `top` when the popup is blocked). `top`: whole shell navigates to the IdP; the return redirect re-enters the proxy and is wrapped again |
| `patterns` | strings | common IdP set (`DefaultAuthBreakoutPatterns`) | Case-insensitive wildcard fragments matched against the full navigation URL. `*` matches any run; plain text matches as substring. Only navigations leaving the proxy origin are candidates |

Scope: project-wide — applies to every proxy of the project, on every daemon
creation path (autostart, URL detection, fallback port, explicit `proxy
start`, restart, restore).

MSAL apps additionally need `system: { allowRedirectInIframe: true }` in their
dev config, or the breakout never sees the navigation.

Interception routes, browser coverage, the MSAL caveat, and security notes:
**[auth-breakout.md](auth-breakout.md)**.
