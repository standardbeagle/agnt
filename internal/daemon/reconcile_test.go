package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestDiffByKey(t *testing.T) {
	tests := []struct {
		name                       string
		desired, running           map[string]string
		wantStart, wantStop, wantR []string
	}{
		{
			name:      "add only",
			desired:   map[string]string{"a": "1", "b": "1"},
			running:   map[string]string{"a": "1"},
			wantStart: []string{"b"},
		},
		{
			name:     "remove only",
			desired:  map[string]string{"a": "1"},
			running:  map[string]string{"a": "1", "b": "1"},
			wantStop: []string{"b"},
		},
		{
			name:    "change only",
			desired: map[string]string{"a": "2"},
			running: map[string]string{"a": "1"},
			wantR:   []string{"a"},
		},
		{
			name:      "all three at once, sorted",
			desired:   map[string]string{"keep": "1", "changed": "new", "added": "x"},
			running:   map[string]string{"keep": "1", "changed": "old", "removed": "y"},
			wantStart: []string{"added"},
			wantStop:  []string{"removed"},
			wantR:     []string{"changed"},
		},
		{
			name:    "no changes",
			desired: map[string]string{"a": "1"},
			running: map[string]string{"a": "1"},
		},
		{
			name:    "both empty",
			desired: map[string]string{},
			running: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, stop, restart := diffByKey(tt.desired, tt.running)
			assert.Equal(t, tt.wantStart, nilToEmpty(start), "start")
			assert.Equal(t, tt.wantStop, nilToEmpty(stop), "stop")
			assert.Equal(t, tt.wantR, nilToEmpty(restart), "restart")
		})
	}
}

// nilToEmpty normalizes nil slices so assert.Equal against a nil expectation
// (the zero value in the table) matches a nil result.
func nilToEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestScriptSignature_DetectsLaunchChanges(t *testing.T) {
	base := &config.ScriptConfig{Run: "npm run dev", Cwd: "/app", Env: map[string]string{"A": "1"}}

	// Identical config → identical signature (no restart).
	same := &config.ScriptConfig{Run: "npm run dev", Cwd: "/app", Env: map[string]string{"A": "1"}}
	assert.Equal(t, scriptSignature(base), scriptSignature(same))

	// Each launch-relevant field flips the signature.
	cases := []*config.ScriptConfig{
		{Run: "npm run start", Cwd: "/app", Env: map[string]string{"A": "1"}}, // command changed
		{Run: "npm run dev", Cwd: "/other", Env: map[string]string{"A": "1"}}, // cwd changed
		{Run: "npm run dev", Cwd: "/app", Env: map[string]string{"A": "2"}},   // env value changed
		{Run: "npm run dev", Cwd: "/app", Env: map[string]string{"B": "1"}},   // env key changed
	}
	for i, c := range cases {
		assert.NotEqual(t, scriptSignature(base), scriptSignature(c), "case %d should differ", i)
	}

	// Env ordering must not matter (map iteration is random).
	a := &config.ScriptConfig{Env: map[string]string{"A": "1", "B": "2", "C": "3"}}
	b := &config.ScriptConfig{Env: map[string]string{"C": "3", "B": "2", "A": "1"}}
	assert.Equal(t, scriptSignature(a), scriptSignature(b), "env order must not affect signature")

	// A cosmetic-only change (url-matchers) must NOT trigger a restart.
	cosmetic := &config.ScriptConfig{Run: "npm run dev", Cwd: "/app", Env: map[string]string{"A": "1"},
		URLMatchers: []string{"listening on {url}"}}
	assert.Equal(t, scriptSignature(base), scriptSignature(cosmetic),
		"url-matchers do not change how the process launches")
}

func TestProxySignature_DetectsTargetAndPortChanges(t *testing.T) {
	base := &config.ProxyConfig{URL: "http://localhost:3000", ListenPort: 8080}
	assert.Equal(t, proxySignature(base), proxySignature(&config.ProxyConfig{URL: "http://localhost:3000", ListenPort: 8080}))

	assert.NotEqual(t, proxySignature(base), proxySignature(&config.ProxyConfig{URL: "http://localhost:4000", ListenPort: 8080}),
		"target change must restart")
	assert.NotEqual(t, proxySignature(base), proxySignature(&config.ProxyConfig{URL: "http://localhost:3000", ListenPort: 9090}),
		"listen-port change must restart")
}

func TestComputeReconcile_CombinesScriptsAndProxies(t *testing.T) {
	plan := computeReconcile(
		map[string]string{"web": "v2", "new": "v1"},  // desired scripts
		map[string]string{"web": "v1", "gone": "v1"}, // running scripts
		map[string]string{"api": "t1"},               // desired proxies
		map[string]string{"old": "t1"},               // running proxies
	)
	assert.Equal(t, []string{"new"}, plan.StartScripts)
	assert.Equal(t, []string{"gone"}, plan.StopScripts)
	assert.Equal(t, []string{"web"}, plan.RestartScripts)
	assert.Equal(t, []string{"api"}, plan.StartProxies)
	assert.Equal(t, []string{"old"}, plan.StopProxies)
	assert.False(t, plan.IsEmpty())

	assert.True(t, computeReconcile(nil, nil, nil, nil).IsEmpty())
}
