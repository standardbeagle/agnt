# Agent Adapters

`agnt run` injects or persists agnt-aware guidance for whatever AI coding CLI it wraps (Claude Code, Gemini, Copilot, Aider, Cursor, etc.). Claude takes a `--append-system-prompt` flag. Other known agents use project context files during normal coding sessions, and stdin prompt delivery only for one-shot setup mode. The abstraction that captures that per-agent strategy is an **Adapter**.

This doc explains the adapter model, lists the built-in adapters, and walks through adding a new one.

## Why adapters

Before the adapter refactor, the injection logic lived inline in `cmd/agnt/run.go` and `cmd/agnt/run_windows.go`, with a hardcoded list of known agents and a split between "Claude" and "everyone else." That had three problems:

1. **Duplication.** The unix and windows entrypoints each carried their own copy of `isClaudeCommand` / `isKnownAIAgent` / `knownAgents` and drifted apart over time.
2. **No per-agent config.** Users could not override the injection flag (Claude sometimes renames them), change the stdin delay, or disable injection for a specific agent.
3. **Adding a new agent required editing both entrypoints.** There was no single place that said "here is the set of supported agents."

The `internal/agentadapter` package collapses all of that into one registry. `run.go` and `run_windows.go` now share a single line of adapter-aware injection logic.

## The Adapter interface

```go
type Adapter interface {
    Name() string
    Matches(command string) bool
    BuildArgs(baseArgs []string, prompt string) []string
    InitialStdin(prompt string) []byte
    StdinDelay() time.Duration
}
```

Two injection strategies are supported:

- **Flag-based.** `BuildArgs` appends an injection flag and the prompt to the argv; `InitialStdin` returns nil. Example: Claude Code (`--append-system-prompt`).
- **Stdin-capable.** `BuildArgs` returns `baseArgs` unchanged; `InitialStdin` returns the bytes to write to the child's stdin after `StdinDelay()` has elapsed. Normal coding sessions do not call it; setup mode uses it for one-shot inline setup prompts. Example: Gemini, Aider, Cursor, etc.

## Built-in adapters

| Adapter name | Strategy | Notes |
|--------------|----------|-------|
| `claude` | flag | `--append-system-prompt <prompt>` |
| `gemini` | context file + setup stdin | 500ms setup delay |
| `copilot` | context file + setup stdin | 500ms setup delay |
| `aider` | context file + setup stdin | 500ms setup delay |
| `cursor` | context file + setup stdin | 500ms setup delay |
| `cursor-agent` | context file + setup stdin | 500ms setup delay (checked before `cursor` for specificity) |
| `opencode` | context file + setup stdin | 500ms setup delay |
| `kimi` | file flag | `--agent-file <tmp>` |
| `kimi-cli` | file flag | `--agent-file <tmp>` |
| `auggie` | context file + setup stdin | 500ms setup delay |

All adapters match by **base name** of the command, case-insensitive, with `.exe` stripped on Windows. That covers bare invocations (`claude`), absolute paths (`/usr/bin/claude`, `C:\bin\claude.exe`), and relative paths (`./aider`). When the command is a shell alias or wrapper, the adapter also tries `exec.LookPath` and matches on the resolved path's base name.

## Prompt payload: what the adapter receives

Adapters are intentionally **dumb** about the prompt's contents — they forward the string `buildAgntSystemPrompt` returns, whatever it contains. That keeps the "which agent gets what" question out of each adapter and lets prompt authors evolve the payload in one place (`cmd/agnt/run.go`, `internal/config`, `internal/agntprompt`).

The payload is assembled, in order:

1. **Base prompt** from `internal/config.AgntConfig.BuildSystemPrompt` — agnt tool overview, configured scripts, configured proxies.
2. **`__devtool.*` helpers cheat sheet** from `internal/agntprompt.BuildCheatSheet` — a ~40-line compact reference of ~15 promoted helpers grouped by category (logging, inspection, layout, accessibility, audit, interactions, mutations). It steers agents toward `__devtool.*` instead of raw `document.*` / `window.*` / `getBoundingClientRect` calls when writing `proxy exec` snippets. Opt out with `ai { helpers-cheat-sheet false }` in `.agnt.kdl`.
3. **Runtime state** from the daemon (when reachable) — currently running processes and proxies.
4. **`ai.append-system-prompt`** (if set) — user-authored trailer appended last.

