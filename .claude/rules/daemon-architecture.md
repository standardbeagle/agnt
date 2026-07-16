# Daemon Architecture

## Personas

Four participants interact with daemon. Every feature must consider all four:

1. **Developer** — configures `.agnt.kdl`, runs `agnt run` or opens Claude Code session, expects dev servers and proxies to "just work." Needs "doctor" command for manual verification and cleanup.

2. **AI Agent** — calls MCP tools (`proc`, `proxy`, `proxylog`, `get_errors`, `get_incidents`), decides based on state. Needs verified-accurate state — stale or contradictory data makes agent take wrong actions, worse than no data.

3. **Daemon** — long-running background process outliving any session. Orchestrates lifecycles, manages event system, serves as state cache.

4. **Managed processes/proxies** — active participants, not passive resources. Emit errors, need restarts, can go rogue (zombie PIDs, orphaned ports). State must be verified against OS truth, not assumed from daemon memory.

## Data Ownership — Source of Truth

Daemon in-memory state is **cache**, never authority. Canonical source of truth per state:

| State | Source of truth | Verification method |
|-------|----------------|-------------------|
| Process alive/dead | OS (PID check) | Platform-specific PID probe |
| Port ownership | OS (socket table) | Platform-specific port scan |
| Proxy alive/responsive | Proxy instance (health probe) | TCP connect or HTTP GET |
| Script config / expected state | `.agnt.kdl` on disk | Parse and compare |
| URL associations | URLTracker cache | Verified against actual port binding |
| Script registry entries | Session lifecycle | Rebuilt from config on each session connect |
| Incident inbox contents | Originating subsystem | Inbox is a cache — re-fetch from source on reconciliation |
| Blob store payloads | In-memory LRU only | Best-effort; evicted on session end or cap overflow |
| Bus in-flight events | Transient channel only | Drop-newest on overflow; no replay |

**Rule**: Any mismatch between daemon cache and source of truth = daemon updates cache to match reality and emits event. Daemon never asserts cache correct over OS truth.

**Script registry ephemeral**: Rebuilt from `.agnt.kdl` on each session connect. When last session for project disconnects, `CleanupSessionResources` removes all registry entries. Next session starts fresh from current config. Never persist or carry registry state across sessions.

## Reconciliation Model

Three reconciliation triggers:

1. **On session connect** — daemon runs full health check before responding to first query. AI agent must get verified-accurate state.
2. **Periodic** — every 30s (configurable), daemon runs health check, emits events for state changes. Never kills processes — only updates state and surfaces issues.
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
| `agnt ssh` (remote session-host client) | Full support (`cmd/agnt/ssh.go`) | **Unsupported in v1 — loud, documented gap.** `cmd/agnt/ssh_windows.go` registers the same command so it is discoverable, but `RunE` returns an explicit error ("not yet supported on Windows... use WSL as a workaround") instead of silently missing or half-connecting. Blocked on native named-pipe local forwarding (daemon socket + port forwards); see task 06a / epic `01KWMARXTVWKC33EPHZZJ43JT9`, `docs/superpowers/specs/2026-07-03-remote-ssh-design.md` §7. | Works via the Linux client path (WSL is the documented workaround for Windows users) |

Existing `platform.IsWSL()` helper in `internal/platform/process_unix.go` (memoized `/proc/version` check for `microsoft`/`wsl`) is canonical WSL detection. New OS-level operations must consult it before using `runtime.GOOS == "linux"` to gate Linux-only behavior — WSL is GOOS=linux but routinely needs Windows-side processes via `tasklist.exe` / `netstat.exe` / `taskkill.exe` interop.

The `ShouldUseWindowsShell(path)` helper now exists (`internal/platform/process_unix.go:53`; Windows stub in `process_windows.go:85`). `ScriptConfig.ResolveShell()` (`internal/config/agnt.go:315`) consults it first: a WSL session with a Windows-path `run` or `cwd` resolves to `cmd.exe /c` so `.cmd`/`.bat` scripts run instead of silently picking `sh -c` and failing. Two `wsl-followup` sub-tasks (parent `5YgALr79bfhf`) remain deferred — `detectPortsForPID` has no `netstat.exe` branch and `internal/overlay/status.go:platformShell` still gates on raw `runtime.GOOS`. See `.claude/rules/wsl-audit.md` for the full audit.

