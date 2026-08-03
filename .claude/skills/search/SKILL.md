---
name: search
description: Navigate the agnt codebase — find where features live, how subsystems connect, and which files own what
---

# agnt Codebase Search Guide

Use this skill when you need to find where something is implemented, understand how subsystems connect, or orient yourself before making changes.

## Package Map

| Package | Owns |
|---------|------|
| `cmd/agnt/` | CLI entry points, PTY wrapper, MCP server bootstrap |
| `internal/daemon/` | Background service, session/process/proxy lifecycle, IPC hub |
| `internal/proxy/` | Reverse HTTP proxy, traffic logging, JS injection, WebSocket |
| `internal/tools/` | All MCP tool implementations and schemas |
| `internal/overlay/` | Terminal status bar, popup menus, ANSI output filtering |
| `internal/chromedp/` | Browser automation sessions (CDP) |
| `internal/browser/` | Chrome/Chromium process lifecycle |
| `internal/tunnel/` | Cloudflare/ngrok tunnel management |
| `internal/snapshot/` | Visual regression: baseline capture, image diffing |
| `internal/aichannel/` | AI agent CLI/API abstraction (Claude, Gemini, Copilot…) |
| `internal/automation/` | Prompt-driven task processing via claude-go |
| `internal/project/` | Project type detection (Go/Node/Python) |
| `internal/config/` | `.agnt.kdl` parsing, proxy/script config |
| `internal/protocol/` | IPC verb definitions and request/response types |
| `internal/store/` | Persistent key-value storage (global/folder/page scopes) |
| `internal/debug/` | Debug logging to `~/.cache/agnt/logs/` |

---

## Feature Search Index

### MCP Tools
- **All tool registrations**: `grep -r "Register.*Tools\|mcp.NewTool" internal/tools/`
- **Tool handler pattern**: `internal/tools/daemon_tools.go` — `handleProxy*`, `handleProc*`
- **Tool schemas (input/output structs)**: `grep "json:\"" internal/tools/*.go`
- **get_incidents tool**: `internal/tools/get_incidents.go`; the unified-error collectors `proc snapshot` still uses live in `internal/tools/unified_error.go`
- **responsive_audit tool**: `internal/tools/responsive_audit.go`
- **daemon MCP tools** (proc, proxy, proxylog, daemon): `internal/tools/daemon_tools.go`

### Daemon — Process Lifecycle
- **Process start/stop**: `internal/daemon/daemon.go` — `StartScript`, `autostartScript`, `CleanupSessionResources`
- **Autostart orchestration**: `daemon.go` — `RunAutostart`, `startAutostartScripts`
- **Dependency ordering**: `internal/daemon/process_readiness.go` — `ProcessReadiness`, `MarkReady/Failed/Exited`, `Wait`
  - See also: `docs/plans/2026-03-15-process-dependency-ordering-design.md`
- **Process state machine**: `internal/daemon/daemon.go` + `go-cli-server/process`
- **Script registry** (ephemeral, rebuilt per session): `go-cli-server/script.Registry`
  - `CleanupSessionResources` clears it on last session disconnect
- **Auto-restarter**: `internal/daemon/auto_restart.go`
- **Port conflict pre-flight**: `internal/daemon/port_preflight.go`
- **Alert scanning** (compile errors, panics): `internal/overlay/alerts.go` → `internal/daemon/alert_store.go`

### Daemon — Proxy Lifecycle
- **Event pipeline** (URLDetected → proxy creation): `internal/daemon/proxy_events.go`
  - `handleURLDetected` — URL in script output → find matching proxy config → create proxy
  - `handleFallbackPortCheck` — fallback-port used when URL detection fails
  - `handleScriptStopped` — cleans up proxies linked to a script
  - See also: `.claude/rules/proxy-events.md`
- **Proxy creation from config**: `internal/daemon/daemon.go` — `autostartProxy`
- **Script→proxy tracking** (which proxies belong to which script): `scriptProxies` map + `trackScriptProxy`

### Daemon — Sessions & IPC
- **Hub handlers** (all IPC verbs): `internal/daemon/hub_handlers.go`
  - PROC LIST/START/STOP, PROXY LIST/START/STOP, SCRIPT LIST, SESSION LIST, ALERTS QUERY, DOCTOR RUN
- **Session registration**: `internal/daemon/daemon.go` — `handleSessionRegister`
- **Session registry**: `go-cli-server/session.Registry`
- **IPC verb constants**: `internal/protocol/commands.go`
- **Client-side IPC**: `internal/daemon/client.go`

### Daemon — Health & Diagnostics
- **Doctor checks** (all check functions): `internal/daemon/doctor.go`
  - `checkProcessHealth`, `checkProxyHealth`, `checkMissingProxies`, `checkRunningWithErrors`, `checkSessionHealth`, `checkStartupErrors`, `checkProcessTree`
- **Startup log** (per-process events): `internal/daemon/startup_log_store.go`
- **Alert store** (ring buffer of process alerts): `internal/daemon/alert_store.go`
- **Rogue process detection**: `hub_handlers.go` — `detectRogueProcess`
- **Reconciliation model**: `.claude/rules/daemon-architecture.md`

### Reverse Proxy
- **ProxyServer** (HTTP reverse proxy + WS + JS injection): `internal/proxy/server.go`
- **ProxyManager** (registry, fuzzy lookup, path-scoped): `internal/proxy/manager.go`
  - `GetWithPathFilter` — project-scoped lookup
  - `ErrProxyAmbiguous` — multiple fuzzy matches
