package main

import "fmt"

// buildSetupSystemPrompt builds the system prompt that drives the one-time
// `agnt run` setup phase. It is delivered to the coding agent identified by
// adapterName (e.g. "claude", "gemini") in place of the normal agnt cheat-sheet
// prompt.
//
// The per-agent install guidance comes from the support matrix
// (support_matrix.go / docs/agent-support-matrix.md). Agents with a skill
// mechanism (marketplace or skill-file) get a self-check + install instruction;
// agents with no skill mechanism (mechNone, e.g. aider) get an inline-setup
// prompt instead. The binary deliberately does NOT probe the agent's skill
// list — the agent and `agnt` are separate processes, so skill availability is
// only knowable from inside the agent. The self-check is a prompt instruction,
// not a binary capability.
func buildSetupSystemPrompt(adapterName string) string {
	support := lookupAgentSupport(adapterName)
	display := adapterName
	if display == "" {
		display = "the coding agent"
	}

	header := fmt.Sprintf(`# agnt first-run setup — this is your task right now

This project has no `+"`.agnt.kdl`"+` and agnt could not auto-detect a standard
layout (a simple package.json / dotnet / Go / Python project would have been
configured automatically), so %s is in one-time SETUP MODE.

Configuring agnt for this project IS the task — do it now. Do NOT ask the user
what to work on; do NOT wait for further instructions. Detect the project,
write `+"`.agnt.kdl`"+`, then confirm what you configured.`, display)

	confirm := "\n\n## Confirm the outcome\n\n" +
		"When setup finishes, a `.agnt.kdl` should exist at the project root.\n" +
		"Briefly confirm what was configured (scripts, proxies) so the user knows\n" +
		"the project is ready for a normal `agnt run` session.\n\n" +
		"Do not start dev servers or begin feature work in this setup session —\n" +
		"that happens after agnt relaunches you with autostart enabled."

	if support.Mechanism == mechNone {
		// No installable skill for this agent — drive setup inline.
		return header + "\n\n## How setup works for this agent\n\n" +
			support.InstallText + "\n\n" +
			"## Configure the project\n\n" +
			"Detect the project type (Go / Node / Python / …), then write a\n" +
			"`.agnt.kdl` at the project root registering the dev-server script(s)\n" +
			"and any reverse proxy. The `agnt:setup-project` skill describes the\n" +
			"full schema if you have access to it." + confirm
	}

	return header + "\n\n## Step 1 — self-check for the setup skill\n\n" +
		"Check whether the `agnt:setup-project` skill is available to you.\n" +
		"- If it IS available: run `agnt:setup-project` now and follow it to\n" +
		"  completion. It detects the project type, registers dev-server scripts\n" +
		"  and proxies, and writes `.agnt.kdl`.\n" +
		"- If it is NOT available, install it for this agent:\n  " +
		support.InstallText + "\n" +
		"  Then re-run setup. Tell the user the exact step and stop." + confirm
}
