package aichannel

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLive_ClaudeSummarizerCLI runs the summarizer's CLI path against the real
// claude binary on PATH: Config defaults → ClaudeAdapter → PTY (ConPTY on
// Windows) → output parsing. It is the only check that the platform PTY start
// works with the real agent rather than a stand-in, and it spends tokens on
// the developer's account, so it is opt-in.
func TestLive_ClaudeSummarizerCLI(t *testing.T) {
	if os.Getenv("AGNT_LIVE_CLAUDE") == "" {
		t.Skip("set AGNT_LIVE_CLAUDE=1 to run the summarizer against the real claude CLI (spends tokens)")
	}

	ch := NewWithConfig(Config{Agent: AgentClaude, Timeout: 3 * time.Minute})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Multi-line prompt and context: the shape that a command line built
	// through cmd.exe cannot carry.
	out, err := ch.Send(ctx,
		"Reply with exactly the word LIVE_SUMMARY_OK and nothing else.\nThis prompt is deliberately\nmulti-line.",
		"context line 1\ncontext line 2")
	require.NoError(t, err)
	assert.Contains(t, out, "LIVE_SUMMARY_OK")
}
