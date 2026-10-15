# Overlay Internals

PTY overlay UI components: command palette, ports/orphans panel, startup splash,
animated indicator, output protection chain. CLAUDE.md points here; routing
invariants that matter most are repeated in CLAUDE.md's gotchas.

## Panel Navigation

Ctrl+→ / Ctrl+← from the agent's screen opens the panel view on the **overview**, which is the hub every other panel is reached from. Tab order is fixed by type (`panelRank`, `internal/overlay/overlay.go`): overview, summary, scripts, proxies, config, log — panels created later (a script started after the first open, the config editor) slot into their type's place instead of after the log.

| Key | Overview | Any other panel |
|---|---|---|
| Esc | close the panel view | back to the overview |
| q | close the panel view | close the panel view |
| Tab / → / Ctrl+→ | next panel | next panel |
| Shift+Tab / ← / Ctrl+← | close (no panel before it) | previous panel |
| ↑↓ j k, Home/End g G | move the script highlight | scroll (PgUp/PgDn by page) |
| Enter, 1-9 | open the highlighted / Nth script | 1-9 open the Nth script |
| x | — | close a stopped/failed script panel |

A paste while a panel is open is one unit, never a run of keys: the escape reader collects a bracketed paste (`ESC [200~` … `ESC [201~`, which the terminal sends because the agent enables mode 2004) and reports it as one `Paste:` key. The command palette takes it as typed text on one line (a pasted newline is not Enter), the config editor inserts it verbatim, and any other panel ignores it with a toast. A paste over 64 KiB is dropped whole; one that goes quiet for a second is abandoned.

The 1-9 numbers are printed beside each overview script row and follow the script list, never panel-creation order. The overview list shows 8 rows and scrolls with the highlight. The footer lists only the keys that work on the active panel.

The panel view is live: every status update (the 2s tick, and the immediate refresh after a palette or a/s/r command) rebuilds the tabs and repaints. The repaint is a frame diff, so an unchanged status writes nothing. Proxy tabs come and go with the proxies; the tab you are viewing going away drops you on the overview. A disconnected status never removes tabs. The daemon drops its 4s ports/orphans cache after every port kill or orphan reap (`invalidatePortsCache`), so the refresh after `kill-port` or `kill-orphans` is a fresh scan.

Notifications are painted on the main screen, which a panel view covers, so while a panel is open the newest live notification replaces the footer hints (`OverviewActions.Toast`) until it expires. Without that, a failed a/s/r or palette command, or the config editor's "Esc again to discard" warning, could expire unseen.

## Overview Command Palette

Overview panel command input (`:` or `/`) = **filterable command palette**, not blind shell box. Type to filter `paletteCommands` (`internal/overlay/command_palette.go`); ↑/↓ move highlight; Enter runs highlighted command with trailing token as argument; Tab completes name; Esc cancels. The palette is anchored to the bottom of the overview with its rows reserved before the sections draw (list capped at `maxPaletteRows`, scrolled to the highlight), so a crowded overview cannot push it off screen.

**Routing invariant**: `handleMenuKey` (`internal/overlay/input.go`) routes ALL keys to `handleCommandInput` *first* when `commandInput` active. Must stay above global menu switch — else Enter/↑/↓/q/x/1-9 stolen by panel navigation (original bug: Enter selected script instead of running typed command).

Commands: `start/stop/restart <script>`, `kill-port <port>`, `kill-orphans`, `restart-proxy <id>`, `stop-proxy <id>`, `stop-tunnel <id>`, `toggle-ports`, `dismiss <n>`, `dismiss-all`, `summarize`, `reconnect`, `run <shell…>`. Dispatch = `InputRouter.dispatchPaletteCommand` — summarize/reconnect/toggle-ports/dismiss/dismiss-all run with overlay lock held (no controller round-trip); the rest release it. `ScriptController` gained `KillPort`, `CleanOrphans`, `RestartProxy`, `StopProxy`, `StopTunnel` (`internal/overlay/status.go`); `kill-port` reuses `PROC CLEANUP-PORT`, `kill-orphans` issues `PORTS CLEAN-ORPHANS`, proxy/tunnel commands issue `PROXY RESTART/STOP` and `TUNNEL STOP`. `tunnel <provider> [proxy]` starts one under the **proxy's** id — `PROXY LIST` attaches `tunnel_url` to a proxy row by matching tunnel id to proxy id, so any other id runs fine and never shows up next to its proxy — and points it at the **proxy's** listen port, never the backend's (that would bypass the instrumentation). `tailscale [proxy]` writes `bind "tailscale"` **and** the auto-detected tailnet address as `status-url` into `proxies.<config-name>` in one atomic edit (`config.SetProxyProperties`), then reconciles — `bind` is part of `proxySignature`, so the reconcile stops the loopback listener and restarts the proxy on the tailnet address. Pinning the address without the bind was the earlier behaviour and produced a URL no other device could reach. A proxy with no `ConfigName` (started through the tool path) is refused: config reconcile deliberately never touches it. The file is written before the live apply, so a reconcile failure reports which half landed. Both resolve the target proxy via `resolveProxy` — the only running proxy by default, otherwise a refusal that NAMES the candidates rather than guessing.

