package agentadapter

import (
	"strings"
	"time"
)

// stdinAdapter covers agents that need prompt delivery through stdin. Normal
// coding sessions do not call InitialStdin; their guidance is persisted to the
// agent's always-loaded context file (AGENTS.md, GEMINI.md, ...). Setup mode
// uses InitialStdin to send the one-shot setup prompt.
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
	// The trailing newline submits the prompt as input. Trim first so callers
	// can pass prompt strings that already end in one or more newlines.
	return []byte(strings.TrimRight(prompt, "\n") + "\n")
}

func (s *stdinAdapter) StdinDelay() time.Duration { return s.delay }
