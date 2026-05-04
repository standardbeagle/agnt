# Daemon Architecture

## Personas

Four distinct participants interact with the daemon. Every feature must consider all four:

1. **Developer** — configures `.agnt.kdl`, runs `agnt run` or opens a Claude Code session, expects dev servers and proxies to "just work." Needs a "doctor" command for manual verification and cleanup.

2. **AI Agent** — calls MCP tools (`proc`, `proxy`, `proxylog`, `get_errors`, `get_incidents`), makes decisions based on state. Requires verified-accurate state — stale or contradictory data causes the agent to take wrong actions, which is worse than no data.

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
| Incident inbox contents | Originating subsystem | Inbox is a cache — re-fetch from source on reconciliation |
| Blob store payloads | In-memory LRU only | Best-effort; evicted on session end or cap overflow |
| Bus in-flight events | Transient channel only | Drop-newest on overflow; no replay |

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

The existing `platform.IsWSL()` helper in `internal/platform/process_unix.go` (memoized `/proc/version` check for `microsoft`/`wsl`) is the canonical WSL detection. New OS-level operations must consult it before using `runtime.GOOS == "linux"` to gate Linux-only behavior — WSL is GOOS=linux but routinely needs to reach Windows-side processes via `tasklist.exe` / `netstat.exe` / `taskkill.exe` interop.

A `ShouldUseWindowsShell(path)` helper does **not** yet exist and is referenced aspirationally in `.claude/rules/config-contracts.md` and the `cmd.exe` row of the table above; it is tracked under the `wsl-followup` Dart tag (parent `5YgALr79bfhf`). Until it lands, `ScriptConfig.ResolveShell()` uses `cmd.exe` only when `runtime.GOOS == "windows"` — WSL with a Windows-path script silently picks `sh -c` and fails. See `.claude/rules/wsl-audit.md` for the full audit.

### WSL Awareness — what's wired vs deferred

| Site | Status | File |
|------|--------|------|
| `platform.IsWSL()` detection | Wired | `internal/platform/process_unix.go:25` |
| `platform.ScanWindows()` (`tasklist.exe`) | Wired | `internal/platform/process_unix.go:141` |
| Duplicate scanner appends Windows procs | Wired | `internal/daemon/duplicate_scanner.go:174` |
| `FindPIDsByPort` falls back to `netstat.exe` | Wired (audit landed 2026-05-02) | `internal/config/portdetect_unix.go` |
| `ShouldUseWindowsShell(path)` helper | **Not yet** — `wsl-followup` sub-task | `internal/platform/` |
| `ResolveShell` picks `cmd.exe` for Windows-path scripts on WSL | **Not yet** — depends on helper above | `internal/config/agnt.go:272` |
| Doctor command attributes Windows-side port owners by name | Partial — PID surfaces, name is blank | `internal/daemon/doctor.go` |
| `taskkill.exe` to kill Windows-side rogue processes | **Not yet** — `wsl-followup` sub-task | `internal/platform/` |

### Accepted WSL escape hatches

These behaviors are intentional. They look like WSL bugs but aren't:

| Behavior | Why we accept it |
|----------|-----------------|
| `pidAlive` cannot probe Windows PIDs from WSL | We never register Windows PIDs in our process manager — they only show up via `ScanWindows()` and are read-only for us |
| `directChildren` returns nil for Windows-side parent PIDs | We don't track Windows process trees; descendant cleanup is for our managed processes |
| `normalizePath` is case-sensitive for `/mnt/c/...` paths in WSL | `/mnt/c/Users/Foo` and `/mnt/c/Users/foo` resolve to the same NTFS file but Linux treats them as distinct paths. Forcing lowercase would collapse two distinct sessions registered under different casings |
| `cleanupStaleFiles` PID file uses Linux layout under WSL | The daemon socket path is owned by the caller; running the daemon under WSL means Linux paths end-to-end |
| Browser launcher doesn't have a WSL branch | `BROWSER` env var is the WSL-friendly contract; we don't bridge `open` ↔ `cmd.exe /c start` |
| `chromedp` session URL picker is darwin-only | Chrome process discovery is only meaningful when we launch chrome ourselves; WSL users typically point at chrome on the host |

