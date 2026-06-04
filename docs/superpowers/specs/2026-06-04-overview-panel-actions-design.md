# Overview Panel Actions — Summarize & Reconnect

Date: 2026-06-04

## Problem

The Ctrl+←/→ panel browser replaced the legacy Ctrl+Y popup menu. The menu was
the only reachable entry point for three capabilities, which the refactor
orphaned:

1. **AI status summarize** — `StatusSummarizer.Summarize` — no longer reachable.
2. **Bash command** — `BashRunner.RunBashCommand` via a `StateInput` text dialog.
3. **Daemon reconnect** — `DaemonConnector.Connect` — no longer reachable.

Investigation showed the bash-command path is **redundant**: the overview
panel's `:` command-input already runs ad-hoc shell commands via
`ScriptController.RunCommand`. So bash-command is already on the first panel
through a better affordance; the old `bashRunner` + `StateInput` dialog is dead
duplication.

Summarize and reconnect are genuinely missing from the panel UI. The summarize
implementation also had a UX flaw: it injected its result into the PTY via
stdin (`io.WriteString(ptmx, …)`), which in panel mode lands behind the overlay
and is cleared — the user never sees it.

## Goal

Surface **summarize** and **reconnect** on the overview (first) panel with a
discoverable, panel-native design, and retire the dead menu/bash/dialog code
left behind by the panel refactor.

## Design

### New `summary` panel type

- A `PanelItem{Type: "summary"}`, scrollable, reusing the existing `log`
  panel's `drawScrollableContent` rendering and `SetContent` / `ScrollOffset`
  machinery (no new scroll logic).
- Inserted at panel index 1 (immediately right of `overview`) by
  `buildPanelItems` when `Overlay.summaryText != ""`. Absent until the first
  summarize.
- Tab bar shows it as `summary`. Title row: `summary`.
- A successful summarize replaces its content and auto-focuses it
  (`panelIndex → summary panel`, full redraw).

### Overview "actions" line

Rendered in `drawOverviewContent`, on its own line above the existing `:`
command-input affordance.

- Idle: `actions   m summarize    : command`
- Running: `actions   ◐ summarizing…` (animated spinner)
- Error: `actions   ⚠ summarize failed: <msg>` (red; cleared on next summarize)
- The `m summarize` token is hidden when `summarizer == nil` or
  `!summarizer.IsAvailable()`. The `: command` hint always shows.

### Connection line (contextual)

The first line of `drawOverviewContent`:

- Connected: `● connected (12ms)` — unchanged.
- Disconnected, idle: `✖ disconnected · press c to reconnect`
- Connecting: `◐ connecting…`
- Connect failed: disconnected line plus a red error line below it
  (`⚠ <msg>`), cleared on the next connect attempt or a successful connect.

### Input handling

The existing per-script shortcuts (`a`/`s`/`r`, Enter, `↑↓`) are gated by
`isOverviewWithScripts()` (requires ≥1 script). Summarize and reconnect are
global overview actions and must work with zero scripts, so they are dispatched
from a new guard, `isOverviewPanel()` (panel mode + index 0 + overview type, no
script requirement), checked in `handleMenuKey` after the script-gated block:

- `m` → if a summarizer is available and not already summarizing, start an
  async summarize (see flow below).
- `c` → only when `status.DaemonConnected != ConnectionConnected` and not
  already connecting, start an async reconnect (see flow below).

`:` command-input behavior is unchanged.

### Summarize flow

1. Set `summarizing` (atomic.Bool) true, clear `summaryErr`, redraw.
2. Start a spinner ticker goroutine (~100ms) that redraws the overview while
   `summarizing` is set, so the `◐` frame advances. Stops when the flag clears.
3. Off the overlay lock, call `summarizer.Summarize(ctx)` with a 2-minute
   timeout (same budget as today).
4. On success: store `summaryText`, rebuild panel items (injects/updates the
   summary panel), set `panelIndex` to the summary panel, clear `summarizing`,
   full redraw.
5. On error: set `summaryErr`, clear `summarizing`, redraw.
6. **The PTY stdin injection is removed.** The summary is shown only in the
   summary panel.

### Reconnect flow

