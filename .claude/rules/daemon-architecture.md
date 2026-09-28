---
paths:
  - "internal/daemon/**"
  - "internal/tools/**"
  - "internal/incident/**"
  - "internal/scope/**"
  - "internal/sessionhost/**"
  - "internal/protocol/**"
---

# Daemon Architecture

## Personas

Every feature must serve four participants:

1. **Developer** — writes `.agnt.kdl`, runs `agnt run`, expects things to just work; needs `doctor` for manual verify/cleanup.
2. **AI Agent** — calls MCP tools and acts on the state they report. Stale or contradictory state is worse than none.
3. **Daemon** — long-lived; orchestrates lifecycles, runs the event system, caches state.
4. **Managed processes/proxies** — active participants that emit errors, need restarts and go rogue (zombie PIDs, orphaned ports). Verify them against the OS, never assume.

## Data Ownership — Source of Truth

Daemon memory is a **cache**, never the authority.

| State | Source of truth | Verification |
|-------|----------------|--------------|
| Process alive/dead | OS | platform PID probe |
| Port ownership | OS socket table | platform port scan |
| Proxy alive | proxy instance | TCP connect / HTTP GET |
| Expected scripts/proxies | `.agnt.kdl` on disk | parse and compare |
| URL associations | URLTracker cache | checked against real port binding |
| Script registry | session lifecycle | rebuilt from config on each session connect |
| Incident inbox | originating subsystem | inbox is a cache |
| Blob store payloads | in-memory LRU | best-effort; evicted on session end / cap |
| Bus in-flight events | transient channel | drop-newest on overflow; no replay |

On mismatch the daemon updates its cache to match reality and emits an event. It never asserts its cache over OS truth.

Script registry is ephemeral: rebuilt from `.agnt.kdl` on connect, emptied by `CleanupSessionResources` when the project's last session leaves. Never persist it.

## Reconciliation Model

1. **Session connect** — full health check before answering the first query.
2. **Periodic** (30s default) — updates state and emits events; never kills.
3. **Doctor** — developer-initiated full reconciliation (MCP + overlay), returns a report with offered actions.

## Control-Plane Responsiveness

Registered hub handlers send out-of-band `STATUS` frames every 5s until their terminal response. Clients treat STATUS as idle-deadline progress, never as payload. STATUS writes are non-blocking (a busy write drops the tick). Each connection has its own dispatch goroutine, so a long request never delays `INFO`, `PING`, hooks, or other connections. The terminal response closes the status window atomically; no STATUS may follow it.

## Cross-Platform Mandate

Every OS-level operation goes through `internal/platform/` and handles Linux/macOS, Windows, and WSL:

| Operation | Linux/macOS | Windows | WSL |
|-----------|------------|---------|-----|
| PID alive | `kill(pid,0)` / `/proc` | `OpenProcess` / Job Objects | Linux path; Windows PIDs are read-only to us |
| Port owner | `/proc/net/tcp` / `lsof` | `netstat -ano` | Linux scan, `netstat.exe` fallback when empty |
| Group kill | SIGTERM → SIGKILL to pgid | `CTRL_BREAK_EVENT` → `TerminateJobObject` | `taskkill.exe` for Windows-side PIDs |
| Proxy probe | TCP / HTTP | same | same |

WSL is `GOOS=linux`: consult `platform.IsWSL()` / `platform.ShouldUseWindowsShell(path)` before gating on `runtime.GOOS`. `agnt ssh` and `agnt attach` are unsupported on native Windows and register loud stubs; WSL is the workaround.

WSL wired/deferred status and accepted escape hatches: `.claude/rules/wsl-audit.md`.

## Port-Kill Guard

`ProcessManager.KillProcessByPort` (go-cli-server) re-discovers holders at fire time and kills all of them — any earlier self/managed filtering is void. **Every daemon port-kill routes through `killPortHoldersGuarded`** (`port_preflight.go`): re-scan right before the kill, refuse when the daemon or a managed PID holds the port, return protected PIDs for loud reporting. Callers: `daemon_shutdown.go`, `startup_resilience.go`, `hub_proc.go` (`PROC CLEANUP-PORT`), `killPortBlockers`. Regression: `TestKillPortHoldersGuarded_ProtectsSelf`.

## Silent Failure Prohibition