## Config Editor Panel (`:config`)

`:config` opens `.agnt.kdl` in an editable panel (`internal/overlay/config_editor.go`). The model (`ConfigEditor`) is pure — buffer, rune-based cursor, scroll, dirty flag — so the editing rules are tested without a terminal; the renderer (`drawConfigPanelContent`) and the key router (`handleConfigKey`) are thin wrappers.

**Routing invariant** (same shape as the palette's): `handleMenuKey` routes keys to `handleConfigKey` before the global menu switch. Without it, typing a word containing `q` or `x` reaches the panel browser and closes the panel mid-edit.

- **Save** is `^S`, which arrives as the raw byte `\x13` — the escape reader only names CSI-sequence keys. Backspace likewise arrives as `\x7f`.
- **A buffer that does not parse is never written.** `config.SaveAgntConfigText` validates first and writes atomically, so a refused save leaves the file byte-identical; writing a broken config would take out every autostart script and proxy on the next reload. The footer shows `KDL: ok` / the parse error as you type, so the answer is visible before you reach for the save key.
- A successful save also reconciles, since a saved config the daemon has not read is half the job.
- **Escape with unsaved edits warns once** and discards on a second press; any other key re-arms the guard so an old Escape cannot satisfy the confirmation.
- A project with no `.agnt.kdl` opens on the documented default template (`config.DefaultAgntConfigKDL`), not an empty buffer.
- `buildPanelItems` re-asserts the panel, because panels are otherwise derived from daemon status and the next refresh would drop it mid-edit.

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
- **Locking**: `Notify` takes the overlay lock to repaint, so code already holding it (every
  panel key handler runs under it) calls `notifyLocked`. Calling `Notify` there deadlocks the
  input goroutine on itself and freezes all overlay input; a handler that must call something
  which notifies (`setForwarding`) releases the lock around it.

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
- Tracks the child's cursor; overlay draws restore it by absolute CUP, never CSI s/u (`internal/overlay/cursorpark.go`). Every draw over the child goes out through `drawOverChild`, which reads the cursor and writes the whole draw under the filter's lock (`HoldChildCursor`), so no child chunk can be tracked but not yet emitted when the restore is computed. Lock order: renderer/splash/screen locks → filter → gate; the filter's lock is only ever followed by the gate's.

**OutputGate** (`internal/overlay/gate.go`):
- Freeze/unfreeze for menu display
- Buffers output while frozen in a 64KB ring (oldest bytes dropped on overflow) and flushes it on unfreeze

## Stdin Ownership

One goroutine in the process reads `os.Stdin`: the pump in `cmd/agnt/stdin_pump.go` (`processStdin`). A session takes a **lease** on it and gives that to its single stdin owner — the `InputRouter` (`SetInput`) with the overlay on, `rawStdinRelay` under `--no-overlay`. `pipelineRuntime.Stop` closes the lease, which ends the owner's blocked `Read`; a `Read` on `os.Stdin` itself cannot be ended.

- Never add a second reader of `os.Stdin` in a session. A prompt that needs a keystroke borrows input through the owner's modal capture (`stdinModal`: `BeginModalCapture` / `EndModalCapture`), as the port-conflict prompt does. No answer within the window means decline.
- Bytes typed while no lease is open (between the first-run setup phase and the relaunch) are held and delivered to the next lease.
- The AI REPLs (`agnt ai claude`, `agnt acp`) run one router per process and read stdin through it; a mid-turn question goes through `LineEditor.Ask`, and Ctrl+C during a turn reaches `LineEditor.SetInterruptHandler` (raw mode delivers it as byte `0x03`, never as SIGINT).

## Overlay Socket

The browser → agent channel is a per-session Unix-domain socket, on every platform — there is no TCP listener. `listenOverlaySocket` (`cmd/agnt/overlay.go`) is the only place either overlay server (`Overlay`, `AIOverlay`) gets its listener:

- Sockets live in a per-user directory (`/tmp/agnt-overlay-<uid>/`, `%TEMP%\agnt-overlay-<user>\`), created `0700` and refused if it is a symlink or owned by someone else. Session codes repeat across users, so the directory is what keeps two users' `claude-1` apart.
- The socket is `chmod 0600`: anything that can connect can type into the agent (`/type`, `/inject`, `/key`). `/inject` takes the daemon's plain-text sends (`agnt session send`, the browser's session send, the incident pipeline's PTY hint) and queues them as protected messages; `/type` answers before its Enter retries finish.
- Injections are serialized (`injectMu`): one message's text and Enter retries finish before the next starts. After the first Enter, a retry is sent only while the agent stays silent and, when the screen showed the typed text before that Enter, only while it still shows it near the cursor (`ActivityMonitor.CursorRowsContain`). An agent that accepts a message and works silently gets no stray Enters.
- Alert batches wait while the agent is active and are typed once it goes idle. An agent whose output never pauses gets the batch after `maxActivityDeferral` (2 minutes); the overload throttle bounds what piles up meanwhile.
- An existing socket is unlinked only when nothing answers on it. A live one (a second run with the same `--session`) is an error.
- A session whose overlay failed to start registers an empty endpoint with the daemon (`Overlay.Endpoint`).
