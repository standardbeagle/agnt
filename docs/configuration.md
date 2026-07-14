# Configuration Reference

Daemon defaults, port-conflict policy, autostart ordering, alert push channels,
and incident-pipeline keys. CLAUDE.md carries the short constraints list; this is
the detailed reference.

Remote SSH has no shipped `.agnt.kdl` keys. Use the `agnt ssh` and `agnt push` flags in [remote-ssh.md](remote-ssh.md); SSH KDL shown in archived design specs is not implemented configuration.

## Default query scope

Query/list tools are project-scoped unless a project opts into cross-project results:

```kdl
scope {
    default-global true
}
```

The default is `false`. A tool call's explicit `global: true` or `global: false`
always overrides this setting; omitting `global` uses the project setting.

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

## Public Walkthrough Feedback (`internal/config/feedback.go`, `feedback` in `.agnt.kdl`)

The `feedback` block bounds the public-plane anonymous feedback sink used by
published walkthroughs — the rate limit, per-post size cap, and retention ring.
The values encode the security spec §5
(`docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md`).
See the operator guide: **[public-walkthroughs.md](public-walkthroughs.md)**.

```kdl
feedback {
    rate-per-minute 10       // sustained feedback POST budget per (share token, IP)
    burst 5                  // token-bucket burst allowance per (share token, IP)
    max-body-bytes 4096      // max size of a single feedback POST body
    max-rows-per-share 500   // retention ring depth per share (oldest evicted)
    retention-days 90        // evict rows older than this many days
}
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `rate-per-minute` | int | `10` | Sustained feedback POST budget per (share, IP). Excess ⇒ `429`. |
| `burst` | int | `5` | Token-bucket burst allowance per (share, IP). |
| `max-body-bytes` | int | `4096` | Per-POST body cap. Over-cap ⇒ `413` (never truncated). |
| `max-rows-per-share` | int | `500` | Retention ring depth per share; oldest row evicted. |
| `retention-days` | int | `90` | Rows older than this are evicted. Retention is **500 rows OR 90 days, whichever first**. |

Every key is optional. An omitted or non-positive value falls back to its spec
default via `FeedbackConfig.Normalize` — a misconfigured value can never *disable*
a guard (a `0` rate would mean "never allow"; a `0` cap would mean "reject
everything"), so it is always replaced with the safe default rather than left
unbounded.

The `feedback` block is defined in `internal/config` (`KDLFeedback` →
`config.FeedbackConfig`), parses from `.agnt.kdl`, and is honored by the live
limiter: `runDaemonStart` (`cmd/agnt/daemon.go`) loads the config and threads
`Feedback` into `DaemonConfig.FeedbackLimits`, and `buildPublicPlane` normalizes
only the *unset* fields to the spec §5 defaults above. A non-default value you set
(rate, burst, body cap, retention) takes effect on the running public feedback
route; a malformed config fails startup loud rather than falling back silently.

## Public Walkthrough Listener (`AGNT_PUBLIC_ADDR` environment variable)

The anonymous-viewer **public plane** for published walkthroughs is **opt-in**.
The token-gated public handler (`GET /s/{token}`, `/variants.json`,
`/walkthrough.json`, `POST /s/{token}/feedback`) is always *built* inside the
daemon so owner-scoped `publish feedback` reads work, but a dedicated public HTTP
listener is stood up **only** when `AGNT_PUBLIC_ADDR` is set — the daemon never
auto-binds a public port.

```sh
export AGNT_PUBLIC_ADDR=":8899"   # bind the public plane on :8899; unset = no public port
```

This is an environment variable, **not** a KDL key (`cmd/agnt/daemon.go:128`). A
bind failure is surfaced loud but is non-fatal: the control plane and dev proxy
stay up, only the public plane is unavailable. Unsetting it and restarting the
daemon removes the public surface entirely. See
**[public-walkthroughs.md](public-walkthroughs.md)**.
