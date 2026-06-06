# WSL Awareness Audit

Date: 2026-05-02 (Dart task `5YgALr79bfhf`).

WSL = third runtime platform: `runtime.GOOS == "linux"` but Windows
filesystem paths and Windows-native processes reachable from same
process. `platform.IsWSL()` (in `internal/platform/process_unix.go`,
detects via `/proc/version` containing `microsoft`/`wsl`) = only signal we
have. This doc catalogs every OS-level decision site found by sweeping
`runtime.GOOS` and `//go:build` tags, classifies each, records which gaps
fixed inline vs deferred.

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
`.claude/rules/config-contracts.md` does not exist in codebase. Both
docs aspirational at time of writing.

## Classification

| Site | File | OS-level decision | WSL-aware? | Verdict |
|------|------|-------------------|-----------|---------|
| `FindPIDsByPort` | `internal/config/portdetect_unix.go:117` | `/proc/net/tcp{,6}` for port→inode mapping | NO — Linux ports only | **FIXED inline** — adds `netstat.exe` fallback when `IsWSL()` and Linux scan returns 0 PIDs |
| `detectPortsForPID` | `internal/config/portdetect_unix.go:21` | `/proc/<pid>/fd` socket inode walk | NO — only sees Linux PIDs | Sub-task filed (low priority — callers always pass PID we already manage, Linux-side by construction) |
| `ProcessNameByPID` | `internal/config/portdetect_unix.go:200` | `/proc/<pid>/comm` | NO — silently returns "" for Windows PIDs | Sub-task filed (medium priority — `doctor.go` shows blank rogue process names on WSL) |
| `directChildren` | `internal/daemon/doctor.go:521` | `/proc/<pid>/task/<pid>/children` | NO — Linux PIDs only | Accepted limitation: descendant tracking only meaningful for our managed processes, always Linux-side |
| `findDuplicates` | `internal/daemon/duplicate_scanner.go:174` | `platform.Scan()` + `platform.ScanWindows()` | YES — already correct | Reference impl |
| `pidAlive` | `internal/daemon/doctor_unix.go:8` | `kill(pid, 0)` | Partial — works for Linux PIDs only; cannot probe Windows PIDs from WSL | Accepted limitation: `pidAlive` called on PIDs we registered (Linux side). Windows PID never reaches this path |
| `ScriptConfig.ResolveShell` | `internal/config/agnt.go:272` | Picks `cmd.exe` on `runtime.GOOS == "windows"`, else `sh` | NO — WSL with Windows-path script (`C:\Users\...\foo.cmd`) gets `sh -c` and silently fails | **Sub-task filed** (high priority — affects mixed-fs WSL projects) |
| `platformShell` | `internal/overlay/status.go:644` | Same as `ResolveShell` | NO | Sub-task filed (low priority — overlay shell launches always developer-initiated, easier to work around) |
| `cleanupStaleFiles` | `internal/daemon/upgrade.go:242` | Windows PID file path layout | NO — but socket path set by caller, so WSL using Windows-path socket = user's choice | Accepted limitation |
| `normalizePath` (3 sites) | `hub_helpers.go:63`, `session.go:332`, `client.go` | Lowercase paths on Windows for case-insensitive compare | Partial — WSL with Windows-path projects (`/mnt/c/Users/...`) still uses case-sensitive comparison | Accepted limitation: `/mnt/c/...` = WSL canonical form, Linux paths case-sensitive even on NTFS via 9P |
| `ResolveShell` `runtime.GOOS == "windows"` default | `internal/config/agnt.go:295` | Same gap as above | NO | Covered by ResolveShell sub-task |
| `internal/browser/browser.go:164` | `internal/browser/browser.go` | Picks browser launcher; `darwin` uses `open` | N/A — WSL doesn't ship `open`; user expected to set `BROWSER` | Not applicable |
| `internal/chromedp/session.go:323` | Chrome devtools URL | `darwin` uses `osascript` | N/A | Not applicable |
| `internal/daemon/upgrade.go:242` | PID file path layout | Windows-only branch | N/A — WSL uses Linux socket layout | Not applicable |
| `cmd/agnt/upgrade.go:136,253` | self-upgrade binary swap | Windows-only branch | N/A — WSL uses Linux self-upgrade path | Not applicable |
| `internal/preflight/preflight.go:36` | Calls `FindPIDsByPort` | Inherits whatever platform gives | YES — picks up inline fix transitively | Covered by inline fix |
| `internal/daemon/port_preflight.go:36` | Calls `FindPIDsByPort` | Inherits whatever platform gives | YES — picks up inline fix transitively | Covered by inline fix |
| `internal/daemon/daemon_autostart.go:869` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/daemon_shutdown.go:140` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/hub_helpers.go:118` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/doctor.go:176` | Calls `FindPIDsByPort` to attribute port owners | Inherits — but display still says "Linux PID" | Partial — gets PID from netstat.exe but rogue-process display path doesn't know to call `tasklist.exe` for name | Sub-task filed (inline fix delivers PID; display path = follow-up) |

## Inline fix landed this iteration

**File:** `internal/config/portdetect_unix.go`

`FindPIDsByPort` now falls back to `netstat.exe -ano` when:
1. `platform.IsWSL()` returns true, AND
2. `/proc/net/tcp{,6}` scan returned zero PIDs

Minimum viable WSL-awareness expansion delivering acceptance
criterion: daemon-side port preflight, autostart cleanup,
shutdown cleanup, and doctor command can now see Windows-side process
holding the port they want to claim. Still can't kill it (needs
`taskkill.exe` follow-up — sub-task filed), but visibility
gap closed and detection no longer silently no-ops.

Why fallback rather than always-also-scan: Linux-side path hot
(every autostart, preflight, shutdown) and `netstat.exe`
shells out, parses CSV, blocks ~50-150 ms. Running it on every
port lookup would dominate startup latency for 99% case where
port owner = Linux process. Falling back only when Linux scan
empty preserves fast path and surfaces Windows-side owners exactly
when needed: when default scan saw nothing.

## Sub-tasks filed (parent: 5YgALr79bfhf, tag: `wsl-followup`)

1. `ResolveShell()` should pick `cmd.exe /c` for Windows-path scripts on
   WSL — needs `ShouldUseWindowsShell(path)` helper that
   `.claude/rules/config-contracts.md` already references but doesn't
   exist
2. Doctor command rogue-process display should call `tasklist.exe` for
   names when PID came from `netstat.exe` on WSL
3. `detectPortsForPID` should also use `netstat.exe` when input PID
   unknown to `/proc` and on WSL — currently silently returns
   nil for any Windows-side PID
4. `ProcessNameByPID` should fall back to `tasklist.exe` lookup for
   Windows-side PIDs on WSL — currently silently returns "" for them
5. `internal/overlay/status.go::platformShell` should use same
   `ShouldUseWindowsShell(path)` helper

## Accepted escape hatches

These behaviors intentional, documented as not-bugs. Future
audit should not "fix" them:

| Behavior | Why we accept it |
|----------|-----------------|
| `pidAlive` cannot probe Windows PIDs from WSL | We never register Windows PIDs in process manager — they only show up via `ScanWindows()`, read-only for us |
| `directChildren` returns nil for Windows-side parent PIDs | We don't track Windows process trees; descendant cleanup for our managed processes |
| `normalizePath` case-sensitive for `/mnt/c/...` paths in WSL | `/mnt/c/Users/Foo` and `/mnt/c/Users/foo` resolve to same NTFS file but Linux treats as distinct paths. Forcing lowercase would break case where two sessions register under different casings — both collapse into one |
| `cleanupStaleFiles` PID file path uses Linux layout under WSL | Daemon socket path owned by caller; run daemon under WSL → Linux paths end-to-end |
| Browser launcher (`internal/browser/browser.go:164`) lacks WSL branch | `BROWSER` env var = WSL-friendly contract; we don't bridge `open` ↔ `cmd.exe /c start` |
| `chromedp` session URL picker darwin-only | Chrome process discovery only meaningful when we launch it ourselves; WSL users typically point at chrome on host |

## How to extend this audit

Run the three grep commands at top of doc. Diff result
against table above. New sites land in table with one of:
- **FIXED inline** — done, with file & line of fix
- **Sub-task filed** — Dart ID + brief rationale
- **Accepted limitation** — added to escape-hatch table
- **Not applicable** — branch platform-specific, WSL correctly
  on neither side

`runtime.GOOS == "windows"` branches almost always need WSL
question asked. `runtime.GOOS == "linux"` branches usually don't (WSL
identifies as linux), but `runtime.GOOS != "linux"` shape (e.g.
`portdetect_unix.go:21`) often does — it's "I'm doing macOS
fallback" which on WSL might want Windows fallback instead.