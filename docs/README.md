# agnt Documentation

Engineering documentation for the `agnt` repository. User-facing docs are
published from `docs-site/` (Docusaurus). High-level project overview and the
canonical architecture invariants live in [`../CLAUDE.md`](../CLAUDE.md) and
[`../.claude/rules/`](../.claude/rules/).

## Reference

Current, maintained references for specific subsystems:

| Doc | Topic |
|-----|-------|
| [agent-adapters.md](agent-adapters.md) | System-prompt injection per AI tool (Claude Code, Gemini, Aider, …) |
| [hook-rules.md](hook-rules.md) | `agnt hook` dispatcher events and drain fan-out |
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
