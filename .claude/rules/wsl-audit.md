# WSL Awareness Audit

Date: 2026-05-02 (Dart task `5YgALr79bfhf`).

WSL is a third runtime platform: `runtime.GOOS == "linux"` but Windows
filesystem paths and Windows-native processes are reachable from the same
process. `platform.IsWSL()` (defined in `internal/platform/process_unix.go`,
detects via `/proc/version` containing `microsoft`/`wsl`) is the only signal we
have. This document catalogs every OS-level decision site found by sweeping
`runtime.GOOS` and `//go:build` tags, classifies each, and records which gaps
were fixed inline vs deferred.

## Audit method

```bash
grep -rn "runtime\.GOOS" --include="*.go" | grep -v vendor | grep -v _test.go
find . -path ./vendor -prune -o -name "*.go" -print | xargs grep -l "//go:build" | grep -v vendor
grep -rn "func IsWSL\|func ShouldUseWindowsShell" --include="*.go"
```

Existing `IsWSL()` callers (pre-audit baseline): 2.
- `internal/daemon/duplicate_scanner.go:174` — appends `ScanWindows()` results to native scan
- `internal/platform/process_unix.go:142` (`ScanWindows` self-guard)

`ShouldUseWindowsShell()` referenced in `CLAUDE.md` and
`.claude/rules/config-contracts.md` does not exist in the codebase. Both
documents are aspirational at the time of writing this audit.

## Classification

