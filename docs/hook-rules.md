# Hook Rules — Redirecting Bash to agnt MCP Tools

The `agnt hook check-bash` and `agnt hook check-prompt` subcommands are
Claude Code hook interceptors that nudge LLM behavior away from raw Bash
(`npm run dev`, `kill`, `lsof`, `tail -f`, `curl`, `grep error`) and
toward agnt's `run` / `proc` / `proxy` / `proxylog` / `get_errors` MCP
tools. They complement the existing `agnt hook` telemetry forwarder with
a push-model interception layer.

**Status**: The interceptor is CLI-only. The hook wiring itself is
distributed via the `standardbeagle-tools` plugin marketplace in
`plugins/agnt/hooks/hooks.json`; see the [Plugin Wiring](#plugin-wiring)
section below for the exact fragments to apply.

---

## Quick Reference

| Subcommand | Hook event | What it does | Exit behavior |
|------------|-----------|--------------|---------------|
| `agnt hook check-bash` | `PreToolUse` (Bash) | Match command against ruleset | 2 + stderr on block, 0 + stderr on soft-warn, 0 silent on allow |
| `agnt hook check-prompt` | `UserPromptSubmit` | Inject `<system-reminder>` on intent match | Always 0 |
| `agnt hook pre-tool-use` | `PreToolUse` (any) | Telemetry forward; chains into check-bash inline for Bash | 0 silent, or 2 when chained Bash rule blocks |
| `agnt hook rules list` | — | Print the merged ruleset | 0 |
| `agnt hook rules test --command '<cmd>'` | — | Dry-run a command through the matcher | 0 |
| `agnt hook rules test --prompt '<text>'` | — | Dry-run a prompt through the matcher | 0 |
| `agnt hook rules reload` | — | Reserved for future daemon-side rule cache | 0 |

All subcommands **fail open** on every error path (malformed JSON, scope
guard mismatch, regex failure, missing `.agnt.kdl`): a broken agnt install
must never block the agent's tool call.

---

## Built-in Bash Rules (9 patterns)

| # | Pattern | Action | Replacement |
|---|---------|--------|-------------|
| 1 | `(npm\|pnpm\|yarn\|bun) (run )?(dev\|start\|serve)` | **block** | `agnt.run {script_name: "dev"}` |
| 2 | `go run` | **block** | `agnt.proc {action: "run", name: "<script>"}` |
| 3 | `kill` / `killall` / `pkill` | **block** | `agnt.proc {action: "stop", name: "<script>"}` |
| 4 | `lsof -i` | soft-warn | `agnt.proc {action: "cleanup_port", port: N}` |
| 5 | `ss` / `netstat` `-…l…` | soft-warn | `agnt.proc {action: "list"}` |
| 6 | `tail -f` | **block** | `agnt.proxylog {action: "query"}` or `agnt.watch` |
| 7 | `curl … localhost` | soft-warn | `agnt.proxy {action: "exec", code: "…"}` |
| 8 | `grep … error` | soft-warn | `agnt.get_errors {}` |
| 9 | `ps aux \| grep` | soft-warn | `agnt.proc {action: "status", name: "<script>"}` |

The first match wins. Soft-warn rules emit a stderr nudge and return 0
(the tool call proceeds); block rules return exit 2 with a stderr message
that cites the replacement MCP invocation.

## Built-in Prompt Rules (2 patterns)

| Pattern | Reminder |
|---------|----------|
| `start.*(server\|dev\|app)` … | "Use agnt.run or agnt.proc to start dev servers …" |
| `(check\|view\|tail).*(logs?\|errors?)` | "Use agnt.get_errors or agnt.watch instead of tail -f …" |

Prompt rules are additive — a single prompt can trigger multiple reminders.

---

## Bypass Mechanisms

Three ways to bypass interception:

1. **Inline marker**: append `# agnt-allow` to the Bash command.
   ```bash
   npm run dev # agnt-allow
   ```
   This wins over every rule — use for one-off debugging only.

2. **Env variable**: set `AGNT_HOOK_BYPASS=1` (name configurable via KDL).
   Scoped to the agent process, so the whole session is opted out.

3. **Scope guard**: when the project directory has no `.agnt.kdl` at any
   parent level, interception is skipped entirely. This prevents false
   positives in unrelated repositories.

---

## KDL Overrides

Add a `hook-rules` block to `.agnt.kdl` to extend (never override)
builtins:

```kdl
hook-rules {
    bypass-env "AGNT_HOOK_BYPASS"
    bash-patterns {
        block-docker {
            pattern "(?m)(^|[;&|]\\s*)docker\\s+(run|compose)\\b"
            action "block"
            replacement "agnt.proc {action: \"run\", name: \"docker\"}"
            reason "Unmanaged docker containers leak ports."
        }
    }
    prompt-patterns {
        check-ports {
            pattern "(what|which)\\s+ports?"
            reminder "Use agnt.proc action:list to see managed ports."
        }
    }
}
```

- `action` must be `"allow"`, `"soft-warn"`, or `"block"`. Empty defaults
  to `"block"` to match builtin semantics.
- Pattern strings are Go regexes. `(?m)` multiline mode is common for
  Bash rules so `^` matches after `;` / `&&` separators.
- Prompt patterns are compiled with `(?i)` case-insensitive prefix by
  default.
- Invalid regexes are silently skipped on the hot path. Run
  `agnt hook rules list` to surface parse errors — it uses the strict
  loader that reports per-rule compile failures.

---

## Exit Code Contract

The subcommands follow the Claude Code hook protocol:

| Exit code | Meaning |
|-----------|---------|
| **0** | Allow the tool call. `stderr` text, if any, is shown to the developer but not the model. |
| **2** | Block the tool call. `stderr` is surfaced to the model on retry. |
| Other | Treated as "user configuration error" by the outer `agnt hook` dispatcher; reserved for argument validation (not emitted by the interceptor path). |

JSON stdout decisions (`{"decision": "block", "reason": "..."}`) are NOT
emitted by the current implementation. The exit-2 + stderr shape is
strictly clearer in both Claude Code logs and developer terminals and
interoperates with non-Claude harnesses.

---

## Plugin Wiring

The `agnt` Claude Code plugin lives in the `standardbeagle-tools`
marketplace repo, not this one. To wire the interceptor, the plugin
author should apply the following to `plugins/agnt/hooks/hooks.json`:

```json
{
  "hooks": {
    "preToolUse": [
      {
        "type": "command",
        "command": "agnt hook check-bash --project-path $PWD"
      },
      {
        "type": "command",
        "command": "agnt hook pre-tool-use --session-id $CLAUDE_SESSION_ID --project-path $PWD"
      }
    ],
    "userPromptSubmit": [
      {
        "type": "command",
        "command": "agnt hook check-prompt --project-path $PWD"
      }
    ]
  }
}
```

Both `preToolUse` entries can co-exist: check-bash runs first (exits 2
on block, stopping the chain); if allowed, `pre-tool-use` fires for
telemetry. Alternatively, use only `pre-tool-use` — it chains into the
check-bash logic inline and returns exit 2 on block via the same path.

### Skills Copy Updates

`plugins/agnt/skills/process-proxy.md` and `error-monitor.md` in the
marketplace repo should reference this doc and mention the interceptor
behavior, so end-users understand why `npm run dev` in a plain Bash
tool call gets redirected. Concrete copy suggestion for the
`process-proxy.md` preamble:

> **Bash redirection**: When the `agnt` hook is installed,
> `npm run dev`, `go run`, `kill`, and `tail -f` in plain Bash are
> intercepted and blocked with a stderr nudge pointing at the agnt MCP
> tool equivalent. See
> [`docs/hook-rules.md`](https://github.com/standardbeagle/agnt/blob/main/docs/hook-rules.md)
> for the full ruleset and bypass mechanisms.

---

## Performance

The matcher is benchmarked at `<1 µs` per call on the default ruleset
(see `BenchmarkCheckBash` in `internal/hookrules/rules_test.go`). The
end-to-end `agnt hook check-bash` budget is 1 second — fail-open
enforced via a `context.WithTimeout` at the CLI entry point.

---

## Authoring Checklist for New Rules

1. Start in `.agnt.kdl` as a KDL override. Ship built-in promotion only
   after enough production signal.
2. Prefer `(?m)` prefix + shell separator anchor (`(^|[;&|]\s*)`) so the
   rule fires across shell chains without false positives on substring
   hits (e.g. `my-npm-wrapper` should not match `npm run dev`).
3. Always cite a concrete `replacement` — the model retries against the
   stderr message, so vague "use agnt" nudges produce worse behavior.
4. Add a regression row to
   `internal/hookrules/rules_test.go::TestBashRegressionCorpus`
   covering at least one positive and one negative case.
