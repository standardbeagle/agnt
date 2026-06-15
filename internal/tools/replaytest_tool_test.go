package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/agnt/internal/replaytest"
	"github.com/standardbeagle/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grantingManager builds a license.Manager that grants advanced_testing.
func grantingManager(t *testing.T) *license.Manager {
	t.Helper()
	p := &license.Payload{
		Email:        "test@example.com",
		IssuedAt:     time.Now(),
		Expiry:       time.Now().Add(24 * time.Hour),
		Capabilities: []string{string(license.CapAdvancedTesting)},
	}
	return license.NewManagerForTest(license.Evaluate(p, time.Now()))
}

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

// TestReplaytestShowWithLicense saves a scenario to a temp-dir store and shows it
// under a granting license, asserting the scenario name round-trips.
func TestReplaytestShowWithLicense(t *testing.T) {
	dir := t.TempDir()
	sc := &replaytest.Scenario{
		Name:    "checkout",
		Version: 1,
		BaseURL: "http://localhost:3000",
		Steps: []replaytest.Step{
			{Index: 0, Kind: replaytest.StepNavigate, Selector: "/cart"},
		},
	}
	require.NoError(t, replaytest.NewStore(dir).SaveScenario(sc))

	h := newReplaytestHandler(grantingManager(t))
	res, out, err := h.handle(context.Background(), ReplaytestInput{Action: "show", Name: "checkout", Directory: dir})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.True(t, out.Success)
	assert.Contains(t, out.Report, "checkout")
}

// TestReplaytestExploreSeedPartition verifies that explore partitions a scenario
// with two navigate steps into exactly explore_agents seeds, cycling routes.
func TestReplaytestExploreSeedPartition(t *testing.T) {
	dir := t.TempDir()
	sc := &replaytest.Scenario{
		Name:    "tour",
		Version: 1,
		BaseURL: "http://localhost:3000",
		Steps: []replaytest.Step{
			{Index: 0, Kind: replaytest.StepNavigate, Selector: "/home"},
			{Index: 1, Kind: replaytest.StepClick, Selector: "#go"},
			{Index: 2, Kind: replaytest.StepNavigate, Selector: "/account"},
		},
	}
	require.NoError(t, replaytest.NewStore(dir).SaveScenario(sc))

	h := newReplaytestHandler(grantingManager(t))
	res, out, err := h.handle(context.Background(), ReplaytestInput{Action: "explore", Name: "tour", Directory: dir, ExploreAgents: 3})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	require.True(t, out.Success)
	require.Len(t, out.Seeds, 3)
	// Two distinct navigate routes, padded to 3 by cycling.
	assert.Equal(t, "/home", out.Seeds[0].Route)
	assert.Equal(t, "/account", out.Seeds[1].Route)
	assert.Equal(t, "/home", out.Seeds[2].Route)
	assert.Equal(t, 2, out.Seeds[2].Index)
}

// TestReplaytestRefineNoKeyGuidance verifies refine returns honest guidance (not
// an error) when no LLM API key is configured.
func TestReplaytestRefineNoKeyGuidance(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_KEY", "")
	dir := t.TempDir()
	sc := &replaytest.Scenario{Name: "r", Version: 1, BaseURL: "http://x"}
	require.NoError(t, replaytest.NewStore(dir).SaveScenario(sc))

	h := newReplaytestHandler(grantingManager(t))
	res, out, err := h.handle(context.Background(), ReplaytestInput{Action: "refine", Name: "r", Directory: dir})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.False(t, out.Success)
	assert.Contains(t, out.Message, "API key")
}

// TestComputeExploreSeedsFallback verifies the BaseURL fallback when a scenario
// has no navigate steps and a default agent count.
func TestComputeExploreSeedsFallback(t *testing.T) {
	sc := &replaytest.Scenario{Name: "n", BaseURL: "http://localhost:8080"}
	seeds := computeExploreSeeds(sc, 0)
	require.Len(t, seeds, 1)
	assert.Equal(t, "http://localhost:8080", seeds[0].Route)
}