| Site | File | OS-level decision | WSL-aware? | Verdict |
|------|------|-------------------|-----------|---------|
| `FindPIDsByPort` | `internal/config/portdetect_unix.go:117` | `/proc/net/tcp{,6}` for port→inode mapping | NO — Linux ports only | **FIXED inline** — adds `netstat.exe` fallback when `IsWSL()` and Linux scan returns 0 PIDs |
| `detectPortsForPID` | `internal/config/portdetect_unix.go:21` | `/proc/<pid>/fd` socket inode walk | NO — only sees Linux PIDs | Sub-task filed (low priority — callers always pass a PID we already manage, which is Linux-side by construction) |
| `ProcessNameByPID` | `internal/config/portdetect_unix.go:200` | `/proc/<pid>/comm` | NO — silently returns "" for Windows PIDs | Sub-task filed (medium priority — `doctor.go` shows blank rogue process names on WSL) |
| `directChildren` | `internal/daemon/doctor.go:521` | `/proc/<pid>/task/<pid>/children` | NO — Linux PIDs only | Accepted limitation: descendant tracking is only meaningful for our managed processes which are always Linux-side |
| `findDuplicates` | `internal/daemon/duplicate_scanner.go:174` | `platform.Scan()` + `platform.ScanWindows()` | YES — already correct | Reference impl |
| `pidAlive` | `internal/daemon/doctor_unix.go:8` | `kill(pid, 0)` | Partial — works for Linux PIDs only; cannot probe Windows PIDs from WSL | Accepted limitation: `pidAlive` is called on PIDs we registered ourselves (Linux side). A Windows PID never reaches this path |
| `ScriptConfig.ResolveShell` | `internal/config/agnt.go:272` | Picks `cmd.exe` on `runtime.GOOS == "windows"`, else `sh` | NO — WSL with a Windows-path script (`C:\Users\...\foo.cmd`) gets `sh -c` and silently fails | **Sub-task filed** (high priority — affects mixed-fs WSL projects) |
| `platformShell` | `internal/overlay/status.go:644` | Same as `ResolveShell` | NO | Sub-task filed (low priority — overlay shell launches are always developer-initiated, easier to work around) |
| `cleanupStaleFiles` | `internal/daemon/upgrade.go:242` | Windows PID file path layout | NO — but socket path is set by caller, so WSL using a Windows-path socket is the user's choice | Accepted limitation |
| `normalizePath` (3 sites) | `hub_helpers.go:63`, `session.go:332`, `client.go` | Lowercase paths on Windows for case-insensitive compare | Partial — WSL with Windows-path projects (`/mnt/c/Users/...`) still uses case-sensitive comparison | Accepted limitation: `/mnt/c/...` is the WSL canonical form, and Linux paths are case-sensitive even when they live on NTFS via 9P |
| `ResolveShell` `runtime.GOOS == "windows"` default | `internal/config/agnt.go:295` | Same gap as above | NO | Covered by ResolveShell sub-task |
| `internal/browser/browser.go:164` | `internal/browser/browser.go` | Picks browser launcher; `darwin` uses `open` | N/A — WSL doesn't ship `open`; user is expected to set `BROWSER` | Not applicable |
| `internal/chromedp/session.go:323` | Chrome devtools URL | `darwin` uses `osascript` | N/A | Not applicable |
| `internal/daemon/upgrade.go:242` | PID file path layout | Windows-only branch | N/A — WSL uses Linux socket layout | Not applicable |
| `cmd/agnt/upgrade.go:136,253` | self-upgrade binary swap | Windows-only branch | N/A — WSL uses Linux self-upgrade path | Not applicable |
| `internal/preflight/preflight.go:36` | Calls `FindPIDsByPort` | Inherits whatever the platform gives | YES — picks up the inline fix transitively | Covered by inline fix |
| `internal/daemon/port_preflight.go:36` | Calls `FindPIDsByPort` | Inherits whatever the platform gives | YES — picks up the inline fix transitively | Covered by inline fix |
| `internal/daemon/daemon_autostart.go:869` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/daemon_shutdown.go:140` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/hub_helpers.go:118` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/doctor.go:176` | Calls `FindPIDsByPort` to attribute port owners | Inherits — but display still says "Linux PID" | Partial — gets the PID from netstat.exe but the rogue-process display path doesn't know to call `tasklist.exe` for the name | Sub-task filed (the inline fix delivers the PID; the display path is a follow-up) |

## Inline fix landed this iteration

**File:** `internal/config/portdetect_unix.go`

`FindPIDsByPort` now falls back to `netstat.exe -ano` when:
1. `platform.IsWSL()` returns true, AND
2. The `/proc/net/tcp{,6}` scan returned zero PIDs

This is the minimum viable WSL-awareness expansion that delivers the
acceptance criterion: the daemon-side port preflight, autostart cleanup,
shutdown cleanup, and doctor command can now see a Windows-side process
holding the port they're trying to claim. They still can't kill it (that
requires a `taskkill.exe` follow-up — sub-task filed), but the visibility
gap is closed and the detection no longer silently no-ops.

Why fallback rather than always-also-scan: the Linux-side path is hot
(every autostart, every preflight, every shutdown) and `netstat.exe`
shells out, parses CSV, and blocks for ~50-150 ms. Running it on every
port lookup would dominate startup latency for the 99% case where the
port owner is a Linux process. Falling back only when the Linux scan is
empty preserves the fast path and surfaces Windows-side owners exactly
when we need them: when our default scan saw nothing.

## Sub-tasks filed (parent: 5YgALr79bfhf, tag: `wsl-followup`)

1. `ResolveShell()` should pick `cmd.exe /c` for Windows-path scripts on
   WSL — needs the `ShouldUseWindowsShell(path)` helper that
   `.claude/rules/config-contracts.md` already references but doesn't
   exist
2. Doctor command rogue-process display should call `tasklist.exe` for
   names when the PID came from `netstat.exe` on WSL
3. `detectPortsForPID` should also use `netstat.exe` when the input PID
   is unknown to `/proc` and we're on WSL — currently silently returns
   nil for any Windows-side PID
4. `ProcessNameByPID` should fall back to `tasklist.exe` lookup for
   Windows-side PIDs on WSL — currently silently returns "" for them
5. `internal/overlay/status.go::platformShell` should use the same
   `ShouldUseWindowsShell(path)` helper

## Accepted escape hatches

These behaviors are intentional and documented as not-bugs. Any future
audit should not "fix" them:

| Behavior | Why we accept it |
|----------|-----------------|
| `pidAlive` cannot probe Windows PIDs from WSL | We never register Windows PIDs in our process manager — they only show up via `ScanWindows()` and are read-only for us |
| `directChildren` returns nil for Windows-side parent PIDs | We don't track Windows process trees; descendant cleanup is for our managed processes |
| `normalizePath` is case-sensitive for `/mnt/c/...` paths in WSL | `/mnt/c/Users/Foo` and `/mnt/c/Users/foo` resolve to the same NTFS file but Linux treats them as distinct paths. Forcing lowercase would break the case where two sessions register under different casings — both would collapse into one session |
| `cleanupStaleFiles` PID file path uses Linux layout under WSL | Daemon socket path is owned by the caller; if you run the daemon under WSL you get Linux paths end-to-end |
| Browser launcher (`internal/browser/browser.go:164`) doesn't have a WSL branch | `BROWSER` env var is the WSL-friendly contract; we don't try to bridge `open` ↔ `cmd.exe /c start` |
| `chromedp` session URL picker is darwin-only | Chrome process discovery is only meaningful when we're launching it ourselves; WSL users typically point at a chrome on the host |

## How to extend this audit

Run the three grep commands at the top of this doc. Diff the result
against the table above. New sites land in the table with one of:
- **FIXED inline** — done, with the file & line of the fix
- **Sub-task filed** — Dart ID + brief rationale
- **Accepted limitation** — added to the escape-hatch table
- **Not applicable** — branch is platform-specific and WSL is correctly
  on neither side

`runtime.GOOS == "windows"` branches almost always need the WSL
question asked. `runtime.GOOS == "linux"` branches usually don't (WSL
identifies as linux), but the `runtime.GOOS != "linux"` shape (e.g.
`portdetect_unix.go:21`) often does — it's "I'm doing the macOS
fallback" which on WSL might want the Windows fallback instead.