### WSL Awareness — what's wired vs deferred

| Site | Status | File |
|------|--------|------|
| `platform.IsWSL()` detection | Wired | `internal/platform/process_unix.go:24` |
| `platform.ScanWindows()` (`tasklist.exe`) | Wired | `internal/platform/process_unix.go:172` |
| Duplicate scanner appends Windows procs | Wired | `internal/daemon/duplicate_scanner.go:175` |
| `FindPIDsByPort` falls back to `netstat.exe` | Wired (audit landed 2026-05-02) | `internal/config/portdetect_unix.go` |
| `ShouldUseWindowsShell(path)` helper | Wired | `internal/platform/process_unix.go:53` |
| `ResolveShell` picks `cmd.exe` for Windows-path scripts on WSL | Wired | `internal/config/agnt.go:315` |
| `ProcessNameByPID` / `ProcessNamesByPIDs` fall back to `tasklist.exe` | Wired | `internal/config/portdetect_unix.go:336,387` |
| Doctor command attributes Windows-side port owners by name | Wired (batched `tasklist.exe`) | `internal/daemon/doctor.go:209` |
| `taskkill.exe` to kill Windows-side rogue processes | Wired (`KillWindowsPID`; called by port preflight + shutdown) | `internal/platform/killwindowspid_unix.go:36` |
| `detectPortsForPID` falls back to `netstat.exe` | **Not yet** — `wsl-followup` sub-task | `internal/config/portdetect_unix.go` |
| `platformShell` uses `ShouldUseWindowsShell` | **Not yet** — `wsl-followup` sub-task | `internal/overlay/status.go:624` |

### Accepted WSL escape hatches

Intentional behaviors. Look like WSL bugs but aren't:

| Behavior | Why we accept it |
|----------|-----------------|
| `pidAlive` cannot probe Windows PIDs from WSL | We never register Windows PIDs in our process manager — show up only via `ScanWindows()`, read-only for us |
| `directChildren` returns nil for Windows-side parent PIDs | We don't track Windows process trees; descendant cleanup is for our managed processes |
| `normalizePath` is case-sensitive for `/mnt/c/...` paths in WSL | `/mnt/c/Users/Foo` and `/mnt/c/Users/foo` resolve to same NTFS file but Linux treats as distinct paths. Forcing lowercase would collapse two distinct sessions registered under different casings |
| `cleanupStaleFiles` PID file uses Linux layout under WSL | Daemon socket path owned by caller; running daemon under WSL means Linux paths end-to-end |
| Browser launcher doesn't have WSL branch | `BROWSER` env var is WSL-friendly contract; we don't bridge `open` ↔ `cmd.exe /c start` |
| `chromedp` session URL picker is darwin-only | Chrome process discovery only meaningful when we launch chrome ourselves; WSL users typically point at chrome on host |
| Unix daemon/control sockets live under WSL `$HOME` | The client and daemon path is Linux-side; native named-pipe forwarding is tracked by `01KXDMG7KG02MH91W2KXWHZAYA` |
| SSH config and `known_hosts` come from WSL `~/.ssh` | They belong to OpenSSH inside WSL; Windows-profile files are not an implicit second source of truth |
| `/mnt/c` drop watching uses `fsnotify` plus polling | Polling is the correctness fallback for unreliable DrvFS/9P notifications |
| `agnt attach` uses Unix raw mode in WSL | WSL supplies a Linux tty; native Windows ConPTY relay is tracked by `01KXDMGBMJB61WXA5YDHB8CY40` |
| Local `/mnt/c` sources pair with POSIX remote SFTP destinations | Each path is interpreted by the operating system that owns that side of the transfer |

## Port-Kill Guard

`ProcessManager.KillProcessByPort` (go-cli-server) re-discovers holders at fire time and kills them ALL — no exclusion list. Any self/managed filtering done on an earlier scan is void by kill time. **Every port-kill in daemon must route through `killPortHoldersGuarded`** (`internal/daemon/port_preflight.go`): re-scans immediately before kill, refuses to fire when the daemon itself or a managed PID holds the port, returns protected PIDs for loud surfacing. Call sites: startup port cleanup (`daemon_shutdown.go`), preflight cleanup (`startup_resilience.go`), `PROC CLEANUP-PORT` (`hub_proc.go`), `killPortBlockers` (`port_preflight.go`). Regression test: `TestKillPortHoldersGuarded_ProtectsSelf`.

