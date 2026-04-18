package agentadapter

import "time"

// claudeDefaultFlag is the CLI flag Claude Code uses to accept an
// additional system prompt fragment. Exposed via the Override.FlagName
// hook so projects on a different Claude release can point at a renamed
// flag without waiting for an agnt release.
const claudeDefaultFlag = "--append-system-prompt"

// claudeAdapter injects the agnt system prompt into Claude Code via the
// command-line flag supported by the Claude CLI. This is the only
// flag-based adapter — every other AI agent we support is stdin-based.
type claudeAdapter struct {
	flag string
}

func newClaudeAdapter() *claudeAdapter {
	return &claudeAdapter{flag: claudeDefaultFlag}
}

func (c *claudeAdapter) Name() string { return "claude" }

func (c *claudeAdapter) Matches(command string) bool {
	_, ok := resolveBaseName(command, []string{"claude"})
	return ok
}

func (c *claudeAdapter) BuildArgs(baseArgs []string, prompt string) []string {
	if prompt == "" {
		// Preserve legacy behavior: no prompt means no injection at all.
		return cloneArgs(baseArgs)
	}
	flag := c.flag
	if flag == "" {
		flag = claudeDefaultFlag
	}
	out := make([]string, 0, len(baseArgs)+2)
	out = append(out, baseArgs...)
	out = append(out, flag, prompt)
	return out
}

func (c *claudeAdapter) InitialStdin(prompt string) []byte { return nil }

func (c *claudeAdapter) StdinDelay() time.Duration { return 0 }

func cloneArgs(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
