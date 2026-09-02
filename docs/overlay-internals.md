# Overlay Internals

PTY overlay UI components: command palette, ports/orphans panel, startup splash,
animated indicator, output protection chain. CLAUDE.md points here; routing
invariants that matter most are repeated in CLAUDE.md's gotchas.

## Overview Command Palette

Overview panel command input (`:` or `/`) = **filterable command palette**, not blind shell box. Type to filter `paletteCommands` (`internal/overlay/command_palette.go`); ↑/↓ move highlight; Enter runs highlighted command with trailing token as argument; Tab completes name; Esc cancels.

**Routing invariant**: `handleMenuKey` (`internal/overlay/input.go`) routes ALL keys to `handleCommandInput` *first* when `commandInput` active. Must stay above global menu switch — else Enter/↑/↓/q/x/1-9 stolen by panel navigation (original bug: Enter selected script instead of running typed command).

Commands: `start/stop/restart <script>`, `kill-port <port>`, `kill-orphans`, `restart-proxy <id>`, `stop-proxy <id>`, `stop-tunnel <id>`, `toggle-ports`, `dismiss <n>`, `dismiss-all`, `summarize`, `reconnect`, `run <shell…>`. Dispatch = `InputRouter.dispatchPaletteCommand` — summarize/reconnect/toggle-ports/dismiss/dismiss-all run with overlay lock held (no controller round-trip); the rest release it. `ScriptController` gained `KillPort`, `CleanOrphans`, `RestartProxy`, `StopProxy`, `StopTunnel` (`internal/overlay/status.go`); `kill-port` reuses `PROC CLEANUP-PORT`, `kill-orphans` issues `PORTS CLEAN-ORPHANS`, proxy/tunnel commands issue `PROXY RESTART/STOP` and `TUNNEL STOP`. Tunnel start omitted (needs multi-arg config; use MCP tunnel tool).

## Silent-Failure Notice Banner

Top of the overview renders a **dismissable notice banner** (`drawNoticeBanner`, `internal/overlay/render.go`) for config-declared resources that failed to start and have not recovered — the daemon's Silent Failure Prohibition made visible. Without it a failed proxy is invisible: the script shows "running" and the proxy list is simply empty (the original bug: `bind "0.0.0.0"` without `allow-external true` failed proxy creation on both the URL-detection and fallback paths, silently).

**Source of truth = daemon.** `buildNotices` (`internal/daemon/notices.go`) is a pure reduction over the project-scoped startup-log entries the overview already polls. A `noticeClassification` table maps `event_type → {domain, role, severity}`; failure and success events pair up **per `(process_id, domain)`** — critical because a proxy and its script share a `process_id`, so a script success must not resolve a proxy failure. A notice is active when the latest failure for a `(process_id, domain)` has no success at or after it. Resolve-on-success and latest-failure dedup both fall out of this. Notices ride the `ALERTS STARTUP-LOG` response (`notices` field) — no new verb.

**Dismiss = overlay, session-only.** `noticeDismissals` (`internal/overlay/notices.go`) holds dismissed IDs in memory; `draw()` filters them out of the rendered snapshot and prunes dismissed IDs absent from the active set, so a resolved-then-recurring failure (same `domain:process_id` ID) re-shows. `:dismiss <n>` (1-based, matches the `[n]` index in the banner) and `:dismiss-all` mutate it directly — no daemon call. Banner caps at 3 visible + `+k more`.

## Ports & Orphans Panel

Overview panel renders **ports** section (port-whisperer style) and **orphans** section (orphaned process groups: leader dead, members alive).

Ports tagged `managed`/`unmanaged`/`conflict`, classified `system` vs dev. **System/infra ports hidden by default** (port-whisperer parity): `portIsSystem` (`internal/daemon/hub_ports.go`) flags OS daemons (`systemProcNames` denylist), privileged `<1024` ports, unattributable sockets, WSL Windows-side listeners. managed/conflict never hidden; databases and docker never system. Header shows `N system hidden · :toggle-ports`; `toggle-ports` palette command flips `Overlay.showAllPorts`.

**Perf**: `ListListeningPorts` cached daemon-side (`portsCache`, 4s TTL) since overview polls on 2s status tick, and does **not** shell to `netstat.exe` on WSL — that ran every tick and stalled WSL VM. Windows-side listeners = host noise hidden by default; declared-port conflict detection still reaches Windows owners via `FindPIDsByPort`'s on-demand fallback.