## Silent Failure Prohibition

No subsystem may silently skip expected action. If config declares proxy, process, or dependency, system must either:
1. Successfully create/start it, OR
2. Emit visible error/warning event reaching AI agent and session log

`debug.Log` not sufficient for failures — only goes to debug file. Failures must propagate through event system or session log.

## Config Authority

If `.agnt.kdl` declares expected state (proxy with `fallback-port`, script with `depends-on`), system must honor it. Config fields parsed but not acted on are bugs.

## Scope token (`internal/scope`)

Cross-session delivery is gated by a `scope.Scope` value so that **global is the loud exception, session-scoping the default**. The zero `Scope` is invalid and matches nothing — callers must construct one explicitly, so "forgot to scope" cannot silently compile into a global.

| Constructor | Meaning | Audit |
|-------------|---------|-------|
| `scope.Project(path)` | matches one project (path normalized via `scope.NormalizePath`) | none |
| `scope.Unscoped(reason)` | matches every project | logs `UNSCOPED scope created reason=… caller=file:line` at construction |

Key APIs and rules:

- **`ProxyManager.ListScoped(scope)`** replaced the old unscoped `List()`. Removing `List()` is deliberate compile-time enforcement: every proxy enumeration must pass a scope.
- **`resolveScope(filter, connSessionCode)`** (`hub_helpers.go`) is the token form of `resolveProjectScope` — the single bridge from the legacy `(path, global)` chain to a `Scope`. A non-global call with no resolvable session fails loud; an explicit `global:true` becomes an audited `Unscoped`.
- **`overlayEndpointForProject(path)`** resolves a proxy's overlay socket from the session that owns its project, returning `""` (fail closed) when none is registered — the proxy is late-bound by `rebindProxyOverlays` when its session connects. There is **no** global overlay fallback.
- **`Daemon.SetOverlayEndpoint`** no longer pushes one endpoint onto every proxy. That daemon-wide blast was the cross-project leak (a message from project A's browser reaching project B's agent). Per-proxy binding is project-scoped only.
- **Allowlist test**: `internal/scope/audit_test.go` (`TestUnscopedCallSites`) pins the exact set of production `Unscoped(...)` call sites; a new one fails CI until reviewed and added.

## Tool session-scoping

**Canonical classification** of every hub query/list verb (and MCP tools driving it) against session-scope chokepoint. Project scoping is *structural* property, not per-handler convention: exactly one resolution point, `resolveProjectScope` (`internal/daemon/hub_helpers.go`), and every non-debug list/query routes through it. Adding new query/list verb without classifying it here — and wiring to gate if non-debug — is a bug.

### The gate contract (`resolveProjectScope`)

Given per-call `DirectoryFilter{Global, SessionCode, Directory}` and connection's bound session code, resolution order:

1. `Global == true` → `("", true, nil)`: no project filter (cross-project).
2. explicit `SessionCode` → that session's project path (error if unknown).
3. explicit `Directory` → normalized directory.
4. otherwise connection's bound session's project path.
5. none of above → `("", false, errNoSessionScope)`: **fail loud**.

Call that cannot resolve a project is rejected with `invalid_args: no session attached …` rather than silently leaking every project's data. After resolving the project, omitted `global` uses `scope.default-global` from `.agnt.kdl` (secure default `false`); explicit `global:true` or `global:false` wins in either direction. MCP daemon connection not session-bound, so MCP tools name project explicitly via `SessionCode` (preferred) or `Directory` (fallback) — see `collectProcessAlerts` / `handleProcList` for canonical client pattern.

### Uniform `global` override on MCP tools (C6)

Every gated MCP tool exposes the **same** optional `global *bool` override (`json:"global,omitempty"`, documented in jsonschema). The pointer is required to preserve three states: omitted uses project config, explicit `true` is cross-project, and explicit `false` forces project scope. MCP daemon connection not session-bound, so each tool names the project on wire whenever it is not explicitly global; this lets the daemon load that project's config. Reflection contract test (`internal/tools/global_scope_uniform_test.go`, `TestGatedMCPTools_ExposeGlobalFlagUniformly`) pins the six gated inputs.

Two tools intentionally do **not** take cross-project `global`, excluded from contract test:

