# Agent Adapters

`agnt run` injects an agnt-aware system prompt into whatever AI coding CLI it wraps (Claude Code, Gemini, Copilot, Aider, Cursor, etc.). Each agent expects the prompt to arrive in a different way — Claude takes a `--append-system-prompt` flag, every other known agent gets the prompt as its first user message on stdin. The abstraction that captures that per-agent strategy is an **Adapter**.

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
- **Stdin-based.** `BuildArgs` returns `baseArgs` unchanged; `InitialStdin` returns the bytes to write to the child's stdin after `StdinDelay()` has elapsed. Example: Gemini, Aider, Cursor, etc.

## Built-in adapters

| Adapter name | Strategy | Notes |
|--------------|----------|-------|
| `claude` | flag | `--append-system-prompt <prompt>` |
| `gemini` | stdin | 500ms delay |
| `copilot` | stdin | 500ms delay |
| `aider` | stdin | 500ms delay |
| `cursor` | stdin | 500ms delay |
| `cursor-agent` | stdin | 500ms delay (checked before `cursor` for specificity) |
| `opencode` | stdin | 500ms delay |
| `kimi` | stdin | 500ms delay |
| `kimi-cli` | stdin | 500ms delay |
| `auggie` | stdin | 500ms delay |

All adapters match by **base name** of the command, case-insensitive, with `.exe` stripped on Windows. That covers bare invocations (`claude`), absolute paths (`/usr/bin/claude`, `C:\bin\claude.exe`), and relative paths (`./aider`). When the command is a shell alias or wrapper, the adapter also tries `exec.LookPath` and matches on the resolved path's base name.

## Per-agent overrides via `.agnt.kdl`

Project-level overrides live in the `ai.adapters` block:

```kdl
ai {
    adapters {
        claude {
            // Override the injection flag (e.g. if Claude renames it)
            flag-name "--system-prompt"
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
| `stdin-delay-ms` | int | stdin-based | Delay in milliseconds before writing the initial stdin. Ignored by flag adapters. A value of 0 inherits the adapter default (500ms). |

Unknown adapter names in the config are silently ignored — they do not error out autostart.

## Adding a new adapter

Say we want to add support for `newbot`, a stdin-based CLI that behaves like Aider.

1. **Register it.** In `internal/agentadapter/registry.go`, add a line to `DefaultRegistry`:

    ```go
    r.Register(newStdinAdapter("newbot", []string{"newbot"}))
    ```

    The second argument is the set of base-name aliases to match; a single entry equal to the canonical name is the common case. If `newbot` is sometimes invoked as `nb` or `new-bot`, add those here.

2. **Write a test.** Extend `internal/agentadapter/adapter_test.go` with a case confirming `DefaultRegistry().Lookup("newbot")` and `DefaultRegistry().Lookup("/usr/local/bin/newbot")` both resolve. The existing `TestDefaultRegistry_MatchesAllKnownAgents` table is the natural home — add `"newbot"` to the loop.

3. **If the agent needs a non-default strategy,** write a new adapter type in its own file (follow the pattern in `claude.go` or `stdin.go`). Flag-based adapters need to implement `BuildArgs`; stdin-based adapters need `InitialStdin` and `StdinDelay`.

4. **Done.** `run.go` and `run_windows.go` pick up the new agent automatically via `resolveAgentAdapter`, and users can override its behavior via `ai.adapters.newbot { … }` in `.agnt.kdl`.

## Testing adapters

The adapter package is pure Go with no PTY or filesystem dependencies, so tests are fast. Key cases the test suite covers:

- Match by bare name, absolute path, relative path, `.exe` suffix, and case-insensitive base name.
- `cursor` and `cursor-agent` resolve to distinct adapters (regression test for the old `strings.HasPrefix` trap).
- Flag-based `BuildArgs` appends `<flag> <prompt>` without mutating the caller's argv.
- Stdin-based `InitialStdin` contains both the agnt note and the prompt body, ends in a newline, and returns nil for an empty prompt.
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