Data flow = daemon→IPC→overlay (overlay can't import daemon):
- `config.ListListeningPorts(ctx)` (`internal/config/portdetect_unix.go`, `_windows.go`) — one `/proc/net/tcp{,6}` + `/proc/*/fd` walk for every LISTEN port and owner; macOS via `lsof`; WSL folds in `netstat.exe`/`tasklist.exe` Windows-side listeners for ports with no Linux owner.
- `PORTS` verb (`protocol.VerbPorts`, handler `internal/daemon/hub_ports.go`): `QUERY` returns ports (classified via `collectManagedPIDs` + declared ports from `.agnt.kdl`) + orphans (`platform.ScanOrphanedPGIDs`, uid-scoped listing). `CLEAN-ORPHANS` resolves the caller's project via `resolveProjectScope` (fails loud on an unresolved non-global caller), then runs each candidate orphan pgid through `pgidOwnershipCheck` — the same cmdline+cwd ownership-evidence gate the startup orphan scan applies (`daemon_orphan_pgid.go`) — before signaling it via `platform.KillSessionPGID`; a shared uid alone is never sufficient to reap a pgid (`internal/daemon/hub_ports_unix.go`, `reapOrphans`/`reapOrphanCandidates`).
- `StatusFetcher.fetchPorts` (`internal/overlay/status.go`) → `Status.Ports`/`Status.Orphans` → `drawOverviewContent` (`internal/overlay/render.go`).

## Startup Splash

**StartupSplash** (`internal/overlay/splash.go`):
- Displays rotating tip text between PTY start and first child output
- Auto-expires after 30s, clears instantly on `OnFirstActivity` callback from ActivityMonitor
- Message rotation on 2.5s timer
- Writes above protected status bar row, uses cursor save/restore to avoid disturbing child cursor

## User Notifications (terminal toast stack)

**Files**: `internal/overlay/notify.go`, `render.go` (`DrawNotifications`), `cmd/agnt/user_notify.go`

One sink for every user-facing message in `cmd/agnt`: `notifyUser(level, fmt, ...)` /
`notifyUserID(id, ...)`. The sink is swapped per phase so a message can never
tear the screen:

| Phase | Sink |
|---|---|
| before the PTY starts (first-run notice, auto-config, config errors) | plain `agnt: …` lines on stderr (`overlay.NewLineNotifier`) |
| PTY session with the terminal overlay | `Overlay.Notify` — the stack below |
| PTY session in raw passthrough | CRLF lines on stderr |
| after `cleanupTerminal` | stderr lines again (`finishSession` restores it) |

`Overlay.Notify(Notification{ID, Level, Text, TTL, Sticky})`:
- **Levels** `Info` / `Warn` / `Error` pick the row style (grey / yellow / red) and the
  default TTL (4s / 8s / 15s). `Sticky` pins until `ClearNotification(id)`.
- **Dedup**: same `ID` (or same level+text when `ID` is empty) bumps a `(×N)` counter and
  refreshes the TTL instead of stacking.
- **Bounds**: store cap 8 (oldest non-sticky evicted), 3 visible rows plus one `+N more`
  row; rows are full-width, padded by display cells (emoji/CJK are two).
- **Placement**: bottom-anchored on the rows directly above the status bar, newest last.
  Painted only in `StateIndicator` on the main screen (never over a child alt screen; a
  panel view returns to whatever the child repaints). Every paint is one buffered write
  bracketed by cursor save/hide and restore/show, like `DrawIndicator`.
- **Expiry**: one `time.AfterFunc` armed for the earliest pending TTL on every paint; the
  repaint clears rows it no longer uses. No per-notification goroutine, no ticker while idle.
- **Splash arbitration**: the startup splash `YieldTo(overlay.HasNotifications)` and clears
  its row while notifications are up.

Trade-off, shared with the splash: the rows belong to the child, so a toast overwrites
whatever the agent drew there until the agent repaints (interactive agents redraw their
input box continuously, so this self-heals). `DrawStatusBarMessage` remains only for the
panel transition spinner; the daemon→browser `PROXY TOAST` path is unrelated and never
reaches the terminal.

## Animated Status Indicator

**Renderer** (`internal/overlay/render.go`):
- `animFrame` atomic counter incremented each `DrawIndicator` call
- `processStateIcon` cycles pulse frames (filled/empty circle variants) for processes in "starting" or "restarting" state
- Frame-based animation distinguishes active startup from static states

## PTY Output Protection

**Output Chain**: `PTY → ProtectedWriter → OutputGate → os.Stdout`

**ProtectedWriter** (`internal/overlay/filter.go`):
- Parses ANSI sequences
- Blocks alt screen (`\x1b[?1049h`)
- Enforces scroll region (`\x1b[r` → `\x1b[1;Nr`)
- Clamps cursor moves to protected bottom row

**OutputGate** (`internal/overlay/gate.go`):
- Freeze/unfreeze for menu display
- Discards output when frozen (not buffered)