- **`get_incidents`** — incident inbox per-session *hard-isolated* ("Cross-session isolation" numbered contract below). Stronger guarantee than project scoping; cross-session global would violate it.
- **`watch`** — emits `agnt monitor` command string. Monitor stream scoping is separate STREAM-EVENTS concern, not result-returning query, so `global` flag would be no-op (silent no-ops forbidden).

### Gated (must route through `resolveProjectScope`)

Default project-scoped, `global`-overridable, session-less non-global rejected.

| Verb | MCP tool | Filter field | Notes |
|------|----------|--------------|-------|
| `ALERTS QUERY` | `get_errors` (process alerts) | `AlertStoreFilter.ProjectPath` | C4 |
| `ALERTS STARTUP-LOG` | `get_errors` (startup errors), `daemon startup_log` | `StartupLogFilter.ProjectPath` (matched via `basename-hash:` ProcessID prefix — entries not stamped at ingest) | C5 |
| `PROC LIST` | `proc {action:"list"}` | `ProjectPath` compare on each process | C5 (migrated off inline logic) |
| `PROXY LIST` | `proxy {action:"list"}` | `ProjectPath` compare on each proxy | C5 (migrated off inline logic) |
| `TUNNEL LIST` | `tunnel {action:"list"}` | `tunnelm.ListByPath` | C5 (migrated off `getSessionProjectPath` fallback-to-all) |
| `SESSION LIST` | `session {action:"list"}` | `sessionRegistry.List(path, global)` | C5 |
| `SESSION TASKS` | `session {action:"tasks"}` | `scheduler.ListTasks(path, global)` | C5 |
| `INCIDENTS QUERY` | `get_incidents` | per-session inbox partition | pre-existing model gate converges toward |
| `PORTS QUERY` | overview ports panel (`fetchPorts`) | `resolveProjectScope` → declared-port set | classifies owners as managed/unmanaged/conflict; orphans uid-scoped, not project-scoped |

### ID-scoped (single resource addressed by explicit id)

Take resource id, not project filter. Id **lookup** resolves through `getSessionScoped` (`internal/daemon/hub_helpers.go`), restricting fuzzy matching to connection's session project so you cannot address another project's resource by id; exact id always works. No `global` flag — id is the scope.

| Verb(s) | Resolver |
|---------|----------|
| `PROC STATUS` / `OUTPUT` / `STOP` / `RESTART` | id → `ProcessManager` |
| `PROXY STATUS` / `STOP` / `RESTART` / `TOAST` | `getSessionScoped(…, GetWithPathFilter)` |
| `PROXYLOG QUERY` / `SUMMARY` / `CLEAR` / `STATS` | `getSessionScoped(…, GetWithPathFilter)` |
| `CURRENTPAGE LIST` / `GET` / `SUMMARY` / `CLEAR` | `getSessionScoped(…, GetWithPathFilter)` |
| `TUNNEL STOP` / `STATUS` | `getSessionScoped(…, GetWithPathFilter)` |

### Debug-exempt (by design)

Agent supplies explicit `proxy_id` it already holds; these interactive browser-debug surfaces, not cross-project discovery. **Not** project-filtered, but `proxy_id` **lookup** still resolves through `getSessionScoped`, so debug call cannot reach another project's proxy by id.

| Tool | Verb |
|------|------|
| `proxy {action:"exec"}` | `PROXY EXEC` |
| `responsive_audit` | `PROXY EXEC` (script injection) |
| `snapshot` | `PROXY EXEC` |
| `screenshot` | `PROXY EXEC` |
| sketch / design modes | `PROXY EXEC` / panel |
| `channel_reply` | `PROXY TOAST` |

### Why STARTUP-LOG is prefix-matched, not ingest-tagged

`StartupLogEntry` has 59 ingest sites across daemon; stamping project path at each would be invasive and error-prone. Instead entry's `ProcessID` (`makeProcessID(projectPath, name)` → `basename-hash:name`) deterministically encodes project, so scoped query filters by `basename-hash:` prefix (`makeProcessID(projectPath, "")`). Consequence: daemon-wide events with bare (non-project) `ProcessID` — shutdown/scan records — visible only to `global` query, never to scoped one. Intended trade-off.

## Incident Pipeline

