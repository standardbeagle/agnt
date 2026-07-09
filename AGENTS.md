# CLAUDE.md

Claude Code 治此 repo 之綱：唯導向與不變式；詳見 `docs/`、`.claude/rules/`（見 [Reference Map](#reference-map)）。

## Project Overview

**agnt**：賦 AI coding agents 以 browser superpowers；通 AI agent 與 browser，作 real-time debug、UI wireframe、visual feedback。

- **Version**: 0.13.31
- **Repository**: https://github.com/standardbeagle/agnt

**Binaries**:
- `agnt`: 主 CLI（唯一實建 binary）
- `agnt-daemon`: daemon auto-start 副本（避 sandbox 禁 fork）
- `devtool-mcp`: 舊名 alias（backwards compat）

**CLI Subcommands**: `mcp` (MCP server), `run` (PTY wrapper), `init` (setup-only, no relaunch), `skills` (install agnt skills via `npx skills` + register MCP), `monitor` (event stream), `ai` (interactive AI — Claude-only, stream-json), `acp` (any ACP agent via `coder/acp-go-sdk`: gemini/opencode/claude-code-acp; one-shot + overlay/cooked REPL; fs+terminal caps; deterministic alert gate), `hook` (telemetry forwarder), `setup-project`, `activate`/`license` (Pro license activation + management — offline lk validation, see `internal/license/`)

**Core Architecture Decisions**:

1. **Binary copies instead of self-exec**：sandbox（如 Claude Code）禁 binary fork/exec self；別本 binary 可避之。
2. **`agnt run` workaround for MCP notifications**：MCP servers 不能 push notifications；`agnt run` 以 PTY 包 AI tools，注 browser events 為 synthetic stdin：
   ```
   Browser → Proxy → HTTP POST → Overlay (port 19191) → PTY stdin → AI Tool
   ```
3. **System prompt/context delivery**：起 AI agents 時自注或持久化 agnt context；Claude Code 用 `--append-system-prompt`；Gemini, Copilot, Aider 等 normal sessions 寫入 agent context file，setup mode 才用 stdin prompt。詳 `docs/agent-adapters.md`。

**Core Features**: browser debug (screenshots, DOM inspect, error capture), floating indicator messaging, sketch mode (Excalidraw-like wireframe), design mode (AI UI iteration), process/proxy management with daemon persistence, PTY overlay.

## Installation

```bash
# Install binary
go install github.com/standardbeagle/agnt/cmd/agnt@latest
# or: make install-local

# Register MCP
claude mcp add agnt -s user -- agnt mcp
```

Claude Code plugin 已遷 standalone marketplace repo；此 repo 唯出 `agnt` binary + MCP server。

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

1. **MCP Tools** (`internal/tools/`): daemon-aware MCP tools
2. **Daemon** (`internal/daemon/`): background service, persistent state, socket IPC
3. **Protocol** (`internal/protocol/`): text IPC protocol
4. **Business Logic** (`internal/project/`, `internal/process/`, `internal/proxy/`): project detection, process management, reverse proxy
5. **Infrastructure** (`internal/process/ringbuf.go`, `internal/config/`): RingBuffer, config

### Critical Design: Lock-Free Process Management

**ProcessManager**：`sync.Map` registry；`atomic.Int64` metrics (`activeCount`, `totalStarted`, `totalFailed`)；`atomic.Bool` shutdown coordination。

**ManagedProcess**：state 皆 atomic：`atomic.Uint32` (`state`), `atomic.Int32` (`PID`/`exitCode`), `atomic.Pointer[time.Time]` (`timestamps`)。唯 RingBuffer boundary writes 用一 `sync.Mutex`。

### Process Lifecycle State Machine

```
Pending → Starting → Running → Stopping → Stopped/Failed
              ↓                     ↓
          Failed ←──────────────────┘
```

State transitions 必由 `CompareAndSwapState()` atomic。Child cleanup：process groups (`Setpgid: true`) + `signalProcessGroup()` 殺 parent + children。

### Reverse Proxy Architecture

**ProxyServer** (`internal/proxy/server.go`)：基 `httputil.ReverseProxy`；HTML response 注 JS；WebSocket server for frontend metrics (`/__devtool_metrics`)；`sync.Map` registry；auto-port discovery；auto-restart max 5/min。

四部：1 HTTP proxy forwards/logs/modifies；2 JS injection（error tracking, `__devtool` API）；3 WebSocket server receives metrics；4 JS execution (`proxy exec` browser control)。

**TrafficLogger** (`internal/proxy/logger.go`)：circular buffer 1000；16 log types (HTTP, Error, Performance, Custom, Screenshot, Execution, Response, Interaction, Mutation, PanelMessage, Sketch, DesignState, DesignRequest, DesignChat, ResponsiveRequest, ResponsiveState)；`sync.RWMutex`；`onLogEntry` callback 入 StreamEvents hub。

### StreamEvents Hub

**AlertHub** (`internal/daemon/alert_hub.go`) 三 sink：**OverlayAlertSink** (PTY stdin injection), **MCPAlertSink** (MCP `Log()` notifications), **StreamSink** (channel streaming with filtering, for `agnt monitor`)。

Push channel config = `alerts.push` in `.agnt.kdl`；presets `claude-code` = MCP only, `universal` = all；default = all enabled。若 `alerts.incident-pipeline true`，三 sink 皆換 Pinger (`internal/incident/pinger.go`)；`get_incidents` 為 authoritative pull surface，`get_errors` 為 shim。

**STREAM-EVENTS** (`internal/daemon/hub_stream.go`)：daemon handler 以 type/proxy/process/severity/grep filters 註冊 `StreamSink`；30s keepalive；`BroadcastLogEntry()` / `BroadcastProcessOutput()` 推 filtered events 至 matching sinks。

### Incident Pipeline (`internal/incident/`)

Opt-in Phase A：`alerts { incident-pipeline true }`，default `false`。九層 pipeline 統一 signal sources 入 priority inbox，推 compact pings 至 AI agent：

```
Signal sources → Bus → Dedup/Coalesce/FlowControl → Inbox → Pinger → MCP/channel/PTY
```

逐層 spec、source-of-truth table、numbered invariants 見 **`.claude/rules/daemon-architecture.md` § Incident Pipeline**。Key files: `internal/incident/` package。

### Overlay UI

PTY overlay components：command palette (`:`/`/` filterable, **not** a shell box), ports & orphans panel, startup splash, animated indicator, output-protection chain (`PTY → ProtectedWriter → OutputGate → os.Stdout`)。詳與 routing invariants：**`docs/overlay-internals.md`**。

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

**Handler pattern**：Input/Output structs 帶 JSON schema tags；return `(*mcp.CallToolResult, OutputStruct, error)`；errors 作 `CallToolResult{IsError: true}`（非 Go errors）。

**Session scoping**：query/list tools 默認 scoped to caller's session project；gated tools 以 `global: true` cross-project。關隘 = `resolveProjectScope`。全分類：`.claude/rules/daemon-architecture.md` § Tool session-scoping。

Per-tool parameters、output formats、`window.__devtool` frontend API、`agnt monitor`、tunnel usage：**`docs/mcp-tools.md`**。

## Configuration

`.agnt.kdl` per-project config。Hardcoded daemon defaults：`DefaultTimeout: 0`, `MaxOutputBuffer: 256KB`, `GracefulTimeout: 5s`, `HealthCheckPeriod: 10s` (`main.go:31-36`)。

Port-conflict policy、autostart cleanup ordering、alert push channels、incident-pipeline keys、URL tracking：**`docs/configuration.md`**。

**KDL for app config** (settings, keybindings, preferences)；**JSON for content data, API contracts, LLM-consumed formats**。

## Hooks & Channel Mode

- **Hook dispatcher** (`agnt hook`)：fire-and-forget telemetry forwarder 入 daemon ring buffer（p99 ≤5ms；transient failure always exit 0）。Events、drain fan-out、sample `settings.json`：**`docs/hook-dispatcher.md`**。Bash-interceptor side (`check-bash`/`check-prompt`)：`docs/hook-rules.md`。
- **Channel Mode** (beta, Claude Code only)：push-based event forwarding via MCP `claude/channel`；免 `agnt run`。Setup、event shape、`channel_reply`：**`docs/channel-mode.md`**。

## Testing

**Two-tier suite**：`make test`（除 `procisolation`-tagged，host-safe）與 `make test-isolated`（`procisolation` tests in `unshare --user --pid --mount --fork --mount-proc`，Linux only）。isolated 因 subset 走真 `/proc` + 真 `kill(2)` 對 dead-leader pgids；native 恐 reap unrelated same-uid processes。

`procisolation`-tagged: `internal/daemon/daemon_orphan_pgid_test.go`, `internal/platform/orphanpgid_unix_test.go`.

餘 daemon tests native；`Start()` orphan scan 由 `DaemonConfig.OrphanScanEnabled` gate（zero value default `false`，literal `DaemonConfig{}` safe）。Production 於 `cmd/agnt/daemon.go` 設 `true`。**此欄唯 internal test-safety knob，勿寫 user-facing docs，勿 expose in `.agnt.kdl`.** 代舊 `AGNT_DISABLE_ORPHAN_SCAN` env var（已刪）。

**Pre-commit hook** (`.git/hooks/pre-commit`)：`gofmt`, `go vet ./...`, then `go test -count=1 -race -p 1` on staged packages。Adaptive flake detection：first race pass <10s → 2 more passes (`-count=2`)；slow packages (`internal/daemon` ~90s) single pass only。Tests starting real OS processes (`sleep`, `echo`, agnt binary) must NOT use `t.Parallel()`；`exec.CommandContext` PID-reuse race under high concurrency kills unrelated processes。

Test startup contract (`Start()` vs `NewForTest`)：`.claude/rules/daemon-architecture.md` § Test startup contract。

## Important Constraints

### MCP Protocol

- Tool names: `^[a-zA-Z0-9_-]{1,128}$`; transport stdio only (logs to stderr); all I/O needs JSON schema tags; errors as `CallToolResult{IsError: true}` (NOT Go errors).

### Process Management

- No timeout by default (`DefaultTimeout: 0`); 256KB output buffer per stream; graceful shutdown 5s SIGTERM → SIGKILL; aggressive SIGKILL when deadline <3s; health checks 10s.
- **Session pgid containment**：PTY child's pgid = session container。`CleanupSessionResources` 先殺 entire pgid（SIGTERM, 2s grace, SIGKILL），後觸 managed processes；捕 `npm run dev &` 等 backgrounded jobs。Startup scans orphaned pgids from daemon crashes。Accepted escapes: `setsid &`, double-fork, `systemd-run`, container runtimes。Full invariant + file ownership: `.claude/rules/daemon-architecture.md` § Session Containment.

### Reverse Proxy

- Default port hash-based (stable, 10000-60000); traffic log 1000 entries; request/response 10KB max in logs; `/__devtool_metrics` reserved; JS injection only `text/html`; auto-restart max 5/min.

### Platform Support

- **Linux/macOS**: `Setpgid: true`, SIGTERM/SIGKILL, `creack/pty`, SIGWINCH resize, PPID chain walking via `/proc/<pid>/stat`.
- **Windows**: ConPTY, Job Objects, `CTRL_BREAK_EVENT`, named pipes (`\\.\pipe\devtool-mcp-<username>`).
- **WSL**: GOOS=linux 但及 Windows-side processes。Use `platform.IsWSL()` / `ShouldUseWindowsShell(path)`, not bare `runtime.GOOS`. Full audit: `.claude/rules/wsl-audit.md`.

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
