package main

// installMechanism classifies how the agnt:setup-project skill is delivered to
// a given coding agent. See docs/agent-support-matrix.md for the researched
// classification and per-agent sources (≥2 each).
type installMechanism string

const (
	// mechMarketplace: first-class plugin marketplace, installed by name.
	mechMarketplace installMechanism = "marketplace-install"
	// mechSkillFile: no marketplace; reads skills/commands from a known dir.
	mechSkillFile installMechanism = "skill-file"
	// mechNone: no invocable skill mechanism; setup runs via inline prompt.
	mechNone installMechanism = "none"
)

// agentSupport is one row of the support matrix: how to install (or substitute
// for) the agnt:setup-project skill on a specific agent.
type agentSupport struct {
	Mechanism   installMechanism
	InstallText string
}

// supportMatrix maps an adapter name (agentadapter.Adapter.Name()) to its
// install guidance. Sourced from docs/agent-support-matrix.md — keep the two in
// sync. codex/qwen/crush are included for forward-compatibility even though
// they are not yet registered adapters (see the matrix doc's Slice C notes).
var supportMatrix = map[string]agentSupport{
	"claude": {
		Mechanism:   mechMarketplace,
		InstallText: "Run `/plugin marketplace add standardbeagle/agnt` then `/plugin install agnt` in Claude Code. The `agnt:setup-project` skill becomes available immediately.",
	},
	"gemini": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.gemini/commands/agnt/setup-project.toml` (or `.md`) in your project, or `~/.gemini/commands/` for global, with the agnt setup prompt; invoke it as `/agnt:setup-project`.",
	},
	"copilot": {
		Mechanism:   mechSkillFile,
		InstallText: "Save the agnt setup instructions as a custom agent at `.github/agents/agnt-setup.agent.md` (or add them to `AGENTS.md`); invoke with `copilot --agent=agnt-setup`.",
	},
	"codex": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.agents/skills/agnt-setup-project/SKILL.md` in your repo (or `~/.agents/skills/` globally) with the agnt setup instructions; Codex auto-discovers it.",
	},
	"opencode": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.opencode/commands/agnt-setup-project.md` (or `~/.config/opencode/commands/` global) with the agnt setup prompt as the template; run `/agnt-setup-project`.",
	},
	"cursor-agent": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.cursor/commands/agnt-setup-project.md` (or `~/.cursor/commands/` global) with the agnt setup prompt, or drop the instructions into `AGENTS.md` / `.cursor/rules/` which the CLI reads; invoke `/agnt-setup-project`.",
	},
	"cursor": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.cursor/commands/agnt-setup-project.md` (or `~/.cursor/commands/` global) with the agnt setup prompt, or drop the instructions into `AGENTS.md` / `.cursor/rules/` which the CLI reads; invoke `/agnt-setup-project`.",
	},
	"qwen": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.qwen/commands/agnt/setup-project.md` (or `~/.qwen/commands/` global) with the agnt setup prompt and a `description` frontmatter; invoke `/agnt:setup-project`.",
	},
	"crush": {
		Mechanism:   mechSkillFile,
		InstallText: "Create a `agnt-setup-project/SKILL.md` skill folder under `.crush/skills/` (or `~/.config/crush/skills/`) with the agnt setup instructions and `user-invocable: true`.",
	},
	"kimi-cli": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.kimi/skills/agnt-setup-project/SKILL.md` (or `~/.kimi/skills/` global) with the agnt setup instructions and `name`/`description` frontmatter; invoke `/skill:agnt-setup-project`.",
	},
	"auggie": {
		Mechanism:   mechSkillFile,
		InstallText: "Create `.augment/commands/agnt-setup-project.md` (or `~/.augment/commands/` global) with the agnt setup prompt and a `description` frontmatter; invoke `/agnt-setup-project`.",
	},
	"aider": {
		Mechanism:   mechNone,
		InstallText: "Aider has no installable skill — agnt drives setup via an inline prompt this session. Optionally persist guidance as `CONVENTIONS.md` and load it with `aider --read CONVENTIONS.md`.",
	},
}

// genericAgentSupport is returned for agents not in the matrix: the conservative
// advice is to look for a skills/commands directory or marketplace, and fall
// back to the inline prompt when neither exists.
var genericAgentSupport = agentSupport{
	Mechanism:   mechSkillFile,
	InstallText: "Check whether your agent supports a skills/commands directory or a plugin marketplace; if so, install the `agnt:setup-project` skill there. Otherwise, follow the setup instructions in this prompt directly.",
}

// lookupAgentSupport returns the install guidance for adapterName, falling back
// to generic advice for any agent not in the matrix.
func lookupAgentSupport(adapterName string) agentSupport {
	if s, ok := supportMatrix[adapterName]; ok {
		return s
	}
	return genericAgentSupport
}
