package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chaos set declared chaos_rules but read only chaos_config.rules, so rules
// given in chaos_rules were dropped and the set reported success with none
// configured. They must take effect; giving both is refused.
func TestProxyChaosSet_ChaosRulesTakeEffect(t *testing.T) {
	// No t.Parallel(): starts a real daemon and proxy.
	dt := startChaosTestProxy(t)
	handler := dt.makeProxyHandler()
	rule := ChaosRuleInput{ID: "err500", Type: "http_error", Enabled: true, URLPattern: "/api", ErrorCodes: []int{500}}

	res, _, err := handler(context.Background(), nil, ProxyInput{
		Action: "chaos", ID: "chaos", ChaosOperation: "set",
		ChaosConfig: &ChaosConfigInput{Enabled: true},
		ChaosRules:  []ChaosRuleInput{rule},
	})
	require.NoError(t, err)
	require.Nil(t, res, "set failed: %s", errText(res))
	assert.Equal(t, []string{"err500"}, chaosRuleIDs(t, dt), "chaos_rules must be applied")

	res, _, err = handler(context.Background(), nil, ProxyInput{
		Action: "chaos", ID: "chaos", ChaosOperation: "set",
		ChaosConfig: &ChaosConfigInput{Enabled: true, Rules: []ChaosRuleInput{{ID: "lat", Type: "latency", Enabled: true}}},
		ChaosRules:  []ChaosRuleInput{rule},
	})
	require.NoError(t, err)
	require.NotNil(t, res, "rules in both places must be refused")
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "not both")
	assert.Equal(t, []string{"err500"}, chaosRuleIDs(t, dt), "a refused set must not change the rules")
}

// chaos_rules was validated but never used, while chaos_config.rules, the
// field set actually sends, was not validated at all.
func TestValidateProxyInput_ChaosConfigRules(t *testing.T) {
	long := ChaosRuleInput{ID: strings.Repeat("x", maxIDLength+1), Type: "latency"}
	err := validateProxyInput(ProxyInput{Action: "chaos", ChaosConfig: &ChaosConfigInput{Rules: []ChaosRuleInput{long}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chaos_config.rules[0].id")

	many := make([]ChaosRuleInput, maxChaosRules+1)
	err = validateProxyInput(ProxyInput{Action: "chaos", ChaosConfig: &ChaosConfigInput{Rules: many}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chaos_config.rules")

	assert.NoError(t, validateProxyInput(ProxyInput{Action: "chaos", ChaosConfig: &ChaosConfigInput{Rules: []ChaosRuleInput{{ID: "ok", Type: "latency"}}}}))
}