A declared proxy, process, or dependency must either start, or emit a visible warning that reaches the agent and session log (event system / startup log). `debug.Log` alone is not enough — it only reaches the debug file.

## Config Authority

If `.agnt.kdl` declares it (`fallback-port`, `depends-on`, …) the system honours it. A field parsed but never acted on is a bug. See `config-contracts.md`.

## Scope token (`internal/scope`)

Cross-session delivery is gated by `scope.Scope`. The zero value is invalid and matches nothing, so "forgot to scope" cannot compile into global.

- `scope.Project(path)` — one project (path normalized).
- `scope.Unscoped(reason)` — every project; logs `UNSCOPED … caller=file:line`.
- `ProxyManager.ListScoped(scope)` replaced `List()` on purpose — every proxy enumeration must pass a scope.
- `resolveScope(filter, connSessionCode)` (`hub_helpers.go`) bridges the `(path, global)` chain to a `Scope`; non-global with no session fails loud, `global:true` becomes an audited `Unscoped`.
- `overlayEndpointForProject(path)` resolves a proxy's overlay socket from the owning session, `""` when none (fail closed; `rebindProxyOverlays` late-binds). No global overlay fallback — `SetOverlayEndpoint` no longer broadcasts (that was the cross-project leak).
- `internal/scope/audit_test.go` (`TestUnscopedCallSites`) pins every production `Unscoped(...)` site; a new one fails CI until reviewed.

## Tool session-scoping

Project scoping is structural: one chokepoint, `resolveProjectScope` (`hub_helpers.go`), and every non-debug list/query goes through it. A new query/list verb must be classified here and, if non-debug, wired to the gate.

### Gate contract (`resolveProjectScope`)

Given `DirectoryFilter{Global, SessionCode, Directory}` and the connection's session:

1. `Global` → no filter.
2. explicit `SessionCode` → that session's project (error if unknown).
3. explicit `Directory` → normalized dir.
4. else the connection's bound session's project.
5. else `errNoSessionScope`.

Handlers never write `errNoSessionScope` verbatim: `writeScopeErr` / `writeScopeErrHint` (`hub_scope_candidates.go`) reply with the active sessions to re-issue against (metadata only — code, path, command). A bare error only when no session exists at all. Omitted `global` falls back to `scope.default-global` in `.agnt.kdl` (default false); explicit true/false wins. The MCP connection is not session-bound, so MCP tools name the project via `SessionCode` (preferred) or `Directory`.

### Uniform `global` override (C6)

Every gated MCP tool exposes `global *bool` (`json:"global,omitempty"`) — pointer for three states (omitted / true / false). Pinned by `TestGatedMCPTools_ExposeGlobalFlagUniformly` (`internal/tools/global_scope_uniform_test.go`). Excluded by design:

- **`get_incidents`** — inboxes are hard-isolated per session (Incident Pipeline contract 1), so no cross-project `global`. It takes a `session` selector to choose which of the caller's inboxes to read; a session-less query returns candidates. Retention writes (pin/unpin/clear) take no selector and need an attached session.
- **`watch`** — returns an `agnt monitor` command; a `global` flag would be a silent no-op.

### Gated

| Verb | MCP tool | Filter |
|------|----------|--------|
| `ALERTS QUERY` | `proc snapshot` (alerts) | `AlertStoreFilter.ProjectPath` |
| `ALERTS STARTUP-LOG` | `proc snapshot`, `daemon startup_log` | `basename-hash:` ProcessID prefix |
| `PROC LIST` / `PROXY LIST` | `proc list` / `proxy list` | per-item `ProjectPath` |
| `TUNNEL LIST` | `tunnel list` | `tunnelm.ListByPath` |
| `SESSION LIST` / `SESSION TASKS` | `session list` / `tasks` | `(path, global)` |
| `INCIDENTS QUERY` | `get_incidents` | per-session inbox |
| `PORTS QUERY` | overview ports panel | declared-port set; orphans uid-scoped |
| `PORTS CLEAN-ORPHANS` | `kill-orphans` palette cmd | fail loud if unresolved; per-candidate `pgidOwnershipCheck` (cmdline+cwd evidence; shared uid alone is never enough) |

### ID-scoped