## Silent Failure Prohibition

No subsystem may silently skip an expected action. If config declares a proxy, process, or dependency, the system must either:
1. Successfully create/start it, OR
2. Emit a visible error/warning event that reaches the AI agent and session log

`debug.Log` is not sufficient for failures — it only goes to the debug file. Failures must propagate through the event system or session log.

## Config Authority

If `.agnt.kdl` declares expected state (a proxy with `fallback-port`, a script with `depends-on`), the system must honor it. Config fields that are parsed but not acted on are bugs.

## Incident Pipeline

The incident pipeline (`internal/incident/`) is an opt-in alert path (Phase A,
gated by `alerts.incident-pipeline true`) that replaces direct `AlertHub` sink
dispatch with a normalised, deduped, priority-ordered inbox.

### Source of Truth

| State | Source of truth | Notes |
|-------|----------------|-------|
| Inbox entries | Originating subsystem | Inbox is a cache; an entry present in the inbox does not mean the event is still active |
| Bus in-flight events | Transient MPSC channel | Drop-newest on overflow (`bus.go`, 4096-cap). No replay path. |
| Blob store payloads | In-memory LRU per session | Best-effort: evicted when session ends or 16MB cap is reached. Never persisted to disk. |
| Dedup fingerprints | Deduplicator in-process state | Cleared on session teardown; cross-session dedup does not apply |

### Numbered Contracts

1. **Cross-session isolation.** Each session that connects with the pipeline
   enabled gets its own `sessionPipeline` instance. Events from session A never
   appear in session B's inbox, even for the same project.

2. **Drop-newest on bus overflow.** The MPSC bus drops the incoming event (not
   the oldest) when the 4096-slot channel is full. This keeps latency bounded
   at the cost of losing the most recent event under extreme load. Overflow
   count is surfaced via `bus.OverflowCount()`.

3. **Dedup scope is per-session, not per-project.** A fingerprint collision
   in session A does not suppress the same event in session B. The
   Deduplicator state is owned by the `sessionPipeline` and is torn down with
   it.

4. **Coalescer batch window is non-configurable at runtime.** The coalesce
   window (default 200ms) is set at `sessionPipeline` construction time from
   config. Live reconfiguration is not supported; a daemon restart is required
   to change it.

5. **Inbox capacity is hard-capped per band.** Each of the four priority bands
   (critical / error / warning / info) holds at most 100 entries. Oldest
   entries are evicted to make room for new arrivals. The AI agent must poll
   with the returned cursor to drain the inbox before it wraps.

6. **Blob store is best-effort.** A `BlobRef` in an envelope may resolve to
   `nil` if the blob was evicted before the agent pulled the incident. Callers
   must handle absent blobs gracefully; they are not errors.

7. **Pinger never blocks delivery.** The Pinger sends compact pings to MCP,
   channel, and PTY sinks using non-blocking channel sends. A slow consumer
   does not delay other consumers or block the Inbox drain loop.

8. **Migration flag is all-or-nothing per session.** If `alerts.incident-pipeline`
   is `false` when a session connects, that session uses the legacy `AlertHub`
   path for its entire lifetime, even if the config file is changed mid-session.
   The pipeline path and the legacy path are mutually exclusive for a given
   session.

### File Ownership

| Component | File |
|-----------|------|
| `IncidentEvent`, `BlobRef`, `BlobStore` | `internal/incident/envelope.go` |
| Signal source adapters (11 sources) | `internal/incident/adapters.go` |
| `Deduplicator`, `Coalescer`, `FlowController` | `internal/incident/dedup.go` |
| `Inbox` (4 bands, cursor pull, subscribe) | `internal/incident/inbox.go` |
| `Pinger` (subscribe → fan-out pings) | `internal/incident/pinger.go` |
| `get_incidents` MCP tool | `internal/incident/get_incidents.go` |
| Remediation routing table | `internal/incident/routing.go` |
| MPSC bus + `sessionPipeline` | `internal/incident/bus.go` |
| `INCIDENTS QUERY` hub handler, session lifecycle wiring | `internal/daemon/hub_incidents.go` |

