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

> **Update 2026-06-01:** `ShouldUseWindowsShell()` — referenced
> aspirationally by `.claude/rules/config-contracts.md` and
> `daemon-architecture.md` when this audit was first written — now exists
> at `internal/platform/process_unix.go:53` (Windows stub in
> `process_windows.go:85`) and is consumed by `ResolveShell`. See the
> "Fixes landed since the audit" section below; the verdicts in the
> classification table reflect post-fix reality.

## Classification

| Site | File | OS-level decision | WSL-aware? | Verdict |
|------|------|-------------------|-----------|---------|
| `FindPIDsByPort` | `internal/config/portdetect_unix.go:117` | `/proc/net/tcp{,6}` for port→inode mapping | NO — Linux ports only | **FIXED inline** — adds `netstat.exe` fallback when `IsWSL()` and Linux scan returns 0 PIDs |
| `detectPortsForPID` | `internal/config/portdetect_unix.go:21` | `/proc/<pid>/fd` socket inode walk | NO — only sees Linux PIDs | Sub-task filed (low priority — callers always pass a PID we already manage, which is Linux-side by construction) |
| `ProcessNameByPID` / `ProcessNamesByPIDs` | `internal/config/portdetect_unix.go:336,387` | `/proc/<pid>/comm` | YES | **FIXED** — falls back to a batched `tasklist.exe` resolve for `/proc` misses on WSL (`tasklistResolve`); `ProcessNamesByPIDs` coalesces a whole PID set into one `tasklist.exe` call |
| `directChildren` | `internal/daemon/doctor.go:521` | `/proc/<pid>/task/<pid>/children` | NO — Linux PIDs only | Accepted limitation: descendant tracking is only meaningful for our managed processes which are always Linux-side |
| `findDuplicates` | `internal/daemon/duplicate_scanner.go:174` | `platform.Scan()` + `platform.ScanWindows()` | YES — already correct | Reference impl |
| `pidAlive` | `internal/daemon/doctor_unix.go:8` | `kill(pid, 0)` | Partial — works for Linux PIDs only; cannot probe Windows PIDs from WSL | Accepted limitation: `pidAlive` is called on PIDs we registered ourselves (Linux side). A Windows PID never reaches this path |
| `ScriptConfig.ResolveShell` | `internal/config/agnt.go:315` | Picks `cmd.exe` when `ShouldUseWindowsShell(cwd\|\|run)`, then on `runtime.GOOS == "windows"`, else `sh` | YES | **FIXED** — consults `platform.ShouldUseWindowsShell` first, so a WSL session with a Windows-path `run`/`cwd` gets `cmd.exe /c` |
| `platformShell` | `internal/overlay/status.go:624` | Same as `ResolveShell` | NO | Sub-task open (low priority — overlay shell launches are always developer-initiated, easier to work around) |
| `cleanupStaleFiles` | `internal/daemon/upgrade.go:242` | Windows PID file path layout | NO — but socket path is set by caller, so WSL using a Windows-path socket is the user's choice | Accepted limitation |
| `normalizePath` (3 sites) | `hub_helpers.go:63`, `session.go:332`, `client.go` | Lowercase paths on Windows for case-insensitive compare | Partial — WSL with Windows-path projects (`/mnt/c/Users/...`) still uses case-sensitive comparison | Accepted limitation: `/mnt/c/...` is the WSL canonical form, and Linux paths are case-sensitive even when they live on NTFS via 9P |
| `ResolveShell` `runtime.GOOS == "windows"` default | `internal/config/agnt.go:347` | Final fallback after the `ShouldUseWindowsShell` check | YES | **FIXED** — see `ResolveShell` row above |
| `internal/browser/browser.go:164` | `internal/browser/browser.go` | Picks browser launcher; `darwin` uses `open` | N/A — WSL doesn't ship `open`; user is expected to set `BROWSER` | Not applicable |
| `internal/chromedp/session.go:323` | Chrome devtools URL | `darwin` uses `osascript` | N/A | Not applicable |
| `internal/daemon/upgrade.go:242` | PID file path layout | Windows-only branch | N/A — WSL uses Linux socket layout | Not applicable |
| `cmd/agnt/upgrade.go:136,253` | self-upgrade binary swap | Windows-only branch | N/A — WSL uses Linux self-upgrade path | Not applicable |
| `internal/preflight/preflight.go:36` | Calls `FindPIDsByPort` | Inherits whatever platform gives | YES — picks up inline fix transitively | Covered by inline fix |
| `internal/daemon/port_preflight.go:36` | Calls `FindPIDsByPort` | Inherits whatever platform gives | YES — picks up inline fix transitively | Covered by inline fix |
| `internal/daemon/daemon_autostart.go:869` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/daemon_shutdown.go:140` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/hub_helpers.go:118` | Calls `FindPIDsByPort` | Inherits | YES — covered by inline fix | Covered by inline fix |
| `internal/daemon/doctor.go:209` | Calls `FindPIDsByPort` to attribute port owners, then `ProcessNamesByPIDs` for names | Inherits the netstat.exe PID + tasklist.exe name | YES | **FIXED** — port-conflict report now batches all rogue PIDs through `config.ProcessNamesByPIDs`, surfacing Windows-side process names via `tasklist.exe` |

## Fixes landed since the audit

The first iteration delivered the `FindPIDsByPort` → `netstat.exe -ano`
fallback (fires only when `platform.IsWSL()` and the `/proc/net/tcp{,6}`
scan returned zero PIDs). Subsequent iterations closed four more of the
filed sub-tasks. Net WSL surface as of 2026-06-01:

