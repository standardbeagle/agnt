# agnt Documentation

Engineering documentation for the `agnt` repository. User-facing docs are
published from `docs-site/` (Docusaurus). High-level project overview and the
canonical architecture invariants live in [`../CLAUDE.md`](../CLAUDE.md) and
[`../.claude/rules/`](../.claude/rules/).

## Reference

Current, maintained references for specific subsystems:

| Doc | Topic |
|-----|-------|
| [mcp-tools.md](mcp-tools.md) | MCP tool catalog, per-tool params, output formats, `__devtool` API, `agnt monitor` |
| [configuration.md](configuration.md) | `.agnt.kdl` config: port-conflict, autostart ordering, alert push, incident keys, URL tracking |
| [auth-breakout.md](auth-breakout.md) | Running OAuth/OIDC sign-in outside the content iframe |
| [channel-mode.md](channel-mode.md) | Channel Mode beta (push events via MCP `claude/channel`) |
| [overlay-internals.md](overlay-internals.md) | PTY overlay UI: command palette, ports/orphans panel, splash, output protection |
| [agent-adapters.md](agent-adapters.md) | System-prompt injection per AI tool (Claude Code, Gemini, Aider, …) |
| [agent-support-matrix.md](agent-support-matrix.md) | First-run setup skill delivery per agent (marketplace / skill-file / none) |
| [hook-dispatcher.md](hook-dispatcher.md) | `agnt hook` telemetry forwarder — events, drain fan-out, sample settings |
| [hook-rules.md](hook-rules.md) | `agnt hook check-bash`/`check-prompt` Bash-interceptor rules |
| [orphan-cleanup.md](orphan-cleanup.md) | Orphaned process / pgid cleanup behavior |
| [url-matching.md](url-matching.md) | Dev-server URL tracking and matching |
| [javascript-error-handling-standards.md](javascript-error-handling-standards.md) | Injected JS error-capture conventions |
| [visual-regression-usage.md](visual-regression-usage.md) | Snapshot tool usage guide |
| [visual-regression-spec.md](visual-regression-spec.md) | Snapshot tool technical spec |

## Project

| Doc | Topic |
|-----|-------|
| [roadmap.md](roadmap.md) | Forward-looking feature roadmap |
| [release.md](release.md) | Version management and release process |

## Archives

Historical design docs and specs, kept for context. These describe work as it
was planned; the code is the source of truth for current behavior.

- [`plans/`](plans/) — dated implementation/design plans (per feature)
- [`superpowers/plans/`](superpowers/plans/) and [`superpowers/specs/`](superpowers/specs/) — superpowers-workflow plans and specs
- [`spikes/`](spikes/) — exploratory spikes (e.g. go-pty evaluation)
- [`articles/`](articles/) — deep-dive technical write-ups (e.g. win32 ConPTY input mode)
