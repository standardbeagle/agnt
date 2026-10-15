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

### Keys that parse but have no consumer (decision, 2026-10-13)

These predate the rule and are in users' files, so removing the field would make an existing config fail to load (the parser rejects unknown nodes). They stay accepted and each one produces a warning saying it has no effect:

| Key | File | Warning carried by |
|-----|------|--------------------|
| `proxies.<n>.websocket`, `ai.skill`, `ai.env` | `.agnt.kdl` | `AgntConfig.Warnings` → `surfaceConfigWarnings` → startup log (`config_warning`) |
| `alerts.auto-forward.debounce`, `alerts.incident-pipeline` | `.agnt.kdl` | `AgntConfig.Warnings` (as above) |
| `version`, `languages { … }` | global `config.kdl` | `Config.Warnings` (the daemon start path must log them; `debug.Log` alone does not count) |

They are not in the default template or `docs/configuration.md`. Do not add a key to this list: a new key gets a consumer or is rejected at parse time. Detection is by presence in the document (`inspectAgntDocument`, `agnt_validate.go`), not by value, so `websocket false` warns too.

## Validate what the unmarshaller cannot

`agnt_validate.go` runs inside `ParseAgntConfig`:

- **Closed sets** fail with the accepted values listed: `project.port-conflict`, `alerts.preset`, `alerts.auto-forward.sources` (`browser`, `http`) and `.severity` (`error`, `warning`; `warn` kept as an alias because it loaded before the check existed), `alerts.patterns.<id>.severity` (`error`, `warning`, `info`), `shims.rules.<n>.action` (pinned to `internal/shims` by `TestShimActionsAcceptedByConfig`) and `hook-rules.bash-patterns.<n>.action` (case-insensitive, stored lowercased; pinned by `TestHookActionsAcceptedByConfig`). A consumer `switch` over such a value still needs a safe `default`.
- **Number shape** is checked on the raw document, because kdl-go truncates a float into an `int` field without an error: `scripts.<n>.ports`, `proxies.<n>.listen-port` / `port` / `fallback-port` / `max-log-size`, `automation.max-sessions`. Ports are range-checked too.
- **Custom unmarshalers skip kdl-go's unknown-property check.** `depends-on` rejects any property but `timeout` itself, and a negative timeout.

The default template (`DefaultAgntConfigKDL`) must parse, and so must every example in it once uncommented (`TestDefaultAgntConfigKDL_ExamplesParse`).

## URL matchers

`{url}` is removed and the rest used as a substring regex, after ANSI stripping. Test against real output from common dev servers (dotnet, vite, next, flask).

## Numeric coercion: bound-into-absence

Where `0`/absent means unbounded (`depends-on` timeout, `settings.default-timeout`), truncating a small value to 0 silently removes the limit. Convert in float space: `time.Duration(val * float64(time.Second))`, not `time.Duration(val) * time.Second`. `kdl-go` truncates a float into an `int` field with no error, so an `int` field whose 0 means unbounded is latent (open: `settings.default-timeout`, task `01M0B1Y2D30F1J4GAQN6F3A6M2`). Truncation to 0 is safe only when the code re-clamps ≤0 to a positive default (e.g. `graceful-timeout` → 5s).

## Shell resolution

`ResolveShell` uses `cmd.exe /c` when `platform.ShouldUseWindowsShell(cwd||run)` (WSL + Windows path), else `sh -c` (`cmd.exe` on native Windows).