Incident pipeline (`internal/incident/`) is the always-active agent alert path, providing a normalised, deduped, priority-ordered inbox. `alerts.push` selects delivery sinks; the deprecated `alerts.incident-pipeline` key is parse-only compatibility.

### Source of Truth

| State | Source of truth | Notes |
|-------|----------------|-------|
| Inbox entries | Originating subsystem | Inbox is a cache; entry present does not mean event still active |
| Bus in-flight events | Transient MPSC channel | Drop-newest on overflow (`bus.go`, 4096-cap). No replay path. |
| Blob store payloads | In-memory LRU per session | Best-effort: evicted when session ends or 16MB cap reached. Never persisted to disk. |
| Dedup fingerprints | Deduplicator in-process state | Cleared on session teardown; cross-session dedup does not apply |

### Numbered Contracts

1. **Cross-session isolation.** Each connected session gets its own `sessionPipeline` instance. Events from session A never appear in session B's inbox, even for same project.

2. **Drop-newest on bus overflow.** MPSC bus drops incoming event (not oldest) when 4096-slot channel full. Keeps latency bounded at cost of losing most recent event under extreme load. Overflow count surfaced via `bus.OverflowCount()`.

3. **Dedup scope per-session, not per-project.** Fingerprint collision in session A does not suppress same event in session B. Deduplicator state owned by `sessionPipeline`, torn down with it.

4. **Coalescer batch window non-configurable at runtime.** Coalesce window (default 200ms) set at `sessionPipeline` construction time from config. Live reconfiguration not supported; daemon restart required to change.

5. **Inbox capacity hard-capped per band.** Each of four priority bands (critical / error / warning / info) holds at most 100 entries. Oldest entries evicted to make room for new arrivals. AI agent must poll with returned cursor to drain inbox before it wraps.

6. **Blob store is per-session and best-effort.** Production adapters retain oversized bytes until `MPSCBus` spills them into the destination session's bounded store; `detail:"full"` hydrates only from that same store. `BlobRef` may resolve to `nil` after eviction or session teardown; callers fall back to `Summary`, never another session's store.

7. **Pinger never blocks delivery.** Pinger sends compact pings to MCP, channel, and PTY sinks using non-blocking channel sends. Slow consumer does not delay other consumers or block Inbox drain loop.

8. **Push policy is project-isolated and live.** Effective `alerts.push` policy is keyed by normalized project path and resolved from the session on every ping. Updating one project never changes another project's sinks and never replaces its inbox pipeline.

### File Ownership

| Component | File |
|-----------|------|
| `IncidentEvent`, `BlobRef`, `BlobStore` | `internal/incident/envelope.go` |
| Signal source adapters (11 sources) | `internal/incident/adapter_*.go` |
| `Deduplicator`, `Coalescer`, `FlowController` | `internal/incident/dedup.go` |
| `Inbox` (4 bands, cursor pull, subscribe) | `internal/incident/inbox.go` |
| `Pinger` (subscribe → fan-out pings) | `internal/incident/ping.go` |
| `get_incidents` MCP tool | `internal/tools/get_incidents.go` |
| Remediation routing table | `internal/incident/remediation.go` |
| MPSC bus + `sessionPipeline` | `internal/incident/bus.go` |
| `INCIDENTS QUERY` hub handler, session lifecycle wiring | `internal/daemon/hub_incidents.go` |

## Session Containment

Session owns more than processes explicitly registered with `proc run`. AI agent behind `agnt run` session routinely spawns background work through non-interactive bash — `npm run dev &`, `cargo watch &`, `python manage.py runserver &` — and non-interactive bash does not enable job control. So those jobs inherit PTY child's process group instead of getting own, and daemon has no explicit handle on them. Without containment, session B cannot claim ports session A's backgrounded jobs still hold.

### The Session pgid Invariant

PTY child started by `agnt run` gets own POSIX session via `setsid` (creack/pty does this). Its PID doubles as session pgid, and every descendant process — interactive shells, tool invocations, backgrounded jobs spawned via `sh -c 'cmd &'` — inherits that pgid unless descendant explicitly escapes (see below).

Daemon holds this invariant through three primitives:

