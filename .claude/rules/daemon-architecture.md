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

What exists today:

1. **Session connect** — `reconcileScriptStates` (`daemon_reconcile.go`) runs on `SESSION REGISTER`, before autostart: a script entry in Running/Starting whose managed process is missing or whose PID is dead moves to Stopped and emits `ScriptStopped`. Scripts only; proxies and ports are not probed here.
2. **On exit** — `watchProcessExit` records every managed process death as it happens (exit info, lifecycle alert, incident).
3. **Heartbeat sweep** — `runHeartbeatSweep` (`daemon.go`) marks a classic session Disconnected once its heartbeats are older than the registry timeout (60s; clients beat every 30s). It only flips status and drops the investigation record; it never tears a session down, and a later heartbeat revives it. Session-host sessions are exempt.
4. **Doctor** — developer-initiated full reconciliation (MCP + overlay), returns a report with offered actions.
5. **Config reconcile** — `AUTOSTART RECONCILE` (`reconcile_apply.go`) applies an edited `.agnt.kdl` to running scripts and proxies.

There is no periodic process/proxy/port reconciliation loop. A proxy that dies without its script exiting, or a port that changes hands, is noticed only by doctor or the next session connect. `DuplicateScanner` has a 30s loop that `Start()` deliberately never launches (it raced managed child trees).

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

`ProcessManager.KillProcessByPort` (go-cli-server) re-discovers holders at fire time and kills all of them — any earlier self/managed filtering is void. **Every daemon port-kill routes through `killPortHoldersGuarded`** (`port_preflight.go`): re-scan right before the kill, refuse when the daemon, a managed PID, or a descendant of one (`managedTreeChecker` — the listener is usually a child of `cmd /c`, `npm run`, `dotnet watch`) holds the port, return protected PIDs for loud reporting. Callers: `daemon_shutdown.go`, `startup_resilience.go`, `hub_proc.go` (`PROC CLEANUP-PORT`), `killPortBlockers`. Regression: `TestKillPortHoldersGuarded_ProtectsSelf`.

On macOS (any non-Linux Unix) the vendored lookup behind `KillProcessByPort` is `lsof -ti :PORT`, which also matches processes merely connected to the port — a browser with the dev server open. There (`vendoredPortLookupIsBroad`) the guard does not delegate: `killPortHolders` signals exactly the listener PIDs its own scan returned (`portKillTargets`, `platform.KillPID`), under the same refusals. Linux and Windows lookups are listener-only and keep the vendored kill. Regression: `TestKillPortHolders_ExactPathKillsOnlyListeners`. The selection logic is tested on every OS through injected operations; the real darwin kill has only been type-checked (`GOOS=darwin go vet`), not run.

The guard reads both sides of the port probe. On native Windows every holder comes back on the Windows side and those PIDs are our own namespace (`windowsPIDsAreLocal`); under WSL they belong to the Windows kernel and are never compared with ours. Regression: `port_preflight_guard_test.go`.

The autostart conflict scan covers the same ports the launch pre-flight cleans up (`withExpectedPorts`: declared, linked-proxy, command-line, package.json), so `prompt`/`fail` rule on every port a kill could reach.

A declined kill (`port-conflict "skip"`, `AUTOSTART CONTINUE`) travels into the launch as `withPortCleanupDeclined(ctx)` → `StartScriptConfig.SkipPortCleanup` → `portKillDeclined`, so `startScriptWithRetry` kills nothing on that process's ports, on this start or a later restart.

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
- `overlayEndpointForProject(path)` resolves a proxy's overlay socket from the owning session, `""` when none (fail closed; `rebindProxyOverlays` late-binds, replacing an endpoint no registered session owns). A retired session's proxies move to the project's remaining session or unbind (`releaseProxyOverlays`, CAS on the departed endpoint). No global overlay fallback — `SetOverlayEndpoint` no longer broadcasts (that was the cross-project leak).
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

Handlers never write `errNoSessionScope` verbatim: `writeScopeErr` / `writeScopeErrHint` (`hub_scope_candidates.go`) reply with the active sessions to re-issue against (metadata only — code, path, command). A bare error only when no session exists at all. Omitted `global` falls back to `scope.default-global` in `.agnt.kdl` (default false); explicit true/false wins. An `.agnt.kdl` that does not load resolves as false and records one `config_error` startup-log warning per distinct error (`reportScopeConfigError`) — it never fails the verb. A connection is session-bound only after `SESSION REGISTER` or `SESSION ATTACH`. The MCP tools attach by cwd on first use when a session exists there; otherwise they name the project via `SessionCode` (preferred) or `Directory`. Only the registering connection is the session's control connection: an attached connection closing does not schedule cleanup (`onSessionConnectionClosed`).

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
`PROXY STATUS/STOP/RESTART/TOAST`, `PROXYLOG *`, `CURRENTPAGE *`, `TUNNEL STOP/STATUS`.