## Session Containment

A session owns more than the processes it explicitly registered with `proc run`. The AI agent behind an `agnt run` session routinely spawns background work through non-interactive bash — `npm run dev &`, `cargo watch &`, `python manage.py runserver &` — and non-interactive bash does not enable job control. That means those jobs inherit the PTY child's process group instead of getting one of their own, and the daemon has no explicit handle on them. Without containment, session B cannot claim ports that session A's backgrounded jobs are still holding.

### The Session pgid Invariant

The PTY child started by `agnt run` is given its own POSIX session via `setsid` (creack/pty does this). Its PID doubles as the session pgid, and every descendant process — interactive shells, tool invocations, and backgrounded jobs spawned via `sh -c 'cmd &'` — inherits that pgid unless the descendant explicitly escapes it (see below).

The daemon holds this invariant through three primitives:

1. **Wire-through at registration.** The `agnt run` client captures the PTY child PID and passes it to the daemon as `SessionPGID` during `SessionRegister`. The field survives the client → protocol → hub handler → registry round trip.
2. **Kill on cleanup.** `CleanupSessionResources` → `doCleanup` calls `killSessionPGID` **before** touching managed processes. Sends SIGTERM to the group, waits a 2s grace window, escalates to SIGKILL on any survivors. Self-exclusion protects the daemon's own PID if it ever (defensively) shares the group.
3. **Startup orphan scan.** On `Start()`, the daemon walks `/proc` looking for pgids whose leader is dead but whose members are still alive — the "daemon crashed mid-session" case — and reaps them via the same kill primitive. UID-filtered, gated on the `session.orphan-pgid-scan` config (default on).

### What Is Caught

| Scenario | Caught? | By which primitive |
|----------|---------|--------------------|
| `npm run dev &` in non-interactive bash | yes | session pgid kill on cleanup |
| `nohup cmd &` (SIGHUP blocked) | yes | pgid kill uses SIGTERM/SIGKILL, not SIGHUP |
| `disown %1` after backgrounding | yes | `disown` affects the shell's job table, not the pgid |
| Managed `proc run` scripts | yes | ProcessManager path; redundant with pgid kill |
| Leaked pgid after daemon crash | yes | startup orphan `/proc` scan |
| Grandchildren of backgrounded jobs | yes | they inherit the pgid transitively |

### Accepted Escape Hatches

These **intentionally** escape the session pgid. They represent a conscious "I want to survive session shutdown" decision and the daemon must not try to track them:

| Escape | Why it escapes | Operator responsibility |
|--------|----------------|------------------------|
| `setsid cmd &` | Creates a new session + pgid at exec time | User explicitly asked for a detached process; they own cleanup |
| Double-fork daemon (fork → setsid → fork → exit) | Classic Unix daemonization | Same — this is the explicit "become a daemon" pattern |
| `systemd-run --scope`, `systemd-run --user` | Hands the process to systemd's cgroup | systemd owns the lifetime |
| Container runtimes (`docker run -d`, `podman run -d`) | Container PID1 is the runtime, not the session | Runtime owns cleanup |
| Processes that re-exec into a different uid | `/proc` scan filters by uid | Outside our blast radius |

Each of these leaves a port or resource held after session shutdown, but that is the operator's explicit choice. The repro test `TestSessionContainment_SetsidEscapes` asserts that `setsid` escapes the containment — a regression to it would accidentally reap detached processes, which is worse than leaking them.

### File Ownership

| Primitive | File |
|-----------|------|
| `KillSessionPGID`, `MembersOfPGID`, `readPGID` | `internal/platform/sessionpgid_unix.go` |
| `ScanOrphanPGIDs` (dead-leader scan) | `internal/platform/orphanpgid_unix.go` |
| `killSessionPGID` wiring + `doCleanup` ordering | `internal/daemon/daemon_session_cleanup.go` |
| `startupOrphanPGIDScan` + config gate | `internal/daemon/daemon_orphan_pgid.go` |
| PTY child PID capture + wire-through | `cmd/agnt/pty_common.go`, `internal/daemon/client.go` (`SessionRegisterWithPGID`) |
| Session struct field | `internal/daemon/session.go` (`SessionPGID`) |
| Primitive-level regression tests | `internal/daemon/daemon_session_pgid_test.go`, `internal/daemon/daemon_orphan_pgid_test.go` |
| End-to-end port-reuse repro | `internal/daemon/daemon_session_containment_test.go` |