Take an id, not a filter; lookup goes through `getSessionScoped` so fuzzy matching stays inside the caller's project (exact ids always work). No `global`.
`PROC STATUS/OUTPUT/STOP/RESTART`, `PROXY STATUS/STOP/RESTART/TOAST`, `PROXYLOG *`, `CURRENTPAGE *`, `TUNNEL STOP/STATUS`.

### Debug-exempt

Browser-debug surfaces with an explicit `proxy_id` (`proxy exec`, `responsive_audit`, `snapshot`, `screenshot`, sketch/design, `channel_reply`). Not project-filtered, but the id lookup still uses `getSessionScoped`.

### Client-side project-scoped

`detect`, `demo list/inspect` read the resolved project tree directly (no hub verb). `demo record/assemble` run via `PROC RUN` with explicit `ProjectPath` (`AutoRestart:false`).

### Why STARTUP-LOG is prefix-matched

~60 ingest sites; instead of stamping each, the ProcessID (`makeProcessID` → `basename-hash:name`) encodes the project. Daemon-wide entries with bare IDs are visible only to `global` queries — intended.

## Incident Pipeline

`internal/incident/` is the always-on agent alert path: `Signal sources → Bus → Dedup/Coalesce/FlowControl → Inbox → Pinger → MCP/channel/PTY`. `alerts.push` picks sinks; `alerts.incident-pipeline` is parse-only.

Source of truth: inbox = cache of the originating subsystem; bus = transient (drop-newest at 4096, no replay); blob store = per-session in-memory LRU, 16MB, never persisted; dedup fingerprints = per-session, cleared on teardown.

### Numbered Contracts

1. **Cross-session isolation.** Each session has its own `sessionPipeline`; session A's events never reach B's inbox, even same project. Pins live and die with the session inbox (`TestBus_PinDiesWithSession`).
2. **Drop-newest on bus overflow** (4096 slots). Count via `bus.OverflowCount()`.
3. **Dedup is per-session**, not per-project.
4. **Coalesce window fixed at construction** (default 200ms); change needs daemon restart.
5. **Inbox hard-capped per band** — critical/error/warning/info, 100 each, oldest evicted. Agents drain with the cursor.
6. **Blob store per-session, best-effort.** Oversized bytes spill into the destination session's store; `detail:"full"` hydrates only from that store. A `BlobRef` may resolve nil — fall back to `Summary`, never another session's store.
7. **Pinger never blocks delivery** — non-blocking sends to every sink.
8. **Push policy is project-isolated and live** — keyed by normalized project path, resolved per ping; one project's update never touches another's sinks or inbox.

Files: `envelope.go`, `adapter_*.go`, `dedup.go`, `inbox.go`, `ping.go`, `remediation.go`, `bus.go` (all `internal/incident/`); `internal/tools/get_incidents.go`; `internal/daemon/hub_incidents.go`.

## Session Containment

Agents background work via non-interactive bash (`npm run dev &`), which has no job control, so those jobs inherit the PTY child's pgid. Containment lets session B reclaim ports A's jobs held.

### The Session pgid Invariant

The `agnt run` PTY child gets its own session (`setsid`); its PID is the session pgid, inherited by every descendant unless it escapes.

1. **Wire-through** — client passes the PTY child PID as `SessionPGID` on `SessionRegister`.
2. **Kill on cleanup** — explicit unregister/shutdown → `CleanupSessionResources` → `doCleanup` → `killSessionPGID` **before** managed processes (SIGTERM, 2s, SIGKILL; self-excluded). A dropped control connection is not cleanup authority: deferred cleanup reaps the group only after the owning `agnt run` PID is confirmed gone; unknown ownership fails safe.
3. **Startup orphan scan** — `Start()` reaps dead-leader pgids (uid-filtered, `session.orphan-pgid-scan`, default on).

Caught: `cmd &`, `nohup`, `disown`, managed scripts, grandchildren, pgids leaked by a daemon crash.

### Exact-identity retirement & per-code lifecycle gate

Teardown retires an exact `*Session`, under a refcounted per-code gate (`sessionLifecycleGates`, `daemon_session_cleanup.go`) that serializes every register/retire on a code. Reconnect installs a fresh pointer via `ReplaceExact`; a stale deferred cleanup pointer-compares in `doCleanupExact` and no-ops (`UnregisterExact` CAS-guards the delete). Session-host ids and classic codes come from independent counters and can collide (`claude-3`), so `hubHandleSessionRegister` rejects a classic register onto a session-host entry with `invalid_args`.