`PROC STATUS/OUTPUT/STOP/RESTART/AUTORESTART/STDIN` use `resolveProc` (`hub_proc.go`). Every managed process — autostart, `PROC RUN`, and the top-level `RUN` verb, which is `PROC RUN`'s inline mode for an ad-hoc command — has a derived id, `makeProcessID(project, name)` (`myapp-1a2b3c4d:dev`). The argument may be that id or the short name (`dev`):

1. the caller's project, by id then by short name — so a short name several projects use is the caller's own process;
2. the exact id daemon-wide (derived ids are unique);
3. only for a connection bound to no session: the short name daemon-wide when exactly one process has it.

More than one candidate is reported as ambiguous with the projects that have it, never as "not found". A session-bound caller's short names never reach another project.

`RUN` replies with the derived id in `id`/`process_id` and the short name in `name`. An **ad-hoc launch** — `RUN`, `PROC RUN` in inline mode, a `PROC RUN-GROUP` member — is marked in `runLaunches`; `isAdHocLaunch` (`hub_run.go`) is the one definition. It is in the registry and in no config, so `AUTOSTART RECONCILE` does not stop it, the autostart prune does not delete its entry, and a zero exit inside the startup watch is logged as `completed` (info), not as a failed start. `PROC RUN from_config` is the declared script: never marked, follows config. The mark also carries `RUN`'s `enable_stdin` to every launch of the process.

### Debug-exempt

Browser-debug surfaces with an explicit `proxy_id` (`proxy exec`, `responsive_audit`, `snapshot`, `screenshot`, sketch/design, `channel_reply`). Not project-filtered, but the id lookup still uses `getSessionScoped`.

### Client-side project-scoped

`detect`, `demo list/inspect` read the resolved project tree directly (no hub verb). `demo record/assemble` run via `PROC RUN` with explicit `ProjectPath` (`AutoRestart:false`).

### Why STARTUP-LOG is prefix-matched

~60 ingest sites; instead of stamping each, the ProcessID (`makeProcessID` → `basename-hash:name`) encodes the project. Daemon-wide entries with bare IDs are visible only to `global` queries — intended.

## Incident Pipeline

`internal/incident/` is the always-on agent alert path: `Signal sources → Bus → Dedup/Coalesce/FlowControl → Inbox → Pinger → digest sink / PTY sink`. `alerts.push` picks sinks (`mcp-notifications`, `pty-injection`); `alerts.incident-pipeline` is parse-only and declaring it produces a no-effect config warning.

The pinger has two wired sinks (`incidentSinkCallbacks`, `incident_digest.go`), both gated per ping on forwarding-pause and `alerts.push`: the PTY sink injects a one-line hint through the session's overlay, and the digest sink broadcasts an `incident_digest` stream entry scoped to the session's project. The MCP process that owns the agent's connection turns that entry into exactly one notification (`tools.ForwardIncidentDigest`, from `StartIncidentDigestSink`): a `notifications/claude/channel` event when channel mode is on — with the top unread incidents listed from the attached `PingPayload` — and an MCP log notification otherwise. There is no daemon-side channel callback (the pinger's `ChannelNotifyFn` slot is nil) and no separate channel bit in `alerts.push`. `ChannelSink` (`internal/tools/channel_sink.go`) is not a pinger sink: it forwards only the proxy events the pipeline does not ingest (user actions, custom logs, info diagnostics, status-less transport failures) and drops the ones it does (`pipelineOwned`).

Source of truth: inbox = cache of the originating subsystem; bus = transient (drop-newest at 4096, no replay); blob store = per-session in-memory LRU, 16MB, never persisted; dedup fingerprints = per-session, cleared on teardown.

### Numbered Contracts