1. **Wire-through at registration.** `agnt run` client captures PTY child PID, passes to daemon as `SessionPGID` during `SessionRegister`. Field survives client → protocol → hub handler → registry round trip.
2. **Kill on cleanup.** `CleanupSessionResources` → `doCleanup` calls `killSessionPGID` **before** touching managed processes. Sends SIGTERM to group, waits 2s grace window, escalates to SIGKILL on any survivors. Self-exclusion protects daemon's own PID if it ever (defensively) shares group.
3. **Startup orphan scan.** On `Start()`, daemon walks `/proc` looking for pgids whose leader is dead but members still alive — "daemon crashed mid-session" case — and reaps via same kill primitive. UID-filtered, gated on `session.orphan-pgid-scan` config (default on).

### What Is Caught

| Scenario | Caught? | By which primitive |
|----------|---------|--------------------|
| `npm run dev &` in non-interactive bash | yes | session pgid kill on cleanup |
| `nohup cmd &` (SIGHUP blocked) | yes | pgid kill uses SIGTERM/SIGKILL, not SIGHUP |
| `disown %1` after backgrounding | yes | `disown` affects shell's job table, not pgid |
| Managed `proc run` scripts | yes | ProcessManager path; redundant with pgid kill |
| Leaked pgid after daemon crash | yes | startup orphan `/proc` scan |
| Grandchildren of backgrounded jobs | yes | they inherit pgid transitively |

### Accepted Escape Hatches

These **intentionally** escape session pgid. Represent conscious "I want to survive session shutdown" decision and daemon must not try to track them:

| Escape | Why it escapes | Operator responsibility |
|--------|----------------|------------------------|
| `setsid cmd &` | Creates new session + pgid at exec time | User explicitly asked for detached process; they own cleanup |
| Double-fork daemon (fork → setsid → fork → exit) | Classic Unix daemonization | Same — explicit "become a daemon" pattern |
| `systemd-run --scope`, `systemd-run --user` | Hands process to systemd's cgroup | systemd owns lifetime |
| Container runtimes (`docker run -d`, `podman run -d`) | Container PID1 is runtime, not session | Runtime owns cleanup |
| Processes that re-exec into different uid | `/proc` scan filters by uid | Outside our blast radius |

Each leaves port or resource held after session shutdown, but that's operator's explicit choice. Repro test `TestSessionContainment_SetsidEscapes` asserts `setsid` escapes containment — regression would accidentally reap detached processes, worse than leaking them.

### Session-host: a second, explicit-kill-only flavor

`SESSION-HOST CREATE` (see `docs/superpowers/specs/2026-07-03-remote-ssh-design.md` §1-2 and `internal/sessionhost/`) inverts PTY ownership: the daemon spawns and owns the PTY child directly, instead of a client (`agnt run`) reporting a pgid it captured itself. This is a **second `SessionKind`** (`internal/daemon/session.go`), sharing the same `Session` struct and `SessionRegistry` as classic sessions (so `hasOtherSessions`, `FindByDirectory`, and project-scoping consumers need no changes — see the struct-level decision in the spec §2.3), but with a materially different containment lifecycle:

| Concern | Classic (`Kind == "classic"`) | Session-host (`Kind == "session-host"`) |
|---|---|---|
| Who reads `SessionPGID` | Daemon trusts a value reported over the wire by the client | Daemon reads it directly from its own `pty.Start()` call — no wire hop, no possibility of a malicious/buggy client reporting a wrong PID |
| What triggers `killSessionPGID` | Client disconnect (socket drop) → `CleanupSessionResourcesDeferred` → `doCleanup`, after a grace period | **Only** `SESSION-HOST KILL` (`internal/daemon/hub_sessionhost.go`) — an attach-stream disconnect, or an explicit `SESSION-HOST DETACH`, never calls `doCleanup` |
| `doCleanup` behavior | Runs the full teardown (pgid kill, script/proxy cleanup, registry unregister) | **Guarded no-op**: `doCleanup` checks `session.Kind == SessionKindSessionHost` first and returns immediately, logging that the session is explicit-kill-only. This is belt-and-braces — nothing should route a session-host session's code into `doCleanup` in the first place, because... |
| Why the guard is (normally) never hit | N/A | `SESSION-HOST ATTACH` never calls `conn.SetSessionCode()` on the attaching connection, so a dropped attach connection never triggers the hub's session-cleanup callback at all. The guard exists as a second line of defense, not the primary mechanism. |
| Daemon restart | N/A (client-owned PTY, unaffected by daemon restart) | No re-attach path to a PTY fd the daemon no longer holds a handle to — the orphaned child becomes exactly the "dead-leader pgid" shape `startupOrphanPGIDScan` already exists to catch. Swept the same way as a crashed classic session, not specially. |

