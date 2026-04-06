# Daemon Architecture

## Personas

Four distinct participants interact with the daemon. Every feature must consider all four:

1. **Developer** — configures `.agnt.kdl`, runs `agnt run` or opens a Claude Code session, expects dev servers and proxies to "just work." Needs a "doctor" command for manual verification and cleanup.

2. **AI Agent** — calls MCP tools (`proc`, `proxy`, `proxylog`, `get_errors`), makes decisions based on state. Requires verified-accurate state — stale or contradictory data causes the agent to take wrong actions, which is worse than no data.

3. **Daemon** — long-running background process that outlives any session. Orchestrates lifecycles, manages the event system, serves as the state cache.

4. **Managed processes/proxies** — active participants, not passive resources. They emit errors, need restarts, can go rogue (zombie PIDs, orphaned ports). Their state must be verified against OS truth, not assumed from daemon memory.

## Data Ownership — Source of Truth

Daemon in-memory state is a **cache**, never the authority. The canonical source of truth for each piece of state:

| State | Source of truth | Verification method |
|-------|----------------|-------------------|
| Process alive/dead | OS (PID check) | Platform-specific PID probe |
| Port ownership | OS (socket table) | Platform-specific port scan |
| Proxy alive/responsive | Proxy instance (health probe) | TCP connect or HTTP GET |
| Script config / expected state | `.agnt.kdl` on disk | Parse and compare |
| URL associations | URLTracker cache | Verified against actual port binding |
| Script registry entries | Session lifecycle | Rebuilt from config on each session connect |

**The rule**: Any mismatch between daemon cache and source of truth = daemon updates its cache to match reality and emits an event. The daemon never asserts its cache is correct over OS truth.

**Script registry is ephemeral**: The script registry is rebuilt from `.agnt.kdl` on each session connect. When the last session for a project disconnects, `CleanupSessionResources` removes all registry entries. The next session starts fresh from current config. Never persist or carry over registry state across sessions.

## Reconciliation Model

Three triggers for reconciliation:

1. **On session connect** — daemon runs full health check before responding to first query. The AI agent must receive verified-accurate state.
2. **Periodic** — every 30s (configurable), daemon runs health check and emits events for any state changes. Never kills processes — only updates state and surfaces issues.
3. **Doctor command** — developer-initiated full reconciliation via MCP tool and overlay panel. Returns structured report with offered actions.

## Cross-Platform Mandate

**Every OS-level operation must go through `internal/platform/` and handle all four targets:**

| Operation | Linux/macOS | Windows | WSL |
|-----------|------------|---------|-----|
| PID alive check | `kill(pid, 0)` / `/proc/<pid>` | `OpenProcess` / Job Objects | `kill(pid, 0)` but may need `cmd.exe` for Windows-spawned processes |
| Port ownership | `ss -tlnp` / `lsof -i` | `netstat -ano` / `Get-NetTCPConnection` | `ss` for Linux ports, `netstat.exe` for Windows-bound ports |
| Process group kill | `SIGTERM` → `SIGKILL` to pgid | `CTRL_BREAK_EVENT` → `TerminateJobObject` | Depends on `platform.ShouldUseWindowsShell(path)` |
| Proxy health probe | TCP connect / HTTP GET | Same | Same |
| Rogue process identification | `ss -tlnp` gives PID | `netstat -ano` + `tasklist` | Both paths depending on which OS owns the port |

The existing `platform.IsWSL()` / `ShouldUseWindowsShell()` pattern in `internal/platform/wsl.go` is the model. New OS-level operations must follow this pattern. Never use `runtime.GOOS` directly for platform checks in WSL-aware code.

## Silent Failure Prohibition

No subsystem may silently skip an expected action. If config declares a proxy, process, or dependency, the system must either:
1. Successfully create/start it, OR
2. Emit a visible error/warning event that reaches the AI agent and session log

`debug.Log` is not sufficient for failures — it only goes to the debug file. Failures must propagate through the event system or session log.

## Config Authority

If `.agnt.kdl` declares expected state (a proxy with `fallback-port`, a script with `depends-on`), the system must honor it. Config fields that are parsed but not acted on are bugs.