1. Set `connecting` (atomic.Bool) true, clear `connectErr`, redraw.
2. Off the overlay lock, call `daemonConnector.Connect()`.
3. On success: call `statusFetcher.Refresh()` so the connection line and
   script/proxy lists update; clear `connecting`, redraw.
4. On failure: set `connectErr`, clear `connecting`, redraw.

### New Overlay state fields

| Field | Type | Guard | Purpose |
|-------|------|-------|---------|
| `summarizing` | `atomic.Bool` | — | spinner + re-entrancy guard |
| `connecting` | `atomic.Bool` | — | spinner + re-entrancy guard |
| `summaryText` | `string` | `mu` | summary panel content source |
| `summaryErr` | `string` | `mu` | transient actions-line error |
| `connectErr` | `string` | `mu` | transient connection-line error |

### Dead code retired (prep commit)

Removed:

- `menu.go` in full: `Menu`, `MenuItem`, `MainMenu`, `DisconnectedMenu`,
  `ErrorMenu`, `processesMenu`, `proxiesMenu`, `ScriptMenu`, `ProcessListMenu`,
  `ProxyListMenu`, and the now-unused `Action` constants.
- `Overlay.Show`, `Overlay.Hide`, `Overlay.showMenu`, `Overlay.ToggleIndicator`.
- `Overlay.menuStack`, `Overlay.selectedIndex`.
- `InputRouter.executeMenuItem` and the non-panel-mode branches of
  `handleMenuKey` (menu-item navigation: `selectedIndex` up/down, Enter on a
  menu item, shortcut-key dispatch).
- Bash path: `BashRunner` interface, `InputRouter.bashRunner`,
  `SetBashRunner`, `NewDaemonBashRunner`, and the `cmd/agnt` wiring that sets
  them. The `:` command-input replaces this.
- Text-input dialog: `StateInput` state, `handleTextInput`, `Renderer.DrawInput`,
  and `Overlay.inputAction` / `inputPrompt` / `inputBuffer`.

Kept:

- `Action` type and `ActionRefreshStatus` (still switched on by the live
  `cfg.OnAction` callbacks in `pty_common.go` and `ai_claude.go`).
- `StatusSummarizer` + `summarizer`, `DaemonConnector` + `daemonConnector`,
  `ScriptController` + `scriptController` — now wired to the overview keys
  instead of menu actions.

`isActive()` drops its `StateInput` arm (only `StateMenu` remains).

## Components & boundaries

- **Renderer** (`render.go`): pure draw functions. `drawOverviewContent` gains
  the actions line and connection-line states; a `summary` case reuses
  `drawScrollableContent`. No daemon or input dependency.
- **Overlay** (`overlay.go`): state holder. New fields + `buildPanelItems`
  summary injection. No I/O.
- **InputRouter** (`input.go`): dispatch + async flows. Owns the summarize and
  reconnect goroutines, talks to the daemon-backed interfaces.
- **cmd/agnt wiring** (`pty_common.go`, `ai_claude.go`): unchanged for
  summarizer/connector setters; bash-runner setters removed.

## Error handling

- Summarize/connect errors surface inline (actions line / connection line),
  never silently swallowed and never injected into the PTY.
- Re-entrancy: `summarizing` / `connecting` atomics prevent overlapping calls.
- A nil/unavailable summarizer hides the `m` affordance rather than erroring on
  press.

## Testing

- `drawOverviewContent` render states: idle actions line; `◐ summarizing…`;
  `⚠ summarize failed`; `m` hidden when summarizer unavailable; disconnected
  reconnect hint; `◐ connecting…`; connect-error line.
- `summary` panel: scrollable render produces content and respects
  `ScrollOffset`; `buildPanelItems` injects it at index 1 only when
  `summaryText != ""`.
- Input: `m` calls `Summarize`, builds and focuses the summary panel, sets no
  PTY write (assert the PTY recorder is empty); `m` is a no-op when summarizer
  unavailable or already summarizing; `c` calls `Connect` only when
  disconnected and not already connecting; `c` is a no-op when connected.
- Regression: `DrawInput` / `handleTextInput` / `ToggleIndicator` removed
  (compile-enforced; delete their tests).

## Commit split

1. **Dead-code strip** — remove menu/bash/dialog/ToggleIndicator surface; tests
   green.
2. **Feature** — summary panel, actions line, connection-line states, input
   dispatch, async flows; tests green.