Accepted: a same-code reconnect during teardown waits up to ~12s on the gate — releasing early would let a fresh autostart race the old teardown.

### Accepted Escape Hatches

Intentional; do not track: `setsid cmd &`, double-fork daemons, `systemd-run`, container runtimes, uid changes. `TestSessionContainment_SetsidEscapes` guards this — reaping detached processes is worse than leaking them.

### Session-host: a second, explicit-kill-only flavor

`SESSION-HOST CREATE` (`internal/sessionhost/`, `hub_sessionhost.go`): the daemon owns the PTY. Same `Session` struct/registry, `Kind == session-host`.

| | Classic | Session-host |
|---|---|---|
| `SessionPGID` from | client over the wire | daemon's own `pty.Start()` |
| pgid kill trigger | disconnect → deferred cleanup | only `SESSION-HOST KILL` |
| `doCleanup` | full teardown | guarded no-op (belt-and-braces; ATTACH never calls `SetSessionCode`) |
| daemon restart | unaffected | PTY handle lost; child swept by orphan scan |

**Invariant**: a session-host pgid is reaped only on (1) `SESSION-HOST KILL`, (2) the child exiting itself (`waitLoop` → `StatusExited`), (3) daemon restart via orphan scan. Detach/disconnect never. No idle auto-kill.

**Remote-SSH reconnect**: transport and forwards are disposable, the session-host is durable. Reconnect rebuilds forwards from remote state and re-attaches; a missing session fails unless `--create-if-missing` / `--new`. Use `SESSION-HOST LIST/CREATE`, never lifecycle flags baked into a remote `agnt attach` command line.

**Liveness/scrollback**: liveness = in-process atomic `Status` (no separate OS probe). Scrollback = in-memory 1 MiB ring written before fan-out, replayed on attach; lost on daemon restart.

### Files

| Primitive | File |
|-----------|------|
| `KillSessionPGID`, `MembersOfPGID` | `internal/platform/sessionpgid_unix.go` |
| `ScanOrphanPGIDs` | `internal/platform/orphanpgid_{unix,darwin,other}.go`, shared `orphanpgid_classify.go` |
| cleanup ordering, lifecycle gate, `doCleanupExact` | `internal/daemon/daemon_session_cleanup.go` |
| `ReplaceExact` / `UnregisterExact`, `SessionPGID`, `Kind` | `internal/daemon/session.go` |
| orphan scan + config gate | `internal/daemon/daemon_orphan_pgid.go` |
| classic PGID capture | `cmd/agnt/pty_common.go`, `internal/daemon/client.go` |
| tests | `daemon_session_pgid_test.go`, `daemon_orphan_pgid_test.go`, `daemon_session_containment_test.go`, `hub_sessionhost_test.go`, `sessionhost_test.go` |

### Cross-Platform Note

pgid primitives are `!windows`; on Windows Job Objects cascade and `SessionPGID` is 0 (`killSessionPGID` no-ops on `pgid <= 1`). Orphan scan: Linux `/proc`, macOS `sysctl KERN_PROC_ALL`, other Unix stubs (no detection, no false reaping). `orphanpgid_unix_test.go` is `linux && procisolation` (runs under `make test-isolated`).

## Test startup contract

Tests use `NewForTest(t, cfg)` (`test_helpers.go`), never `Start()`. Both share `bootstrap()` (commands, hub, scheduler, URL tracker, proxy-event and hook goroutines), so the test path cannot drift. `NewForTest` skips: debug log file, `cleanupOrphans`, `startupPortCleanup`, `startupOrphanPGIDScan`, `restoreProxies`, update checker; registers `t.Cleanup(Stop)` (5s). Empty `SocketPath` defaults to a `t.TempDir()` socket.

- Pulling a production-only step into `NewForTest` trips `TestNewForTest_StartsUnder100ms`.
- The `*testing.T` parameter is the fence — no build tag needed.
- Tests for the skipped steps call those methods directly.
- `DaemonConfig.OrphanScanEnabled` is an internal knob (zero value false, production sets true) — never expose it in `.agnt.kdl`. The user-facing opt-out is `session.orphan-pgid-scan`.
