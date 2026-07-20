package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
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

	missing := newReplaytestHandler(license.NewManager(), nil) // unloaded => capability NOT granted
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
		require.NotNil(t, res)
		// Free actions are not license-gated: they never emit the activate
		// prompt (a missing-name validation error is fine — it is not the
		// license block being asserted here).
		assert.NotContains(t, resultText(res), "activate", "action %s should not be license-blocked", a)
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

	h := newReplaytestHandler(grantingManager(t), nil)
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

	h := newReplaytestHandler(grantingManager(t), nil)
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

	h := newReplaytestHandler(grantingManager(t), nil)
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

// fakeLogClient is an in-memory replaytestLogClient for record/stop tests.
type fakeLogClient struct {
	entries   []proxy.LogEntry
	target    string
	pullCalls int
	lastSince string
}

func (f *fakeLogClient) ProxyLogQueryFull(proxyID string, filter protocol.LogQueryFilter) ([]proxy.LogEntry, int64, int64, error) {
	f.pullCalls++
	f.lastSince = filter.Since
	return f.entries, int64(len(f.entries)), 0, nil
}

func (f *fakeLogClient) ProxyStatus(id string) (map[string]interface{}, error) {
	return map[string]interface{}{"target_url": f.target}, nil
}

// TestReplaytestStopWithoutRecord asserts stop with no prior record is an error
// and never touches the daemon client.
func TestReplaytestStopWithoutRecord(t *testing.T) {
	fake := &fakeLogClient{}
	h := newReplaytestHandler(grantingManager(t), func() (replaytestLogClient, error) { return fake, nil })

	res, _, err := h.handle(context.Background(), ReplaytestInput{Action: "stop", Name: "ghost"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "no active recording")
	assert.Equal(t, 0, fake.pullCalls, "stop without record must not call the client")
}

func TestReplaytestRecordRequiresDaemonMode(t *testing.T) {
	h := newReplaytestHandler(grantingManager(t), nil)

	res, _, err := h.handle(context.Background(), ReplaytestInput{
		Action: "record", Name: "checkout", ProxyID: "px", Directory: t.TempDir(),
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "require daemon mode")
}

func TestReplaytestDuplicateRecordRejected(t *testing.T) {
	fake := &fakeLogClient{target: "http://localhost:3000"}
	h := newReplaytestHandler(grantingManager(t), func() (replaytestLogClient, error) { return fake, nil })
	in := ReplaytestInput{Action: "record", Name: "checkout", ProxyID: "px", Directory: t.TempDir()}

	first, _, err := h.handle(context.Background(), in)
	require.NoError(t, err)
	assert.False(t, first.IsError)

	second, _, err := h.handle(context.Background(), in)
	require.NoError(t, err)
	assert.True(t, second.IsError)
	assert.Contains(t, resultText(second), "already active")
}

// TestReplaytestRecordThenStop drives a full record→stop flow against a fake
// client and asserts the assembled scenario (with response body preserved as a
// recording) is saved and the client was pulled exactly once.
func TestReplaytestRecordThenStop(t *testing.T) {
	dir := t.TempDir()
	fake := &fakeLogClient{
		target: "http://localhost:3000",
		entries: []proxy.LogEntry{
			{
				Type: proxy.LogTypeHTTP,
				HTTP: &proxy.HTTPLogEntry{
					ID:           "r1",
					Method:       "GET",
					URL:          "http://localhost:3000/api/items",
					StatusCode:   200,
					ResponseBody: `{"items":[1,2,3]}`,
				},
			},
		},
	}
	h := newReplaytestHandler(grantingManager(t), func() (replaytestLogClient, error) { return fake, nil })

	rec, recOut, err := h.handle(context.Background(), ReplaytestInput{Action: "record", Name: "checkout", ProxyID: "px", Directory: dir})
	require.NoError(t, err)
	assert.False(t, rec.IsError)
	assert.True(t, recOut.Success)

	stop, stopOut, err := h.handle(context.Background(), ReplaytestInput{Action: "stop", Name: "checkout", Directory: dir})
	require.NoError(t, err)
	assert.False(t, stop.IsError)
	require.True(t, stopOut.Success)
	assert.Equal(t, 1, fake.pullCalls)
	assert.NotEmpty(t, fake.lastSince, "stop must filter traffic since record start")

	// Scenario persisted with the response body preserved as a recording blob.
	sc, err := replaytest.NewStore(dir).LoadScenario("checkout")
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:3000", sc.BaseURL)
	require.Len(t, sc.Recordings, 1)
	require.Len(t, sc.Blobs, 1)
	assert.Contains(t, sc.Blobs["blob:0"], "items")

	// Recording session is cleared; a second stop reports no active recording.
	again, _, err := h.handle(context.Background(), ReplaytestInput{Action: "stop", Name: "checkout", Directory: dir})
	require.NoError(t, err)
	assert.True(t, again.IsError)
}
