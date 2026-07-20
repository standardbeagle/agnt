# Agent Support Matrix — `agnt:setup-project` Skill Delivery

How the `agnt:setup-project` skill (or its setup instructions) reaches each
supported terminal coding agent. This drives the first-run setup flow
(`agnt run` with no `.agnt.kdl`): for agents with a real install mechanism we
tell the user how to install the skill; for agents without one, agnt drives
setup via an inline system prompt (the same path the stdin adapters already
use).

> **Source of truth + maintenance.** Each row's classification is load-bearing
> (it changes the install text we show users), so every agent carries **≥2
> independent sources** per the dev-standards multi-source rule. Agent CLIs
> evolve fast — re-verify a row's sources before relying on it, and bump the
> `confidence` when a mechanism changes. Researched 2026-05-29.

## Classification buckets

| Bucket | Meaning | agnt behavior |
|--------|---------|---------------|
| `marketplace-install` | First-class plugin/extension marketplace; skill installed by name | Tell user to add the marketplace + install the plugin |
| `skill-file` | No marketplace, but reads skills/commands from a known file or directory | Tell user to drop the skill/command file in the agent's dir |
| `none` | No documented invocable skill/command mechanism | Drive setup via inline system prompt (no install step) |

## Matrix

| Agent | agnt adapter | Classification | Mechanism (one line) | Confidence |
|-------|-------------|----------------|----------------------|-----------|
| claude | flag (`--append-system-prompt`) | `marketplace-install` | `/plugin marketplace add` + `/plugin install`; also `.claude/skills/` | high |
| gemini | stdin | `skill-file` | `~/.gemini/commands/` or `.gemini/commands/` TOML/`.md` → `/ns:cmd` | high |
| copilot | stdin | `skill-file` | `.github/agents/*.agent.md` or `~/.copilot/agents/`; `AGENTS.md` | high |
| codex | context file + setup stdin fallback | `skill-file` | `.agents/skills/<name>/SKILL.md` or `~/.agents/skills/` | high |
| opencode | stdin | `skill-file` | `.opencode/commands/*.md` or `~/.config/opencode/commands/` | high |
| cursor-agent | stdin | `skill-file` | `.cursor/commands/*.md` (+ `AGENTS.md`/`.cursor/rules/` for CLI) | medium |
| qwen | context file + setup stdin fallback | `skill-file` | `~/.qwen/commands/` or `.qwen/commands/` `.md`/frontmatter → `/ns:cmd` | high |
| crush | context file + setup stdin fallback | `skill-file` | `.crush/skills/<name>/SKILL.md` or `~/.config/crush/skills/`; `user-invocable` | high |
| kimi-cli | context file + setup stdin fallback | `skill-file` | `.kimi/skills/<name>/SKILL.md` or `~/.kimi/skills/` → `/skill:<name>` | high |
| auggie | stdin | `skill-file` | `.augment/commands/*.md` or `~/.augment/commands/` `.md`/frontmatter | high |
| aider | stdin | `none` | No invocable command/skill; optional read-only `CONVENTIONS.md` context file | medium |

Registry today (`internal/agentadapter/registry.go`): claude (flag), gemini,
copilot, aider, cursor-agent, cursor, opencode, auggie, codex, qwen, crush, and
kimi-cli (context file with a conservative stdin fallback).

Two unrelated CLIs install as `kimi`, so the shared adapter deliberately adds
no agent-specific CLI flag. In setup mode agnt writes the task into the
startup-loaded `AGENTS.md`; stdin remains a fallback if that write is disabled
or fails. This avoids the rejected `--agent-file` regression and TUI paste
flows that require a second manual Enter. See `docs/agent-adapters.md`.

## Per-agent detail

### claude — Claude Code — `marketplace-install` (high)
- **Mechanism**: `/plugin marketplace add <repo>` then `/plugin install <name>@<marketplace>` — first-class plugin marketplace bundling skills/agents/commands/MCP. Also supports drop-in `.claude/skills/`.
- **Install text**: "Run `/plugin marketplace add standardbeagle/agnt` then `/plugin install agnt` in Claude Code. The `agnt:setup-project` skill becomes available immediately."
- **Sources**:
  - https://code.claude.com/docs/en/discover-plugins — official: marketplace add + `/plugin` install flow
  - https://docs.litellm.ai/docs/tutorials/claude_code_plugin_marketplace — third-party: `/plugin install <name>@marketplace` + `.claude/skills/` scope

### gemini — Gemini CLI — `skill-file` (high)
- **Mechanism**: Command file in `~/.gemini/commands/` (user) or `.gemini/commands/` (project); TOML (`prompt`/`description`) or Markdown; subdir = `namespace:command`. No marketplace.
- **Install text**: "Create `.gemini/commands/agnt/setup-project.toml` (or `.md`) in your project (or `~/.gemini/commands/` for global) with the agnt setup prompt; invoke `/agnt:setup-project`."
- **Sources**:
  - https://geminicli.com/docs/cli/custom-commands/ — official: `~/.gemini/commands/` + TOML/`{{args}}`
  - https://cloud.google.com/blog/topics/developers-practitioners/gemini-cli-custom-slash-commands — Google blog: TOML format, namespacing, git share