**Numbered invariant**: a session-host session's PTY child pgid is reaped in exactly three cases — (1) explicit `SESSION-HOST KILL`, (2) the PTY child exiting on its own (observed via `sessionhost.Session.waitLoop`, which flips `Status` to `StatusExited`; no pgid action needed since the process tree is already gone), (3) daemon shutdown/restart via the existing startup orphan-pgid scan. Attach-stream disconnect or explicit `SESSION-HOST DETACH` is **never** one of these cases — that is the entire value proposition of session-host (survive client disconnect). No idle-timeout auto-kill exists in v1.

**Remote-SSH reconnect invariant**: the SSH transport and local forwards are disposable; the daemon-owned session-host is durable. Reconnect rebuilds daemon-socket and proxy forwards from authoritative remote state, then attaches to the same session. A missing session fails unless `--create-if-missing` or `--new` explicitly permits replacement. Initial creation and reconnect checks use `SESSION-HOST LIST/CREATE`; never bake unsupported lifecycle flags into the remote `agnt attach` command.

**Session-host liveness and scrollback source of truth**: liveness is the in-process `sessionhost.Session` atomic `Status`. `waitLoop` waits for the daemon's PTY child, records its exit metadata, and stores `StatusExited`; LIST reports that state. The current implementation has no separate OS liveness probe. Output history is the session's in-memory `goprocess.RingBuffer` (`DefaultScrollback`, 1 MiB): `readLoop` writes PTY output before fan-out, and attach snapshots/replays the ring before live frames. It is bounded and daemon-memory-only, not persistent storage; daemon restart loses it along with the PTY handle, while startup orphan cleanup handles the remaining process group.

### File Ownership

| Primitive | File |
|-----------|------|
| `KillSessionPGID`, `MembersOfPGID`, `readPGID` | `internal/platform/sessionpgid_unix.go` |
| `ScanOrphanPGIDs` (dead-leader scan) | `internal/platform/orphanpgid_unix.go` |
| `killSessionPGID` wiring + `doCleanup` ordering (incl. the `SessionKindSessionHost` guard) | `internal/daemon/daemon_session_cleanup.go` |
| `startupOrphanPGIDScan` + config gate | `internal/daemon/daemon_orphan_pgid.go` |
| PTY child PID capture + wire-through (classic) | `cmd/agnt/pty_common.go`, `internal/daemon/client.go` (`SessionRegisterWithPGID`) |
| Session struct field (`SessionPGID`, `Kind`) | `internal/daemon/session.go` |
| Primitive-level regression tests | `internal/daemon/daemon_session_pgid_test.go`, `internal/daemon/daemon_orphan_pgid_test.go` |
| End-to-end port-reuse repro | `internal/daemon/daemon_session_containment_test.go` |
| Daemon-owned PTY child, scrollback ring, attach fan-out (session-host) | `internal/sessionhost/sessionhost.go` |
| `SESSION-HOST` verb handlers (CREATE/LIST/KILL/ATTACH/DETACH/RESIZE/STDIN) | `internal/daemon/hub_sessionhost.go` |
| Session-host containment + attach/detach race tests | `internal/sessionhost/sessionhost_test.go`, `internal/daemon/hub_sessionhost_test.go` |

### Cross-Platform Note

The 2026-07-13 remote-SSH sweep is recorded in `.claude/rules/wsl-audit.md`: WSL intentionally selects the Unix SSH, raw-terminal, Unix-socket, and session-host paths. `/mnt/c` drop watching has a polling fallback; SSH config and `known_hosts` come from WSL `$HOME`. Native-Windows `agnt ssh` (named-pipe forwarding) and `agnt attach` (ConPTY relay) are loud deferred gaps, not partial implementations.

Session pgid primitives Unix-only (`//go:build !windows`). On Windows, Job Objects already provide equivalent cascade-kill semantics for PTY child tree, and `SessionPGID` is always 0 — `killSessionPGID` is no-op guarded by `pgid <= 1`. Startup orphan scan has three implementations selected by build tag:

