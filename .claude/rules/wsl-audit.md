---
paths:
  - "internal/platform/**"
  - "internal/config/portdetect*.go"
  - "internal/config/agnt.go"
  - "internal/overlay/status.go"
  - "internal/daemon/doctor*.go"
  - "internal/daemon/duplicate_scanner.go"
  - "internal/sshclient/**"
  - "cmd/agnt/*_windows.go"
  - "cmd/agnt/ssh*.go"
  - "cmd/agnt/attach*.go"
---

# WSL Awareness

WSL is `runtime.GOOS == "linux"` with Windows paths and Windows processes reachable. The only signals: `platform.IsWSL()` (memoized `/proc/version` check) and `platform.ShouldUseWindowsShell(path)` (WSL + backslash or `/mnt/<drive>/` path), both in `internal/platform/process_unix.go` (Windows stubs in `process_windows.go`).

Any `runtime.GOOS == "windows"` branch needs the WSL question asked. `== "linux"` usually doesn't; `!= "linux"` ("macOS fallback") often does.

## Wired

| Capability | Where | Mechanism |
|---|---|---|
| Windows process scan | `platform.ScanWindows`, `duplicate_scanner.go` | `tasklist.exe` |
| Port → PID | `config.FindPIDsByPort` | `netstat.exe -ano` fallback only when the `/proc` scan is empty |
| PID → name | `config.ProcessNameByPID(s)` | batched `tasklist.exe` for `/proc` misses |
| Kill Windows PID | `platform.KillWindowsPID` (port preflight, shutdown) | `taskkill.exe /PID /T /F` |
| Script shell | `ScriptConfig.ResolveShell` | `cmd.exe /c` for Windows-path `run`/`cwd` |
| Doctor port owners | `doctor.go` | names via `ProcessNamesByPIDs` |

The Windows-side fallbacks are gated, not always-on: they shell out (~50-150 ms) and the Linux path is hot (autostart, preflight, shutdown). One batched call per miss set, never per PID.

## Deferred

- `detectPortsForPID` (`portdetect_unix.go`) has no `netstat.exe` branch. Low priority: callers pass PIDs we manage, which are Linux-side.
- Native-Windows `agnt ssh` (named-pipe forwarding, `01KXDMG7KG02MH91W2KXWHZAYA`) and `agnt attach` (ConPTY relay, `01KXDMGBMJB61WXA5YDHB8CY40`) are loud stubs; WSL is the workaround.

## Accepted escape hatches (do not "fix")

| Behavior | Why |
|---|---|
| `pidAlive` can't probe Windows PIDs | we never register Windows PIDs; they are read-only via `ScanWindows` |
| `directChildren` nil for Windows parents | descendant cleanup is only for our (Linux) managed processes |
| `normalizePath` case-sensitive on `/mnt/c/...` | lowercasing would merge sessions registered under different casings |
| Linux PID-file/socket layout under WSL | daemon under WSL is Linux end-to-end |
| No WSL browser-launcher branch | `BROWSER` env var is the contract |
| chromedp URL picker darwin-only | WSL users point at host Chrome |
| Unix sockets, SSH config, `known_hosts` from WSL `$HOME` | WSL runs the Linux client; Windows profile files are not merged |
| `/mnt/c` drop watching = `fsnotify` + 100 ms poll | DrvFS/9P notifications are unreliable |
| `agnt attach` uses Unix raw mode in WSL | WSL provides a Linux tty |
| local `/mnt/c` sources, POSIX remote SFTP paths | each side's OS interprets its own paths |
| `bootstrap.go` classifies the local binary via `runtime.GOOS/GOARCH` | WSL binary is Linux; remote is probed separately |

## Re-audit

```bash
grep -rn "runtime\.GOOS" --include="*.go" . | grep -v vendor | grep -v _test.go
grep -rl "//go:build" --include="*.go" . | grep -v vendor
```

Diff against the tables; classify each new site as fixed, deferred (with task id), accepted, or not applicable.
