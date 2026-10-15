package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/standardbeagle/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startChaosTestProxy starts a real daemon and a proxy "chaos" on it.
func startChaosTestProxy(t *testing.T) *DaemonTools {
	t.Helper()
	dt, _ := attachTestDaemon(t)
	require.NoError(t, dt.ensureConnected())
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)
	_, err := dt.client.ProxyStart("chaos", backend.URL, 0, 0, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = dt.client.ProxyStop("chaos") })
	return dt
}

func chaosRuleIDs(t *testing.T, dt *DaemonTools) []string {
	t.Helper()
	res, out, err := dt.makeProxyHandler()(context.Background(), nil, ProxyInput{
		Action: "chaos", ID: "chaos", ChaosOperation: "list_rules",
	})
	require.NoError(t, err)
	require.Nil(t, res, "list_rules failed: %s", errText(res))
	ids := make([]string, 0, len(out.ChaosRules))
	for _, r := range out.ChaosRules {
		ids = append(ids, r.ID)
	}
	return ids
}

// The chaos mutations (enable, disable, preset, set, add_rule, remove_rule,
// clear) all failed with "expected JSON response, got OK": the daemon replies
// OK, and takes preset, rule and rule id in the JSON payload where the client
// sent them positionally. list_rules and status read the rule set the last
// set installed rather than the live one, and stats read a "stats" key the
// daemon does not send. Every operation must work end to end.
func TestProxyChaos_OperationsWorkAgainstTheDaemon(t *testing.T) {
	// No t.Parallel(): starts a real daemon and proxy.
	dt := startChaosTestProxy(t)
	handler := dt.makeProxyHandler()
	chaos := func(in ProxyInput) ProxyOutput {
		t.Helper()
		in.Action, in.ID = "chaos", "chaos"
		res, out, err := handler(context.Background(), nil, in)
		require.NoError(t, err)
		require.Nil(t, res, "chaos %s failed: %s", in.ChaosOperation, errText(res))
		return out
	}

	assert.True(t, chaos(ProxyInput{ChaosOperation: "enable"}).ChaosEnabled)
	assert.True(t, chaos(ProxyInput{ChaosOperation: "status"}).ChaosEnabled)
	assert.False(t, chaos(ProxyInput{ChaosOperation: "disable"}).ChaosEnabled)
	assert.False(t, chaos(ProxyInput{ChaosOperation: "status"}).ChaosEnabled)

	assert.True(t, chaos(ProxyInput{ChaosOperation: "preset", ChaosPreset: "flaky-api"}).ChaosEnabled)
	assert.True(t, chaos(ProxyInput{ChaosOperation: "status"}).ChaosEnabled)
	assert.NotEmpty(t, chaosRuleIDs(t, dt), "a preset installs rules")

	out := chaos(ProxyInput{ChaosOperation: "set", ChaosConfig: &ChaosConfigInput{
		Enabled: true,
		Rules:   []ChaosRuleInput{{ID: "slow", Type: "latency", Enabled: true, MinLatencyMs: 1, MaxLatencyMs: 2}},
	}})
	assert.True(t, out.ChaosEnabled)
	assert.Equal(t, []string{"slow"}, chaosRuleIDs(t, dt))

	chaos(ProxyInput{ChaosOperation: "add_rule", ChaosRule: &ChaosRuleInput{ID: "err500", Type: "http_error", Enabled: true, ErrorCodes: []int{500}}})
	assert.Equal(t, []string{"slow", "err500"}, chaosRuleIDs(t, dt), "list_rules must include an added rule")
	status := chaos(ProxyInput{ChaosOperation: "status"})
	require.Len(t, status.ChaosRules, 2, "status must report the live rules")

	chaos(ProxyInput{ChaosOperation: "remove_rule", ChaosRuleID: "slow"})
	assert.Equal(t, []string{"err500"}, chaosRuleIDs(t, dt), "list_rules must drop a removed rule")

	res, _, err := handler(context.Background(), nil, ProxyInput{Action: "chaos", ID: "chaos", ChaosOperation: "remove_rule", ChaosRuleID: "nope"})
	require.NoError(t, err)
	require.NotNil(t, res, "removing an unknown rule must be reported")
	assert.True(t, res.IsError)

	stats := chaos(ProxyInput{ChaosOperation: "stats"}).ChaosStats
	require.NotNil(t, stats)
	assert.Contains(t, stats.RuleStats, "err500", "stats must carry the daemon's per-rule counts")

	assert.False(t, chaos(ProxyInput{ChaosOperation: "clear"}).ChaosEnabled)
	assert.Empty(t, chaosRuleIDs(t, dt))
}

// errText is resultText for a failure message, safe on a nil (success) result.
func errText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	return resultText(res)
}