### copilot — GitHub Copilot CLI — `skill-file` (high)
- **Mechanism**: No per-command slash files; reusable units are **custom agents** (`.agent.md` in `.github/agents/` or `~/.copilot/agents/`) plus instruction files (`AGENTS.md`, `.github/instructions/**/*.instructions.md`). No marketplace.
- **Install text**: "Save the agnt setup instructions as a custom agent at `.github/agents/agnt-setup.agent.md` (or add them to `AGENTS.md`); invoke with `copilot --agent=agnt-setup`."
- **Sources**:
  - https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/create-custom-agents-for-cli — official: `.agent.md` in `.github/agents/` and `~/.copilot/agents/`
  - https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/add-custom-instructions — official: `AGENTS.md` + `.instructions.md` discovery
- **Caveat**: closest reusable mechanism is custom agents / instruction files, not a slash-command file — hence `skill-file`, not `marketplace-install`.

### codex — OpenAI Codex CLI — `skill-file` (high)
- **Mechanism**: **Skills** = directory with `SKILL.md` (name+description frontmatter) in `.agents/skills/` (repo) or `~/.agents/skills/` (user); `$skill-installer` adds curated ones. Legacy `~/.codex/prompts/*.md` slash-prompts deprecated in favor of skills. No central marketplace.
- **Install text**: "Create `.agents/skills/agnt-setup-project/SKILL.md` in your repo (or `~/.agents/skills/` globally) with the agnt setup instructions; Codex auto-discovers it. (Legacy: `~/.codex/prompts/setup-project.md` → `/prompts:setup-project`, deprecated.)"
- **Sources**:
  - https://developers.openai.com/codex/skills — official: `.agents/skills/` + `SKILL.md`, `$skill-installer`
  - https://developers.openai.com/codex/custom-prompts — official: deprecated `~/.codex/prompts/*.md`, "use skills"

### opencode — `skill-file` (high)
- **Mechanism**: Markdown command file in `.opencode/commands/` (project) or `~/.config/opencode/commands/` (user); filename = command name, `template`/`description` frontmatter. Also `skills/`. No marketplace.
- **Install text**: "Create `.opencode/commands/agnt-setup-project.md` (or `~/.config/opencode/commands/` global) with the agnt setup prompt as the template; run `/agnt-setup-project`."
- **Sources**:
  - https://opencode.ai/docs/commands/ — official: `.opencode/commands/` + `~/.config/opencode/commands/`, filename→command
  - https://opencode.ai/docs/config/ — official: `OPENCODE_CONFIG_DIR` + `commands/`/`skills/` dirs

### cursor-agent — Cursor CLI — `skill-file` (medium)
- **Mechanism**: Markdown commands in `.cursor/commands/` (project) or `~/.cursor/commands/` (global), filename = `/command`; CLI also honors `.cursor/rules/` and `AGENTS.md`. Supports `SKILL.md` skills. No marketplace.
- **Install text**: "Create `.cursor/commands/agnt-setup-project.md` (or `~/.cursor/commands/` global) with the agnt setup prompt; invoke `/agnt-setup-project`. Or drop the instructions into `AGENTS.md` / `.cursor/rules/`, which the CLI reads."
- **Sources**:
  - https://cursor.com/docs/cli/using — official: CLI reads `.cursor/rules` + `AGENTS.md`/`CLAUDE.md`
  - https://cursor.com/changelog/1-6 — official changelog: custom commands as `.cursor/commands/` Markdown
  - https://cursor.com/docs/context/commands — official: `.cursor/commands/*.md`, filename→slash command, `~/.cursor/commands/` global
- **Caveat**: `.cursor/commands/` is documented, but the CLI docs only explicitly guarantee `rules`/`AGENTS.md` parity for the CLI; `AGENTS.md`/`.cursor/rules/` is the more reliable install target for cursor-agent specifically.

### qwen — Qwen Code CLI — `skill-file` (high)
- **Mechanism**: Command file in `~/.qwen/commands/` (user) or `.qwen/commands/` (project); Markdown + optional YAML frontmatter (TOML deprecated but supported); subdir = `namespace:command`. No marketplace.
- **Install text**: "Create `.qwen/commands/agnt/setup-project.md` (or `~/.qwen/commands/` global) with the agnt setup prompt and a `description` frontmatter; invoke `/agnt:setup-project`."
- **Sources**:
  - https://qwenlm.github.io/qwen-code-docs/en/users/features/commands/ — official: `~/.qwen/commands/` + `.qwen/commands/`, Markdown/frontmatter
  - https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/commands.md — official repo docs: namespacing, TOML-deprecated note