### Cross-Platform Note

The session pgid primitives are Unix-only (`//go:build !windows`). On Windows, Job Objects already provide equivalent cascade-kill semantics for the PTY child tree, and `SessionPGID` is always 0 — `killSessionPGID` is a no-op guarded by `pgid <= 1`. The startup orphan scan has three implementations selected by build tag:

| Platform | File | Mechanism |
|----------|------|-----------|
| Linux | `internal/platform/orphanpgid_unix.go` (`//go:build linux`) | `/proc` walk via `Scan()` + `readPGID` from `/proc/<pid>/stat` |
| macOS | `internal/platform/orphanpgid_darwin.go` (`//go:build darwin`) | `sysctl` `KERN_PROC_ALL` via `unix.SysctlKinfoProcSlice` — atomic snapshot with pid/pgid/ppid/uid in one syscall |
| Other Unix (FreeBSD, OpenBSD, etc.) | `internal/platform/orphanpgid_other.go` (`//go:build !windows && !linux && !darwin`) | Stubs return nil — no orphan detection but no false reaping either |

The pure orphan-classification logic is shared via `internal/platform/orphanpgid_classify.go` (`//go:build !windows`) and exhaustively tested in `orphanpgid_classify_test.go` so the darwin code path can be verified on Linux CI without a macOS host. macOS-side verification beyond cross-compile (`GOOS=darwin go build ./...`) requires a real darwin runtime to exercise the sysctl source; the procisolation-tagged test file (`orphanpgid_unix_test.go`) is `//go:build linux && procisolation` because it exercises host-global `/proc` and `kill(2)` directly and depends on Linux PID namespaces (`unshare`) for safe execution.

## Test startup contract

Tests almost never want the heavyweight production startup — walking `/proc`, issuing `kill(2)` to whatever PID currently owns a port, replaying persisted proxy state, or spinning up the 24-hour update-check ticker. Running any of those inside a unit test either slows the suite (hundreds of ms per construction × thousands of daemon instances) or, worse, reaps unrelated host processes owned by the same uid.

The daemon solves this with a two-entry-point split. Both entry points share the same `bootstrap()` helper, so the test path cannot drift from production:

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

- **Production `Start()` is byte-for-byte unchanged** — the original sequence is preserved via the shared `bootstrap()` helper. Reviewers who "fix" an apparent omission in `NewForTest` by pulling a production-only step back in will trip the `TestNewForTest_StartsUnder100ms` assertion (100ms budget, ~20ms observed on a laptop).
- **No build tag is needed.** The `*testing.T` parameter is the fence — production code cannot construct a `*testing.T`, so `NewForTest` is unreachable from any non-test caller. Compilation cost of pulling in the `testing` package is negligible for the `agnt` binary.
- **`daemontest.New` routes through `NewForTest`**, so every test that adopted the factory (iter 28, commit `dfcd2a4`) automatically gets the fast startup path. Tests that specifically exercise `cleanupOrphans`, `startupPortCleanup`, `restoreProxies`, or `startupOrphanPGIDScan` continue to call those methods directly — none of them are private to `Start()`.

### File ownership

| Primitive | File |
|-----------|------|
| `bootstrap()` (shared wiring) | `internal/daemon/daemon.go` |
| `Start()` (production path — bootstrap + heavy ops) | `internal/daemon/daemon.go` |
| `NewForTest(t, cfg)` (test entry point) | `internal/daemon/test_helpers.go` |
| `daemontest.New` (ephemeral socket + opts + cleanup) | `internal/daemontest/factory.go` |
| Timing + hub-accept assertions | `internal/daemon/test_helpers_test.go` |