| Platform | File | Mechanism |
|----------|------|-----------|
| Linux | `internal/platform/orphanpgid_unix.go` (`//go:build linux`) | `/proc` walk via `Scan()` + `readPGID` from `/proc/<pid>/stat` |
| macOS | `internal/platform/orphanpgid_darwin.go` (`//go:build darwin`) | `sysctl` `KERN_PROC_ALL` via `unix.SysctlKinfoProcSlice` — atomic snapshot with pid/pgid/ppid/uid in one syscall |
| Other Unix (FreeBSD, OpenBSD, etc.) | `internal/platform/orphanpgid_other.go` (`//go:build !windows && !linux && !darwin`) | Stubs return nil — no orphan detection but no false reaping either |

Pure orphan-classification logic shared via `internal/platform/orphanpgid_classify.go` (`//go:build !windows`) and exhaustively tested in `orphanpgid_classify_test.go` so darwin code path verifiable on Linux CI without macOS host. macOS-side verification beyond cross-compile (`GOOS=darwin go build ./...`) requires real darwin runtime to exercise sysctl source; procisolation-tagged test file (`orphanpgid_unix_test.go`) is `//go:build linux && procisolation` because it exercises host-global `/proc` and `kill(2)` directly and depends on Linux PID namespaces (`unshare`) for safe execution.

## Test startup contract

Tests almost never want heavyweight production startup — walking `/proc`, issuing `kill(2)` to whatever PID currently owns a port, replaying persisted proxy state, or spinning up 24-hour update-check ticker. Running any inside unit test either slows suite (hundreds of ms per construction × thousands of daemon instances) or, worse, reaps unrelated host processes owned by same uid.

Daemon solves with two-entry-point split. Both entry points share same `bootstrap()` helper, so test path cannot drift from production:

| Step | `Start()` (production) | `NewForTest(t, cfg)` |
|------|------------------------|----------------------|
| `setupDebugLogging` — rotated log file at `GetLogPath()` | runs | **skipped** |
| `bootstrap()` — `registerCommands` + `SetSessionCleanup` + `hub.Start` + `scheduler.Start` + `urlTracker.Start` + `handleProxyEvents` goroutine + `drainHooks` goroutine | runs | runs |
| `cleanupOrphans` — walks `FilePIDTracker`, kills stale PIDs | runs | **skipped** |
| `startupPortCleanup` — `FindPIDsByPort` + `KillProcessByPort` for every persisted proxy port | runs | **skipped** |
| `startupOrphanPGIDScan("")` — `/proc` walk for orphan pgids | runs (gated by `OrphanScanEnabled`) | **skipped** (belt-and-braces; `OrphanScanEnabled` already defaults to false) |
| `restoreProxies` — replay persisted `ProxyConfig`s into fresh `proxym` | runs | **skipped** |
| `updateChecker.Start` — 24h GitHub poll goroutine | runs (gated by `EnableUpdateCheck`) | **skipped** |
| `t.Cleanup(Stop)` registration | n/a | **registered** — 5s timeout |

### What the split guarantees

- **Production `Start()` byte-for-byte unchanged** — original sequence preserved via shared `bootstrap()` helper. Reviewers who "fix" apparent omission in `NewForTest` by pulling production-only step back in trip `TestNewForTest_StartsUnder100ms` assertion (100ms budget, ~20ms observed on laptop).
- **No build tag needed.** `*testing.T` parameter is fence — production code cannot construct `*testing.T`, so `NewForTest` unreachable from any non-test caller. Compilation cost of pulling in `testing` package negligible for `agnt` binary.
- **`daemontest.New` routes through `NewForTest`**, so every test adopting factory (iter 28, commit `dfcd2a4`) automatically gets fast startup path. Tests specifically exercising `cleanupOrphans`, `startupPortCleanup`, `restoreProxies`, or `startupOrphanPGIDScan` continue calling those methods directly — none private to `Start()`.

### File ownership

| Primitive | File |
|-----------|------|
| `bootstrap()` (shared wiring) | `internal/daemon/daemon.go` |
| `Start()` (production path — bootstrap + heavy ops) | `internal/daemon/daemon.go` |
| `NewForTest(t, cfg)` (test entry point) | `internal/daemon/test_helpers.go` |
| `daemontest.New` (ephemeral socket + opts + cleanup) | `internal/daemontest/factory.go` |
| Timing + hub-accept assertions | `internal/daemon/test_helpers_test.go` |
