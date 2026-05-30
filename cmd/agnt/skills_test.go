package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSkillsInstallArgs pins the Vercel skills-CLI invocation: -y auto-confirms
// the npx fetch, --all installs non-interactively, -a targets the agent.
func TestSkillsInstallArgs(t *testing.T) {
	got := skillsInstallArgs("standardbeagle-tools/agnt", "claude-code")
	assert.Equal(t, []string{"-y", "skills", "add", "standardbeagle-tools/agnt", "--all", "-a", "claude-code"}, got)

	// Source + agent are threaded through verbatim.
	custom := skillsInstallArgs("owner/repo", "cursor")
	assert.Equal(t, "owner/repo", custom[3])
	assert.Equal(t, "cursor", custom[len(custom)-1])
	assert.Contains(t, custom, "--all")
	assert.Equal(t, "-y", custom[0], "must auto-confirm the npx package fetch")
}

// TestMcpRegisterArgs pins the user-scope agnt MCP registration argv.
func TestMcpRegisterArgs(t *testing.T) {
	got := mcpRegisterArgs()
	assert.Equal(t, []string{"mcp", "add", "agnt", "-s", "user", "--", "agnt", "mcp"}, got)
	// The `--` separator must precede the served command so flags aren't
	// swallowed by `claude mcp add`.
	sep := -1
	for i, a := range got {
		if a == "--" {
			sep = i
		}
	}
	assert.Equal(t, []string{"agnt", "mcp"}, got[sep+1:], "served command follows the -- separator")
}

// TestIsClaudeAgent: only Claude Code names route MCP registration through the
// `claude` CLI; everything else gets the printed-config branch.
func TestIsClaudeAgent(t *testing.T) {
	for _, a := range []string{"claude-code", "claude"} {
		assert.True(t, isClaudeAgent(a), "%s should be a claude agent", a)
	}
	for _, a := range []string{"cursor", "opencode", "gemini", "", "claude-code-x"} {
		assert.False(t, isClaudeAgent(a), "%s should not be a claude agent", a)
	}
}

// TestSkillsDefaults locks the defaults the user selected: source is the
// standardbeagle-tools/agnt repo, default agent is claude-code.
func TestSkillsDefaults(t *testing.T) {
	assert.Equal(t, "standardbeagle-tools/agnt", defaultSkillsSource)
	assert.Equal(t, "claude-code", defaultSkillsAgent)
	assert.True(t, strings.Contains(defaultSkillsSource, "standardbeagle-tools"))
}
