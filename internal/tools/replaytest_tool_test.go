package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resultText concatenates the text from a CallToolResult's content blocks.
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestReplaytestGate(t *testing.T) {
	gated := []string{"record", "stop", "refine", "replay", "explore"}
	free := []string{"list", "show"}

	missing := newReplaytestHandler(license.NewManager()) // unloaded => capability NOT granted
	for _, a := range gated {
		res, _, err := missing.handle(context.Background(), ReplaytestInput{Action: a, Name: "x"})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.True(t, res.IsError, "action %s should be license-blocked", a)
		assert.Contains(t, resultText(res), "activate")
	}
	for _, a := range free {
		res, _, err := missing.handle(context.Background(), ReplaytestInput{Action: a})
		require.NoError(t, err)
		assert.False(t, res.IsError, "action %s should be free", a)
	}

	res, _, err := missing.handle(context.Background(), ReplaytestInput{Action: "bogus"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
