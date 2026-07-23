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

## Cross-Platform Config

`platform.ShouldUseWindowsShell(path)` check determines shell resolution. Config parsing itself platform-independent, but fields like `run` (shell commands) and `cwd` (paths) interpreted differently:
- WSL + Windows filesystem path → `cmd.exe /c`
- Everything else → `sh -c`