- **Traffic logging** (1000-entry ring buffer, 14 log types): `internal/proxy/logger.go`
- **JS injection** into HTML responses: `internal/proxy/injector.go`
- **WebSocket upgrade**: `internal/proxy/websocket.go`
- **URL tracking** (detects dev server URLs in process output): `internal/daemon/urltracker.go`
- **URL matchers** (config-driven regex): `internal/daemon/urlmatcher.go`

### Overlay & PTY
- **PTY wrapper entry point**: `cmd/agnt/run.go` — `runCommand`
- **Output filtering** (blocks alt-screen, clamps scroll region): `internal/overlay/filter.go`
- **Output gate** (freeze/unfreeze during menus): `internal/overlay/gate.go`
- **Summarizer** (formats process/proxy data for overlay): `internal/overlay/summarizer.go`
- **Alert scanner** (classifies output: compile errors, panics, warnings): `internal/overlay/alerts.go`
- **Cross-process data flow**: `cmd/agnt/pty_common.go` — wires scanner → daemon alert push

### Browser Automation
- **CDP session management**: `internal/chromedp/manager.go`, `session.go`
- **Screenshot capture**: `internal/chromedp/session.go` — `CaptureScreenshot`
- **Browser process lifecycle**: `internal/browser/browser.go`

### Configuration
- **`.agnt.kdl` parsing**: `internal/config/agnt.go`
- **Proxy config** (script linking, fallback-port, url-matchers): `internal/config/agnt.go` — `ProxyConfig`
- **Script config** (run, cwd, depends-on, ports, autostart): `internal/config/agnt.go` — `ScriptConfig`
- **Dependency parsing + topo sort**: `internal/config/agnt_deps.go`
- **Config contracts** (what fields must be honored): `.claude/rules/config-contracts.md`

### Platform (WSL / Windows / Linux)
- **Platform detection**: `internal/platform/wsl.go` — `IsWSL()`, `ShouldUseWindowsShell(path)`
- **Never use `runtime.GOOS` directly** in WSL-aware code — always use platform package
- **Cross-platform mandate**: `.claude/rules/daemon-architecture.md`

---

## Key Architectural Rules

**Before changing anything in `internal/daemon/` read:**
- `.claude/rules/daemon-architecture.md` — personas, source of truth, silent failure rules
- `.claude/rules/daemon-lifecycle.md` — process/proxy state machines, session ownership
- `.claude/rules/proxy-events.md` — event pipeline, known silent failure points

**Before changing anything in `internal/proxy/` or `internal/config/` read:**
- `.claude/rules/config-contracts.md` — fields that are parsed must be acted on

**Import cycle constraint:**
- `daemon` cannot import `overlay` (overlay imports daemon via status types)
- Use string parameters or interfaces to cross this boundary
- Conversion happens in `cmd/agnt/pty_common.go`

---

## Common Search Patterns

```bash
# Find where a specific IPC verb is handled
grep -n "VerbProxy\|\"PROXY\"" internal/daemon/hub_handlers.go internal/protocol/commands.go

# Find all places a ProxyEvent type is used
grep -n "URLDetected\|ScriptStopped\|FallbackPortCheck\|ExplicitStart" internal/daemon/

# Find all MCP tool input/output structs
grep -n "type.*Input\|type.*Output" internal/tools/*.go

# Find all places ProcessReadiness is called (dependency coordination)
grep -n "MarkReady\|MarkFailed\|MarkExited\|MarkStarting\|\.Wait(" internal/daemon/daemon.go

# Find all places the script registry is touched
grep -rn "scriptRegistry\." internal/daemon/daemon.go

# Find where a specific config field is read vs. acted on
grep -rn "FallbackPort\|fallback.port" internal/

# Find all hub handler functions (IPC verb implementations)
grep -n "^func (d \*Daemon) hubHandle" internal/daemon/hub_handlers.go

# Find all doctor check functions
grep -n "^func check" internal/daemon/doctor.go
```

---

## Data Flow Diagrams

### Dev Server URL → Proxy Creation
```
Process stdout → URLTracker.Scan()
  → url-matchers (or regex scan)
  → onURLDetected(processID, url)
    → processReadiness.MarkReady(processID)
    → proxyEvents <- URLDetected{...}
      → handleURLDetected()
        → load .agnt.kdl
        → find ProxyConfig where Script == scriptName
        → proxym.Create(proxyID, targetURL)
```

### Process Exit → Dependency Unblocked
```
ProcessManager removes process
  → URLTracker.cleanupRemovedProcesses()
    → onProcessStopped(processID)
      → processReadiness.MarkExited(processID)  ← unblocks all waiters immediately
      → proxyEvents <- ScriptStopped{...}
        → handleScriptStopped() → stop linked proxies
```

### MCP Tool Call → Daemon IPC
```
AI Agent calls MCP tool (e.g. proc {action:"list"})
  → internal/tools/daemon_tools.go handler
    → DaemonTools.client.SendCommand("PROC LIST", filter)
      → Unix socket → daemon hub_handlers.go
        → hubHandleProcList() → ProcessManager.List()
          → JSON response → tool output
```

### Session Connect → Autostart
```
agnt run / Claude Code session
  → daemon.handleSessionRegister()
    → session added to registry
    → RunAutostart(projectPath)
      → load .agnt.kdl
      → port conflict pre-flight
      → TopologicalSort(scripts) → layers
      → for each layer: startAutostartScripts()
        → processReadiness.MarkStarting(pid)
        → waitForDependencies() → processReadiness.Wait(depPid)
        → autostartScript() → StartScript()
```
