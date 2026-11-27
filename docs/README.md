# agnt Documentation

Engineering documentation for the `agnt` repository. The user-facing website is
`site/` (Docusaurus, deployed to dev.standardbeagle.com/agnt). High-level project overview and the
canonical architecture invariants live in [`../CLAUDE.md`](../CLAUDE.md) and
[`../.claude/rules/`](../.claude/rules/).

## Reference

Current, maintained references for specific subsystems:

| Doc | Topic |
|-----|-------|
| [mcp-tools.md](mcp-tools.md) | MCP tool catalog, per-tool params, output formats, `__devtool` API, `agnt monitor` |
| [configuration.md](configuration.md) | `.agnt.kdl` config: port-conflict, autostart ordering, alert push, incident keys, URL tracking, public feedback + `AGNT_PUBLIC_ADDR` |
| [remote-ssh.md](remote-ssh.md) | Remote sessions: quick start, reconnect, forwarding, bootstrap, push, troubleshooting |
| [public-walkthroughs.md](public-walkthroughs.md) | Publishing a walkthrough as a token-gated public webapp: secure defaults, lifecycle (create/rotate/revoke), anonymous feedback, incident response |
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
| [responsive-canonical-target.md](responsive-canonical-target.md) | Responsive frame as the canonical interaction target (shipped model) |
| [replaytest.md](replaytest.md) | Record, worker-mock and replay front-end tests |
| [licensing.md](licensing.md) | Pro licence keys and capability gating |
| [error-formats/](error-formats/) | Real error-message formats per toolchain, the classifier's pattern bank reference |
| [walkthroughs/](walkthroughs/) | Task-oriented developer walkthroughs |
| [agent-efficiency-benchmark.md](agent-efficiency-benchmark.md) | `internal/agentbench` metrics and the pinned baseline |
| [testing-flake-registry.md](testing-flake-registry.md) | Known flaky tests and their triage |
| [vendor-patches.md](vendor-patches.md) | Patches carried on vendored modules |

## Project

| Doc | Topic |
|-----|-------|
| [roadmap.md](roadmap.md) | Forward-looking feature roadmap |
| [release.md](release.md) | Version management and release process |

## Design records

Shipped designs that code cites as its contract (section numbers included), in [`design/`](design/):
remote SSH sessions, public walkthrough publish security (normative) and its subresource-proxying
follow-up, dev OIDC issuer, self-hosted licensing, walkthrough mode, and the opencode plugin.
[`design-mode-feature.md`](design-mode-feature.md) is the design-mode engineering doc.
[`articles/`](articles/) holds technical write-ups; [`fleet/prompts/`](fleet/prompts/) is read by
`.worktrack/templates.json`; [`solutions/`](solutions/) is the compound-engineering learnings store.

## Plans and in-flight work

Plans, specs and in-flight work are tracked in worktrack workspace `agnt`. A plan that an open task
cites is committed under `docs/plans/` while the task is open, and removed once it lands. Landed
plans are in git history (`git log --diff-filter=D -- docs/plans docs/superpowers`).
