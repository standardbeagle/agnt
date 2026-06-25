package agentadapter

import (
	"time"
)

// stdinAdapter covers every non-Claude AI coding agent. It injects a brief
// one-line note as the agent's first user message, written to the child's
// stdin after StdinDelay. The FULL agnt guidance is NOT injected here — it is
// persisted to the agent's always-loaded context file (AGENTS.md, GEMINI.md, …)
// by writePersistentContext, so the stdin nudge stays a short pointer instead
// of dumping the whole cheat-sheet into the conversation.
type stdinAdapter struct {
	name    string
	aliases []string
	delay   time.Duration
}

// newStdinAdapter constructs a stdin-based adapter. `name` is the
// canonical lowercase identifier used in config overrides and logging;
// `aliases` is the set of base names that should match (typically just
// one entry equal to name, but e.g. future aliases like "gcp-cli" can
// go here).
func newStdinAdapter(name string, aliases []string) *stdinAdapter {
	if len(aliases) == 0 {
		aliases = []string{name}
	}
	return &stdinAdapter{
		name:    name,
		aliases: aliases,
		delay:   DefaultStdinDelay,
	}
}

func (s *stdinAdapter) Name() string { return s.name }

func (s *stdinAdapter) Matches(command string) bool {
	_, ok := resolveBaseName(command, s.aliases)
	return ok
}

func (s *stdinAdapter) BuildArgs(baseArgs []string, prompt string) []string {
	return cloneArgs(baseArgs)
}

func (s *stdinAdapter) InitialStdin(prompt string) []byte {
	if prompt == "" {
		return nil
	}
	// Keep this to ONE short line: it lands as the agent's first user message,
	// so dumping the full prompt here is noise. The prompt argument only gates
	// whether to inject at all (empty = injection disabled); its body is
	// delivered via the persistent context file, not stdin. The trailing
	// newline submits the line as input.
	return []byte("Running under agnt: use the `agnt` MCP server tools instead of shell — run dev servers and long builds with `proc` (not Bash), and use proxy/get_errors/currentpage for browser debugging. Full guidance is in your project context file (AGENTS.md).\n")
}

func (s *stdinAdapter) StdinDelay() time.Duration { return s.delay }