### crush — Charm Crush — `skill-file` (high)
- **Mechanism**: Agent Skills (open standard) = folder with `SKILL.md`; discovered from `.crush/skills/`, `.agents/skills/`, `.claude/skills/`, `.cursor/skills/` (project) or `~/.config/crush/skills/`, `~/.config/agents/skills/`, `~/.claude/skills/` (user). `user-invocable: true` surfaces it in the Ctrl+P palette. No marketplace.
- **Install text**: "Create a `agnt-setup-project/SKILL.md` skill folder under `.crush/skills/` (or `~/.config/crush/skills/`) with the agnt setup instructions; add `user-invocable: true`. Crush also reads `CRUSH.md`/`AGENTS.md`."
- **Sources**:
  - https://github.com/charmbracelet/crush/blob/main/README.md — official repo: skill dirs, `SKILL.md`, `user-invocable`, `AGENTS.md`/`CRUSH.md`
  - https://deepwiki.com/charmbracelet/crush/2-getting-started — third-party: config + AGENTS.md/CRUSH.md instructions

### kimi-cli — Kimi Code CLI (MoonshotAI) — `skill-file` (high)
- **Mechanism**: Agent Skills = folder with `SKILL.md`; project `.kimi/skills/` or `.agents/skills/`, user `~/.kimi/skills/` or `~/.config/agents/skills/`; invoked via `/skill:<name>`. No marketplace.
- **Install text**: "Create `.kimi/skills/agnt-setup-project/SKILL.md` (or `~/.kimi/skills/` global) with the agnt setup instructions and `name`/`description` frontmatter; invoke `/skill:agnt-setup-project`."
- **Sources**:
  - https://moonshotai.github.io/kimi-cli/en/customization/skills.html — official: skill dirs, `SKILL.md`, priority order
  - https://moonshotai.github.io/kimi-cli/en/reference/slash-commands.html — official: `/skill:<name>` invocation reads `SKILL.md`

### auggie — Augment Auggie CLI — `skill-file` (high)
- **Mechanism**: Custom slash commands as Markdown+frontmatter in `.augment/commands/` (workspace) or `~/.augment/commands/` (user), filename = `/command`, subdir = `namespace:command`; also `.augment/rules/` and `AGENTS.md`. No marketplace.
- **Install text**: "Create `.augment/commands/agnt-setup-project.md` (or `~/.augment/commands/` global) with the agnt setup prompt and a `description` frontmatter; invoke `/agnt-setup-project`."
- **Sources**:
  - https://docs.augmentcode.com/cli/custom-commands — official: `.augment/commands/`, `.md`+frontmatter, namespacing
  - https://docs.augmentcode.com/setup-augment/guidelines — official: `.augment/rules/` + `AGENTS.md`/`CLAUDE.md` discovery

### aider — `none` (medium)
- **Mechanism**: No custom slash commands or invocable skills. Reusable project instructions go in a `CONVENTIONS.md` loaded via `--read CONVENTIONS.md` or `read:` in `.aider.conf.yml` — a read-only context file, not a command. For agnt's setup flow this means **inline prompt** (aider is a stdin adapter), with `CONVENTIONS.md` as an optional manual context file.
- **Install text**: "Aider has no installable skill — agnt drives setup via an inline prompt. Optionally persist guidance as `CONVENTIONS.md` and load it with `aider --read CONVENTIONS.md` (or `read: CONVENTIONS.md` in `.aider.conf.yml`)."
- **Sources**:
  - https://aider.chat/docs/usage/conventions.html — official: `CONVENTIONS.md` + `/read` / `--read`
  - https://aider.chat/docs/config/aider_conf.html — official: `read:` config key
- **Caveat**: classified `none` for the strict "invocable skill/command" sense (matches agnt's inline-prompt delivery via the stdin adapter). The `CONVENTIONS.md` path is the loose `skill-file` interpretation if Slice C prefers it.

## Key findings (for Slice C)

1. **Claude Code is the only `marketplace-install`.** Every other agent is
   `skill-file` (drop a Markdown/TOML/`SKILL.md` file) except **aider**
   (`none` — inline prompt / read-only context file only).
2. **`SKILL.md` "Agent Skills" is converging into an open standard.** codex,
   crush, and kimi-cli all adopt it, and several agents (crush, kimi, cursor,
   codex) read shared dirs like `.agents/skills/` and `.claude/skills/`. A
   single `SKILL.md` could be portable across multiple agents — Slice C could
   emit one shared skill file plus per-agent install text rather than N copies.
3. **All researched context-file agents are registered.** Their named adapters
   let setup select the correct startup-loaded file while retaining stdin as a
   write-failure fallback.
4. **Two medium-confidence rows** (aider, cursor-agent) carry caveats above;
   re-verify before shipping user-facing install text for them.