Every adapter receives **byte-identical** content for 1–4. Claude receives it through `--append-system-prompt <payload>`. Stdin-capable adapters persist it to their context file during normal coding sessions; setup mode can send its setup prompt through stdin. Regression tests in `cmd/agnt/cheatsheet_prompt_test.go` pin this invariant.

## Per-agent overrides via config

User-level defaults live in `~/.config/agnt/config.kdl`; project-level
overrides live in `<project>/.agnt.kdl`. Both use the same `ai.adapters`
block. User-level aliases are useful for personal shell aliases/wrappers such
as `cdsp`, while project config should only carry team/project-specific
behavior.

```kdl
ai {
    adapters {
        claude {
            // Override the injection flag (e.g. if Claude renames it)
            flag-name "--system-prompt"
            // Map a user wrapper/alias onto Claude's flag-based adapter
            aliases "cdsp"
        }
        aider {
            // Wait a little longer before sending the initial prompt
            stdin-delay-ms 1500
        }
        gemini {
            // Disable agnt-prompt injection for this agent entirely
            disabled true
        }
    }
}
```

Fields:

| KDL key | Type | Applies to | Purpose |
|---------|------|-----------|---------|
| `disabled` | bool | all | Skip prompt injection for this agent. `BuildArgs` returns `baseArgs` unchanged and `InitialStdin` returns nil. |
| `flag-name` | string | flag-based | Replace the injection flag. Ignored by stdin adapters. |
| `stdin-delay-ms` | int | stdin-capable | Delay in milliseconds before writing setup-mode stdin. Ignored by flag adapters and normal coding sessions. A value of 0 inherits the adapter default (500ms). |

Unknown adapter names in the config are silently ignored — they do not error out autostart.

## Adding a new adapter

Say we want to add support for `newbot`, a stdin-based CLI that behaves like Aider.

1. **Register it.** In `internal/agentadapter/registry.go`, add a line to `DefaultRegistry`:

    ```go
    r.Register(newStdinAdapter("newbot", []string{"newbot"}))
    ```

    The second argument is the set of base-name aliases to match; a single entry equal to the canonical name is the common case. If `newbot` is sometimes invoked as `nb` or `new-bot`, add those here.

2. **Write a test.** Extend `internal/agentadapter/adapter_test.go` with a case confirming `DefaultRegistry().Lookup("newbot")` and `DefaultRegistry().Lookup("/usr/local/bin/newbot")` both resolve. The existing `TestDefaultRegistry_MatchesAllKnownAgents` table is the natural home — add `"newbot"` to the loop.

3. **If the agent needs a non-default strategy,** write a new adapter type in its own file (follow the pattern in `claude.go`, `kimi.go`, or `stdin.go`). Flag-based adapters need to implement `BuildArgs`; stdin-capable adapters need `InitialStdin` and `StdinDelay`.

4. **Done.** `run.go` and `run_windows.go` pick up the new agent automatically via `resolveAgentAdapter`, and users can override its behavior via `ai.adapters.newbot { … }` in `.agnt.kdl`.

## Testing adapters

The adapter package is pure Go with no PTY or filesystem dependencies, so tests are fast. Key cases the test suite covers:

- Match by bare name, absolute path, relative path, `.exe` suffix, and case-insensitive base name.
- `cursor` and `cursor-agent` resolve to distinct adapters (regression test for the old `strings.HasPrefix` trap).
- Flag-based `BuildArgs` appends `<flag> <prompt>` without mutating the caller's argv.
- Stdin-capable `InitialStdin` returns the prompt body with exactly one trailing newline and returns nil for an empty prompt. Normal coding sessions should rely on the persistent context file instead of calling it.
- `Override.Disabled` blanks both channels; `Override.FlagName` retargets Claude's flag; `Override.StdinDelay` overrides the default 500ms.

Run them with:

```bash
go test ./internal/agentadapter/...
```

## Where the pieces live

- `internal/agentadapter/adapter.go` — the `Adapter` interface and the default stdin delay constant.
- `internal/agentadapter/registry.go` — `Registry`, `Override`, `DefaultRegistry`, base-name helpers.
- `internal/agentadapter/claude.go` — the one flag-based adapter.
- `internal/agentadapter/stdin.go` — the shared stdin-based adapter implementation.
- `internal/agentadapter/override.go` — override wrapper that layers `Override` on top of any adapter.
- `cmd/agnt/adapter_resolve.go` — glue between `.agnt.kdl` config and the registry; shared by `run.go` and `run_windows.go`.
- `internal/config/agnt.go` — `AIAdapterConfig` KDL struct.