1. **Cross-session isolation.** Each session has its own `sessionPipeline`; session A's events never reach B's inbox, even same project. Pins live and die with the session inbox (`TestBus_PinDiesWithSession`).
2. **Drop-newest on bus overflow** (4096 slots). Count via `MPSCBus.Dropped()`; every 100th drop synthesises one `meta:bus_overflow` warning.
3. **Dedup is per-session**, not per-project. In practice it is the inbox's fingerprint merge (`Inbox.Ingest`: same fingerprint → `Count++`, newest sample, re-marked unread, band move on escalation), which has no time window. The 30s `Deduplicator` (`dedup.go`) sits in front of it but only supplies first/last timestamps — the bus hands the inbox one occurrence per event either way. Fingerprint = `sha256(source|category|canonical_msg|location)[:16]`; `fingerprintLocation` (`envelope.go`) folds the URL, content frame, **process id and proxy id** into `location`, so the same line from two processes or proxies is two entries. Storm fingerprints stay URL-independent but per proxy.
4. **Ping coalescing is per-session backoff, fixed at construction** (`coalesce.go`, `DefaultPingDelays`): first ping after 500ms, doubling per repeat to a 10s ceiling; a pending ping is never pushed back more than `MaxWait` (10s) from when it became pending, so a continuous stream still emits; 30s idle (`ResetAfter`) resets the backoff. A higher severity than the window has pinged about is prompt, and escalation of a pending ping fires at once. `critical` skips the coalescer and the flow buckets. A repeat of an entry already pinged and still unread schedules nothing. Change needs daemon restart.
5. **Inbox hard-capped per band** — critical/error/warning/info, 100 each, least-recently-updated unpinned entry evicted. `critical` is emitted for a process crash (`FromProcessExit` with reason `crash`, `adapter_proc.go`); a stop or signal exit is `error`. Agents sweep with the cursor: a pull carrying `since`, or the legacy `full` profile, pages oldest-first. A cursor-less pull under a triage profile (`bug`/`changed`/`release`; `bug` is the tool default) is ranked instead (`rankedIncidentPull`, `hub_incidents.go`): secondary filters over the whole inbox, severity order (newest first within a severity), profile projection, then `limit`.
6. **Blob store per-session, best-effort.** Oversized bytes spill into the destination session's store; `detail:"full"` hydrates only from that store. A `BlobRef` may resolve nil — fall back to `Summary`, never another session's store.
7. **Pinger never blocks delivery** — non-blocking sends to every sink.
8. **Push policy is project-isolated and live** — keyed by normalized project path, resolved per ping; one project's update never touches another's sinks or inbox.
9. **The rest of the alerts block is project-isolated too.** `ApplyAlertsConfig` gives each project its own `outage-hold` buffer (`projectHoldBuffers`), transport thresholds (`projectTransport`, read through `HealthTracker.SetTransportConfigResolver`) and retention gates (`projectRetention`). A proxy or process of a project with no applied block uses the daemon-wide defaults (`holdBuffer`, `retentionCfg`). The project's last session retires them (`retireProjectAlertsConfig`); held entries move to the default buffer rather than being dropped.
10. **Untrusted callers are labelled, not trusted.** An error response to a request `httpCallerTrusted` refuses is logged `Untrusted`; its 4xx is no incident, its 5xx is `http_5xx_untrusted` (warning, labelled), like `browser_js_untrusted`.

Files: `envelope.go`, `fingerprint.go`, `adapter_{alert,browser,diag,http,proc}.go`, `dedup.go`, `inbox.go`, `coalesce.go`, `flow.go`, `ping.go`, `remediation.go`, `bus.go` (all `internal/incident/`); `internal/tools/get_incidents.go`; `internal/daemon/hub_incidents.go`.

## Session Containment

Agents background work via non-interactive bash (`npm run dev &`), which has no job control, so those jobs inherit the PTY child's pgid. Containment lets session B reclaim ports A's jobs held.

### The Session pgid Invariant

The `agnt run` PTY child gets its own session (`setsid`); its PID is the session pgid, inherited by every descendant unless it escapes.

1. **Wire-through** — client passes the PTY child PID as `SessionPGID` on `SessionRegister`.
2. **Kill on cleanup** — explicit unregister/shutdown → `CleanupSessionResources` → `doCleanup` → `killSessionPGID` **before** managed processes (SIGTERM, 2s, SIGKILL; self-excluded). A dropped control connection is not cleanup authority: deferred cleanup reaps the group only after the owning `agnt run` PID is confirmed gone; unknown ownership fails safe.
3. **Startup orphan scan** — `Start()` reaps dead-leader pgids (uid-filtered, `session.orphan-pgid-scan`, default on).

Caught: `cmd &`, `nohup`, `disown`, managed scripts, grandchildren, pgids leaked by a daemon crash.

### Exact-identity retirement & per-code lifecycle gate

Teardown retires an exact `*Session`, under a refcounted per-code gate (`sessionLifecycleGates`, `daemon_session_cleanup.go`) that serializes every register/retire on a code. Reconnect installs a fresh pointer via `ReplaceExact`; a stale deferred cleanup pointer-compares in `doCleanupExact` and no-ops (`UnregisterExact` CAS-guards the delete). Classic codes are `<command>-<8 hex>` from crypto/rand (`daemonclient.Client.SessionGenerateCode`); session-host ids are `<name>-<counter>` over a user-supplied name. The two can still collide, so `hubHandleSessionRegister` rejects a classic register onto a session-host entry with `invalid_args`, and rejects one onto a classic entry whose recorded `OwnerPID` is a different, still-running process with `ErrAlreadyExists` — only the registering wrapper may reconnect.

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
