package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

// defaultSkillsSource is the GitHub owner/repo passed to `npx skills add`.
// The agnt agent skills live in the standardbeagle-tools marketplace repo.
const defaultSkillsSource = "standardbeagle-tools/agnt"

// defaultSkillsAgent is the skills-CLI agent name targeted by default.
const defaultSkillsAgent = "claude-code"

var (
	skillsAgent  string
	skillsSource string
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Install the agnt agent skills and register the agnt MCP server",
	Long: `Install the agnt agent skills and register the agnt MCP server.

skills bootstraps agnt for a coding agent in two steps:

  1. Installs the agnt agent skills using Vercel's open skills CLI:
       npx -y skills add standardbeagle-tools/agnt --all -a claude-code
  2. Registers the agnt MCP server. For Claude Code this runs:
       claude mcp add agnt -s user -- agnt mcp
     For other agents it prints the MCP server config to add.

Requires Node.js (npx) on PATH. Targets claude-code by default; use --agent to
install the skills for a different agent (any skills-CLI agent name).

Examples:
  agnt skills                  # install for claude-code + register MCP
  agnt skills --agent cursor   # install for cursor, print MCP config
  agnt skills --source owner/repo   # override the skills source`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSkillsSetup(skillsSource, skillsAgent)
	},
}

func init() {
	skillsCmd.Flags().StringVarP(&skillsAgent, "agent", "a", defaultSkillsAgent,
		"target agent for skill install (skills-CLI agent name, e.g. claude-code, cursor, opencode)")
	skillsCmd.Flags().StringVar(&skillsSource, "source", defaultSkillsSource,
		"skills source passed to `npx skills add <source>`")
}

// skillsInstallArgs builds the argv (after the `npx` program name) for the
// Vercel skills CLI install. `-y` auto-confirms the npx package fetch; `--all`
// installs every skill in the source non-interactively.
func skillsInstallArgs(source, agent string) []string {
	return []string{"-y", "skills", "add", source, "--all", "-a", agent}
}

// mcpRegisterArgs builds the argv (after the `claude` program name) that
// registers the agnt MCP server at user scope.
func mcpRegisterArgs() []string {
	return []string{"mcp", "add", "agnt", "-s", "user", "--", "agnt", "mcp"}
}

// isClaudeAgent reports whether agent is one of Claude Code's skills-CLI names,
// for which MCP registration runs via the `claude` CLI.
func isClaudeAgent(agent string) bool {
	return agent == "claude-code" || agent == "claude"
}

// runSkillsSetup installs the agnt skills via the Vercel skills CLI, then
// registers the agnt MCP server. Missing required tools fail loud; an
// already-registered MCP server is reported but not fatal (the skills install
// has already succeeded).
func runSkillsSetup(source, agent string) error {
	if _, err := exec.LookPath("npx"); err != nil {
		return fmt.Errorf("npx not found on PATH — install Node.js to use `agnt skills`: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Installing agnt skills from %s for %s…\n", source, agent)
	if err := runStreaming("npx", skillsInstallArgs(source, agent)...); err != nil {
		return fmt.Errorf("`npx skills add %s` failed: %w", source, err)
	}

	if !isClaudeAgent(agent) {
		fmt.Fprintf(os.Stdout,
			"\nSkills installed. To register the agnt MCP server for %s, add to its MCP config:\n"+
				"  \"agnt\": {\"command\": \"agnt\", \"args\": [\"mcp\"]}\n", agent)
		return nil
	}

	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stdout,
			"\nSkills installed. claude CLI not found — register the MCP server manually:\n"+
				"  claude mcp add agnt -s user -- agnt mcp")
		return nil
	}
	fmt.Fprintln(os.Stdout, "\nRegistering the agnt MCP server with Claude Code…")
	if err := runStreaming("claude", mcpRegisterArgs()...); err != nil {
		// Most common cause: agnt is already registered. Surface it but don't
		// fail the whole setup — the skills install already succeeded.
		fmt.Fprintf(os.Stderr, "`claude mcp add agnt` did not complete (already registered?): %v\n", err)
	}
	return nil
}

// runStreaming runs name+args with the parent's stdio attached so subprocess
// output streams live and any interactive prompts work.
func runStreaming(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
