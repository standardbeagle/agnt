---
paths:
  - "internal/config/**"
---

# Config Contracts

`.agnt.kdl` is the source of truth for expected project state. The parser (`internal/config/agnt.go`) must parse every declared field, validate at parse time (fail loud), and return defaults via `DefaultAgntConfig()`.

## Every field needs a consumer

A struct field nothing reads is a bug. Adding a key means tracing it to its consumer plus a test that a non-default value changes behaviour.

| Field | Consumer |
|-------|----------|
| `proxies.<n>.fallback-port` | fallback proxy creation, port preflight |
| `scripts.<n>.depends-on` | autostart ordering, ready signaler |
| `scripts.<n>.url-matchers` | URLTracker |
| `scripts.<n>.autostart` | RunAutostart |
| `project.port-conflict` | port preflight |
| `shims.enabled` / `watch-script` / `rules` | `shims.Ensure` + `routeShim` / `WatchScriptName` / `shims.Resolve` |

## URL matchers

`{url}` is removed and the rest used as a substring regex, after ANSI stripping. Test against real output from common dev servers (dotnet, vite, next, flask).

## Numeric coercion: bound-into-absence

Where `0`/absent means unbounded (`depends-on` timeout, `settings.default-timeout`), truncating a small value to 0 silently removes the limit. Convert in float space: `time.Duration(val * float64(time.Second))`, not `time.Duration(val) * time.Second`. `kdl-go` truncates a float into an `int` field with no error, so an `int` field whose 0 means unbounded is latent (open: `settings.default-timeout`, task `01M0B1Y2D30F1J4GAQN6F3A6M2`). Truncation to 0 is safe only when the code re-clamps ≤0 to a positive default (e.g. `graceful-timeout` → 5s).

## Shell resolution

`ResolveShell` uses `cmd.exe /c` when `platform.ShouldUseWindowsShell(cwd||run)` (WSL + Windows path), else `sh -c` (`cmd.exe` on native Windows).
