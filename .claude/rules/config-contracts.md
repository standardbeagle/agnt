---
paths:
  - "internal/config/**"
---

# Config Contracts

## Parsing Authority

`.agnt.kdl` = single source of truth for expected project state. Config parser (`internal/config/agnt.go`) must:

1. Parse all declared fields fully — no field parsed but unused
2. Validate at parse time — bad configs fail loud, not silent at runtime
3. Return sane defaults via `DefaultAgntConfig()` — callers no need nil check

## Config Fields That Must Be Honored

Every parsed field needs consumer. Field in struct but nothing reads it = bug. Key fields + consumers:

| Field | Config path | Consumer | Purpose |
|-------|------------|----------|---------|
| `fallback-port` | `proxies.<name>.fallback-port` | Proxy creation fallback, port preflight | Target port when URL detection fails |
| `depends-on` | `scripts.<name>.depends-on` | Autostart ordering, ready signaler | Script launch order |
| `url-matchers` | `scripts.<name>.url-matchers` | URLTracker | Pattern to detect dev server URLs in output |
| `port-conflict` | `project.port-conflict` | Port preflight | Port conflict handling policy |
| `autostart` | `scripts.<name>.autostart` | RunAutostart | Launch on session connect or not |
| `enabled` | `shims.enabled` | `shims.Ensure`, daemon `routeShim` | Gate shim install + routing per project |
| `watch-script` | `shims.watch-script` | `shims.WatchScriptName` | Script restarted by restart-watch/quiesce actions |
| `rules` | `shims.rules.<name>` | `shims.Resolve` | Glob → action routing for shimmed commands |

## URL Matcher Validation

URL matchers (`url-matchers` field) = patterns like `"Now listening on: {url}"` URLTracker uses to find dev server URLs in process output. Common failure: pattern no match actual dev server output format.

When implement or modify URL matcher logic:
- `{url}` placeholder removed, remaining text used as regex
- Regex does substring match (not full-line)
- ANSI escape codes stripped before match
- Test matchers against real output from common dev servers (dotnet, vite, next, flask, etc.)

## Numeric Coercion — the bound-into-absence shape

Discovered task `01KYSZ89VXGFQD9GNG34WK1KB4` (commits `6fe81ecf`/`a75fd811`,
2026-08-18). `toSeconds` built a `time.Duration` as
`time.Duration(val) * time.Second`, which truncates a `float64` toward zero
*before* the unit multiply — so a `depends-on` timeout of `0.5`s collapsed to
`0`, and `0` is the sentinel for **wait indefinitely**. A finite limit silently
became its own removal. Fix: multiply in float space first —
`time.Duration(val * float64(time.Second))` — which preserves sub-unit
precision (0.5→500ms, 1.5→1.5s) while leaving the explicit `0` sentinel intact.

**Rule.** Any numeric config field where `0`/absent means *unbounded / disabled
/ forever* is a **bound-into-absence** hazard: a float→int or float→Duration
truncation can silently push a small real value into that sentinel. When a
field like this appears, sweep every sibling coercion in the package and
classify each:

- **Dangerous** — truncation can reach a sentinel that means *absence of a
  limit* (e.g. `depends-on` timeout `0` = wait forever; `settings.default-timeout`
  `0` = no timeout).
- **Safe** — truncation reaches a value the code re-clamps to a *positive*
  default (`graceful-timeout` truncates to 0 but `config.go` re-clamps ≤0 to
  5s; `OutageHold` windows fall to positive defaults). The danger is specific to
  `sentinel == unbounded`, not to truncation itself.

`kdl-go` **silently truncates a float into an `int` struct field with no error**
(verified: `kdl.Unmarshal("n 0.5", &struct{N int})` → `N=0, err=nil`). So any
`int` KDL field whose `0`/absent value disables a limit is a latent instance of
this bug — prefer a `float64` field with explicit validation, or a `>0` guard
whose fallback is a positive bound, not absence. Open sibling filed against
`settings.default-timeout` (int field, `0` = unbounded run): task
`01M0B1Y2D30F1J4GAQN6F3A6M2`.

When fixing such a field and the choice is WORK-vs-REFUSE, check the downstream
representation first: if the consumer already carries the needed precision (here
the sole consumer feeds `dep.Timeout` straight into `context.WithTimeout` as a
`time.Duration`), supporting the value is both the smaller diff and the honest
one — no new error surface.

## Cross-Platform Config

`platform.ShouldUseWindowsShell(path)` check determines shell resolution. Config parsing itself platform-independent, but fields like `run` (shell commands) and `cwd` (paths) interpreted differently:
- WSL + Windows filesystem path → `cmd.exe /c`
- Everything else → `sh -c`