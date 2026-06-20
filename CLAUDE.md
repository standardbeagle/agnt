# CLAUDE.md

Project guidance for Claude Code working this repo. Orientation + invariants
only — detailed references live in `docs/` and `.claude/rules/` (see
[Reference Map](#reference-map)).

## Project Overview

**agnt** - Browser superpowers for AI coding agents. Bridge AI agent and browser for real-time debug, UI wireframe, visual feedback.

- **Version**: 0.13.23
- **Repository**: https://github.com/standardbeagle/agnt

**Binaries**:
- `agnt`: Primary CLI (only binary actually built)
- `agnt-daemon`: Copy for daemon auto-start (work around fork prevention in sandbox)
- `devtool-mcp`: Legacy alias (backwards compat)

**CLI Subcommands**: `mcp` (MCP server), `run` (PTY wrapper), `init` (setup-only, no relaunch), `skills` (install agnt skills via `npx skills` + register MCP), `monitor` (event stream), `ai` (interactive AI), `hook` (telemetry forwarder), `setup-project`, `activate`/`license` (Pro license activation + management — offline lk validation, see `internal/license/`)

**Core Architecture Decisions**:

1. **Binary copies instead of self-exec**: Sandbox (like Claude Code) blocks binaries from fork/exec'ing self. Separate binary copies bypass this.

2. **`agnt run` workaround for MCP notifications**: MCP servers can't push notifications to clients. `agnt run` wraps AI tools in PTY, injects browser events as synthetic stdin:
   ```
   Browser → Proxy → HTTP POST → Overlay (port 19191) → PTY stdin → AI Tool
   ```

3. **System prompt injection**: Auto-injects agnt context when starting AI agents — Claude Code via `--append-system-prompt`; others (Gemini, Copilot, Aider) via initial stdin message after 500ms delay. Detail: `docs/agent-adapters.md`.

**Core Features**: browser debug (screenshots, DOM inspect, error capture), floating indicator messaging, sketch mode (Excalidraw-like wireframe), design mode (AI UI iteration), process/proxy management with daemon persistence, PTY overlay.

## Installation

```bash
# Install binary
go install github.com/standardbeagle/agnt/cmd/agnt@latest
# or: make install-local

# Register MCP
claude mcp add agnt -s user -- agnt mcp
```

Claude Code plugin moved to standalone marketplace repo — this repo ships `agnt` binary + MCP server only.

**MCP Config** (`claude_desktop_config.json`): `"agnt": {"command": "agnt", "args": ["mcp"]}`

**Project Setup**: `/agnt:setup-project` (auto-detects project, configures auto-start)

## Build Commands

```bash
make build          # Build agnt binary
make all            # Build + create binary copies
make test           # All tests (except procisolation-tagged)
make test-isolated  # procisolation tests inside PID namespace
make test-coverage  # Generate coverage.html
make install-local  # Install to ~/.local/bin

# Single package tests
go test -v ./internal/process
go test -race ./...
```

## Architecture

### Five-Layer Design

1. **MCP Tools** (`internal/tools/`): Expose daemon-aware MCP tools
2. **Daemon** (`internal/daemon/`): Background service, persistent state, socket IPC
3. **Protocol** (`internal/protocol/`): Text-based IPC protocol (commands/responses)
4. **Business Logic** (`internal/project/`, `internal/process/`, `internal/proxy/`): Project detection, process management, reverse proxy
5. **Infrastructure** (`internal/process/ringbuf.go`, `internal/config/`): RingBuffer, config

### Critical Design: Lock-Free Process Management

**ProcessManager**: `sync.Map` process registry (lock-free); `atomic.Int64` metrics (activeCount, totalStarted, totalFailed); `atomic.Bool` shutdown coordination.

**ManagedProcess**: all state fields atomic — `atomic.Uint32` (state), `atomic.Int32` (PID/exitCode), `atomic.Pointer[time.Time]` (timestamps). Single `sync.Mutex` only in RingBuffer for boundary writes.

### Process Lifecycle State Machine

```
Pending → Starting → Running → Stopping → Stopped/Failed
              ↓                     ↓
          Failed ←──────────────────┘
```

State transitions atomic via `CompareAndSwapState()`. Child cleanup: process groups (`Setpgid: true`) + `signalProcessGroup()` for parent + children.

### Reverse Proxy Architecture

**ProxyServer** (`internal/proxy/server.go`): `httputil.ReverseProxy` base; JS injection into HTML responses; WebSocket server for frontend metrics (`/__devtool_metrics`); lock-free `sync.Map` registry; auto-port discovery, auto-restart (max 5/min).

Four-part system: (1) HTTP proxy forwards/logs/modifies, (2) JS injection (error tracking, `__devtool` API), (3) WebSocket server receives metrics, (4) JS execution (`proxy exec` for browser control).

**TrafficLogger** (`internal/proxy/logger.go`): circular buffer (1000 entries); 16 log types (HTTP, Error, Performance, Custom, Screenshot, Execution, Response, Interaction, Mutation, PanelMessage, Sketch, DesignState, DesignRequest, DesignChat, ResponsiveRequest, ResponsiveState); thread-safe `sync.RWMutex`; `onLogEntry` callback feeds StreamEvents hub.

### StreamEvents Hub

**AlertHub** (`internal/daemon/alert_hub.go`) — three delivery sinks: **OverlayAlertSink** (PTY stdin injection), **MCPAlertSink** (MCP `Log()` notifications), **StreamSink** (channel streaming with filtering, for `agnt monitor`).

Push channel config = `alerts.push` in `.agnt.kdl` (presets `claude-code` = MCP only, `universal` = all; default = all enabled). When `alerts.incident-pipeline true`, all three sinks replaced by Pinger (`internal/incident/pinger.go`); `get_incidents` = authoritative pull surface, `get_errors` = shim.

**STREAM-EVENTS** (`internal/daemon/hub_stream.go`): daemon-side handler registers `StreamSink` with type/proxy/process/severity/grep filters; 30s keepalive; `BroadcastLogEntry()` / `BroadcastProcessOutput()` push filtered events to matching sinks.

### Incident Pipeline (`internal/incident/`)

Opt-in (Phase A, `alerts { incident-pipeline true }`, default `false`). Nine-layer pipeline normalises all signal sources into a priority inbox, pushes compact pings to the AI agent:

```
Signal sources → Bus → Dedup/Coalesce/FlowControl → Inbox → Pinger → MCP/channel/PTY
```

Full layer-by-layer spec, source-of-truth table, and numbered invariants:
**`.claude/rules/daemon-architecture.md` § Incident Pipeline**. Key files: `internal/incident/` package.

### Overlay UI

PTY overlay components — command palette (`:`/`/` filterable, **not** a shell box), ports & orphans panel, startup splash, animated indicator, output-protection chain (`PTY → ProtectedWriter → OutputGate → os.Stdout`). Detail + routing invariants: **`docs/overlay-internals.md`**.

## MCP Tools

| Tool | Description |
|------|-------------|
| `detect` | Detect project type (Go/Node/Python) + scripts |
| `run` | Run scripts/commands (background/foreground/foreground-raw) |
| `proc` | Process management (status, output, stop, list, cleanup_port) |
| `proxy` | Reverse proxy (start, stop, status, list, exec) |
| `proxylog` | Query proxy logs (query, clear, stats) |
| `tunnel` | Tunnel management (cloudflare/ngrok) |
| `currentpage` | Page session tracking |
| `get_errors` | Unified error view (legacy; superseded by `get_incidents`) |
| `get_incidents` | Incident inbox pull — cursor-based, priority-ordered, remediation hints |
| `responsive_audit` | Responsive design audits across viewport sizes |
| `api_audit` | API efficiency audit (waterfall, N+1, duplicate, chatty-load) over the fetch/XHR buffer |
| `loading_audit` | Loading-UX audit (spinner cascade + concurrent fragmentation) over the spinner timeline |
| `snapshot` | Visual regression testing (baseline/compare screenshots) |
| `replaytest` | Record→worker-mock→replay front-end testing; fuzz + subagent breadth (Pro: advanced_testing) |
| `daemon` | Daemon management |
| `watch` | Get `agnt monitor` command for streaming events |
| `channel_reply` | Send messages to developer's browser overlay (channel mode beta) |

**Handler pattern**: Input/Output structs with JSON schema tags; return `(*mcp.CallToolResult, OutputStruct, error)`; errors as `CallToolResult{IsError: true}` (NOT Go errors).

**Session scoping**: query/list tools scoped to caller's session project by default; gated tools take `global: true` for cross-project. Chokepoint = `resolveProjectScope`. Full classification: `.claude/rules/daemon-architecture.md` § Tool session-scoping.

Per-tool parameters, output formats, the `window.__devtool` frontend API, `agnt monitor`, and tunnel usage: **`docs/mcp-tools.md`**.

## Configuration

`.agnt.kdl` per-project config. Hardcoded daemon defaults: `DefaultTimeout: 0`, `MaxOutputBuffer: 256KB`, `GracefulTimeout: 5s`, `HealthCheckPeriod: 10s` (`main.go:31-36`).

Port-conflict policy, autostart cleanup ordering, alert push channels, incident-pipeline keys, URL tracking: **`docs/configuration.md`**.

**KDL for app config** (settings, keybindings, preferences); **JSON for content data, API contracts, LLM-consumed formats**.

## Hooks & Channel Mode

- **Hook dispatcher** (`agnt hook`): fire-and-forget telemetry forwarder into daemon ring buffer (p99 ≤5ms, always exit 0 on transient failure). Events, drain fan-out, sample `settings.json`: **`docs/hook-dispatcher.md`**. Bash-interceptor side (`check-bash`/`check-prompt`): `docs/hook-rules.md`.
- **Channel Mode** (beta, Claude Code only): push-based event forwarding via MCP `claude/channel`; no `agnt run` needed. Setup, event shape, `channel_reply`: **`docs/channel-mode.md`**.

## Testing

**Two-tier suite**: `make test` (everything except `procisolation`-tagged, safe any host) and `make test-isolated` (`procisolation` tests inside `unshare --user --pid --mount --fork --mount-proc`, Linux only). Isolated target exists because a subset exercises real `/proc` walks + real `kill(2)` against dead-leader pgids — would reap unrelated same-uid processes if run natively.

`procisolation`-tagged: `internal/daemon/daemon_orphan_pgid_test.go`, `internal/platform/orphanpgid_unix_test.go`.

All other daemon tests run natively; `Start()`'s orphan scan gated by `DaemonConfig.OrphanScanEnabled` (defaults `false` via zero value — literal `DaemonConfig{}` is safe). Production sets `true` in `cmd/agnt/daemon.go`. **This field is an internal test-safety knob — never document as user-facing or expose in `.agnt.kdl`.** Replaces legacy `AGNT_DISABLE_ORPHAN_SCAN` env var (deleted).

**Pre-commit hook** (`.git/hooks/pre-commit`): `gofmt`, `go vet ./...`, then `go test -count=1 -race -p 1` on staged packages. Adaptive flake detection: first race pass <10s → 2 more passes (`-count=2`); slow packages (`internal/daemon` ~90s) get single pass only. Tests starting real OS processes (`sleep`, `echo`, agnt binary) must NOT use `t.Parallel()` — `exec.CommandContext` PID-reuse race under high concurrency kills unrelated processes.

Test startup contract (`Start()` vs `NewForTest`): `.claude/rules/daemon-architecture.md` § Test startup contract.

## Important Constraints

### MCP Protocol
- Tool names: `^[a-zA-Z0-9_-]{1,128}$`; transport stdio only (logs to stderr); all I/O needs JSON schema tags; errors as `CallToolResult{IsError: true}` (NOT Go errors).

### Process Management
- No timeout by default (`DefaultTimeout: 0`); 256KB output buffer per stream; graceful shutdown 5s SIGTERM → SIGKILL; aggressive SIGKILL when deadline <3s; health checks 10s.
- **Session pgid containment**: PTY child's pgid = session container. `CleanupSessionResources` kills entire pgid (SIGTERM, 2s grace, SIGKILL) before touching managed processes — catches `npm run dev &` and other backgrounded jobs. Startup scans for orphaned pgids from daemon crashes. Accepted escapes: `setsid &`, double-fork, `systemd-run`, container runtimes. Full invariant + file ownership: `.claude/rules/daemon-architecture.md` § Session Containment.

### Reverse Proxy
- Default port hash-based (stable, 10000-60000); traffic log 1000 entries; request/response 10KB max in logs; `/__devtool_metrics` reserved; JS injection only `text/html`; auto-restart max 5/min.

### Platform Support
- **Linux/macOS**: `Setpgid: true`, SIGTERM/SIGKILL, `creack/pty`, SIGWINCH resize, PPID chain walking via `/proc/<pid>/stat`.
- **Windows**: ConPTY, Job Objects, `CTRL_BREAK_EVENT`, named pipes (`\\.\pipe\devtool-mcp-<username>`).
- **WSL**: GOOS=linux but reaches Windows-side processes. Use `platform.IsWSL()` / `ShouldUseWindowsShell(path)`, not bare `runtime.GOOS`. Full audit: `.claude/rules/wsl-audit.md`.

## Common Gotchas

1. **Process ID conflicts**: `Register()` → `ErrProcessExists`
2. **State validation**: use `CompareAndSwapState()` for atomic transitions
3. **Output truncation**: check `truncated` flag in RingBuffer
4. **Shutdown race**: check `pm.IsShuttingDown()` before registration
5. **Context cancellation**: all ops respect context
6. **Project detection order**: Go → Node → Python (first match wins)
7. **Proxy ID conflicts**: `Create()` → `ErrProxyExists`
8. **Log buffer overflow**: check `dropped` count in stats
9. **JS injection failures**: silent fail if HTML malformed
10. **Port auto-discovery**: check `listen_addr` in response
11. **Reserved endpoint**: `/__devtool_metrics` shadows backend routes
12. **Overlay can't import daemon**: data flows daemon→IPC→overlay (`status.go`, `summarizer.go` create the cycle). Use interfaces or string params.

## Reference Map

| Topic | Location |
|-------|----------|
| Per-tool params, output formats, `__devtool` API, `agnt monitor` | `docs/mcp-tools.md` |
| `.agnt.kdl` config (port-conflict, alert push, incident keys, URL tracking) | `docs/configuration.md` |
| Hook dispatcher (telemetry forward) | `docs/hook-dispatcher.md` |
| Hook Bash-interceptor (`check-bash`/`check-prompt`) | `docs/hook-rules.md` |
| Channel Mode beta | `docs/channel-mode.md` |
| Overlay UI internals (palette, ports, splash, output protection) | `docs/overlay-internals.md` |
| Agent system-prompt injection per tool | `docs/agent-adapters.md` |
| Daemon invariants (source-of-truth, incident pipeline, session containment, session-scoping, test startup) | `.claude/rules/daemon-architecture.md` |
| Daemon lifecycle | `.claude/rules/daemon-lifecycle.md` |
| Config contracts | `.claude/rules/config-contracts.md` |
| Proxy events | `.claude/rules/proxy-events.md` |
| WSL awareness audit | `.claude/rules/wsl-audit.md` |
| Doc index | `docs/README.md` |

## Dev Notes

- **Version management**: `scripts/release.sh` updates all version numbers (never hand-edit)
- **Binary copies**: workaround for fork prevention in sandbox
- **Future**: persistent logs, HAR export, SSL/TLS, process labels

## Forked Dependencies

- **`github.com/standardbeagle/go-sdk`** (v1.5.0-agnt.2): fork of `modelcontextprotocol/go-sdk` adding `ServerSession.Notify(ctx, method, params)` for custom notification methods. Used directly (no `replace` directive) so `go install` works. When upstream merges PR #898, swap imports back and bump version.