| Capability | File | Mechanism |
|------------|------|-----------|
| Port→PID for Windows-side listeners | `internal/config/portdetect_unix.go` | `FindPIDsByPort` falls back to `netstat.exe -ano` when the `/proc` scan is empty |
| PID→name for Windows-side PIDs | `internal/config/portdetect_unix.go:336,387` | `ProcessNameByPID` / `ProcessNamesByPIDs` fall back to a batched `tasklist.exe` resolve for `/proc` misses |
| Kill Windows-side rogue processes | `internal/platform/killwindowspid_unix.go:36` | `KillWindowsPID` shells to `taskkill.exe /PID /T /F`; called by port preflight + shutdown cleanup |
| Windows-path script shell selection | `internal/config/agnt.go:315` | `ResolveShell` consults `platform.ShouldUseWindowsShell(cwd\|\|run)` and returns `cmd.exe /c` |
| Doctor port-conflict names | `internal/daemon/doctor.go:209` | Batches all rogue PIDs through `config.ProcessNamesByPIDs` so the report shows Windows process names |

Why the `netstat.exe` fallback is gated rather than always-also-scan: the
Linux-side path is hot (every autostart, preflight, shutdown) and
`netstat.exe` shells out, parses CSV, and blocks for ~50-150 ms. Running
it on every port lookup would dominate startup latency for the 99% case
where the owner is a Linux process. Falling back only when the Linux scan
is empty preserves the fast path and surfaces Windows-side owners exactly
when our default scan saw nothing. `tasklist.exe` resolution follows the
same discipline — one batched call per `/proc`-miss set, not per PID.

## Sub-tasks still open (parent: 5YgALr79bfhf, tag: `wsl-followup`)

Closed by the iterations above: `ResolveShell` cmd.exe selection
(+`ShouldUseWindowsShell` helper), doctor rogue-process names,
`ProcessNameByPID` tasklist fallback, and `taskkill.exe` kill support.

Remaining (both low priority):

1. `detectPortsForPID` should also use `netstat.exe` when the input PID
   is unknown to `/proc` and we're on WSL — currently routes only through
   `detectPortsForPIDProc` (Linux) / `detectPortsForPIDLsof` and returns
   nil for any Windows-side PID. Low priority: callers always pass a PID
   we already manage, which is Linux-side by construction.
2. `internal/overlay/status.go:platformShell` should use the same
   `ShouldUseWindowsShell(path)` helper instead of gating on raw
   `runtime.GOOS == "windows"`. Low priority: overlay shell launches are
   developer-initiated and easy to work around.

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
| Remote SSH/control sockets use Unix sockets under WSL `$HOME` | WSL intentionally runs the Linux client end-to-end; Windows named-pipe interoperability belongs to native Windows, not WSL |
| SSH config and `known_hosts` come from WSL `~/.ssh` | This matches the OpenSSH instance running inside WSL; Windows-profile SSH files are not silently merged |
| Drop watching on `/mnt/c` combines `fsnotify` with polling | DrvFS/9P notifications are unreliable; the 100 ms metadata poll is the intentional correctness fallback |
| WSL uses Unix raw terminal relay for `agnt attach` | WSL exposes a Linux tty, so `term.MakeRaw` is the correct implementation; native Windows requires a separate ConPTY relay |
| Remote SFTP destinations use POSIX paths while local sources may be `/mnt/c/...` | The remote host defines destination semantics; WSL local paths are opened locally before transfer |

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

## Remote SSH/session-host audit — 2026-07-13

Scope: `cmd/agnt/{ssh,attach}*`, `internal/sessionhost/`, and the new `internal/sshclient/` session/forward/bootstrap/push/drop paths. The prescribed `runtime.GOOS` and `//go:build` sweeps found **zero unclassified OS decisions in this scope**.

| Site | Decision | WSL verdict |
|---|---|---|
| `ssh.go` (`!windows`) / `ssh_windows.go` | Unix SSH client versus loud native-Windows stub | WSL selects Unix, as intended. Native Windows still lacks named-pipe forwarding. |
| `attach_unix.go` / `attach_windows.go` | Unix raw-terminal relay versus loud native-Windows stub | WSL `term.MakeRaw` uses its Linux tty and is supported. Native Windows still needs ConPTY relay. |
| reconnect tests/harness (`!windows`) | Unix-only fixtures | Correct for WSL; production reconnect adds no raw OS branch. |
| `bootstrap.go` `runtime.GOOS/GOARCH` defaults | Classify the local executable | Correct: the WSL binary is Linux. Remote OS/arch is probed separately; Windows sshd targets fail loudly. |
| daemon/control forward sockets | Unix sockets under `os.UserHomeDir()` | Correct: WSL uses its Linux home/filesystem. No Windows named-pipe interoperability is claimed. |
| SSH config and `known_hosts` | WSL `~/.ssh` via `os.UserHomeDir()` | Accepted OpenSSH behavior: use WSL's Linux files, not the Windows profile's files. |
| `DropWatcher` | `fsnotify` plus 100 ms metadata poll | WSL-aware: polling covers unreliable `/mnt/c` 9P/DrvFS notifications and retains quiescence checks. |
| SFTP push paths | Local `filepath`; POSIX remote paths | Correct: `/mnt/c/...` works as a local source; remote destinations remain POSIX and project-relative. |
| `internal/sessionhost/` | No runtime OS branch or build tag | Correct: existing platform PTY/process primitives own OS differences. |

Deferred work is explicit, not a silent WSL defect: native-Windows `agnt ssh` named-pipe forwarding is follow-up `01KXDMG7KG02MH91W2KXWHZAYA` (Unix socket forwarding cannot provide a native Windows listener), and native-Windows `agnt attach` ConPTY relay is follow-up `01KXDMGBMJB61WXA5YDHB8CY40` (the Unix raw-tty relay cannot drive a Windows console). WSL is the supported Windows-host workaround.
