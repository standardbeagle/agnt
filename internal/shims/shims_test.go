package shims

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/standardbeagle/agnt/internal/config"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cmd  string
		args []string
		want CommandClass
	}{
		{"npm", []string{"run", "dev"}, ClassDevServer},
		{"npm", []string{"dev"}, ClassDevServer},
		{"pnpm", []string{"start"}, ClassDevServer},
		{"yarn", []string{"serve"}, ClassDevServer},
		{"bun", []string{"run", "watch"}, ClassDevServer},
		{"npm", []string{"run", "build"}, ClassOneShot},
		{"npm", []string{"test"}, ClassOneShot},
		{"npm", []string{"install"}, ClassGeneric},
		{"npm", []string{"config", "get", "registry"}, ClassGeneric},
		{"vite", nil, ClassDevServer},
		{"next", []string{"dev"}, ClassDevServer},
		{"next", []string{"build"}, ClassOneShot},
		{"go", []string{"run", "./cmd/app"}, ClassDevServer},
		{"go", []string{"build", "./..."}, ClassOneShot},
		{"go", []string{"test", "./..."}, ClassOneShot},
		{"go", []string{"mod", "tidy"}, ClassGeneric},
		{"cargo", []string{"run"}, ClassDevServer},
		{"cargo", []string{"build"}, ClassOneShot},
		{"cargo", []string{"clippy"}, ClassGeneric},
		{"make", []string{"test"}, ClassOneShot},
		{"kill", []string{"1234"}, ClassKill},
		{"killall", []string{"node"}, ClassKill},
		{"pkill", []string{"-f", "vite"}, ClassKill},
		{"lsof", []string{"-i", ":3000"}, ClassPort},
		{"fuser", []string{"3000/tcp"}, ClassPort},
	}
	for _, tc := range cases {
		got := Classify(tc.cmd, tc.args)
		assert.Equal(t, tc.want, got, "%s %v", tc.cmd, tc.args)
	}
}

func TestResolveDefaults(t *testing.T) {
	t.Parallel()
	// No rules → class defaults: route for known classes, pass for generic.
	assert.Equal(t, ActionRoute, Resolve("npm", []string{"run", "dev"}, nil).Action)
	assert.Equal(t, ActionRoute, Resolve("go", []string{"test", "./..."}, nil).Action)
	assert.Equal(t, ActionRoute, Resolve("kill", []string{"1234"}, nil).Action)
	assert.Equal(t, ActionRoute, Resolve("lsof", []string{"-i"}, nil).Action)
	assert.Equal(t, ActionPass, Resolve("npm", []string{"install"}, nil).Action)
	assert.Equal(t, ActionPass, Resolve("go", []string{"mod", "tidy"}, nil).Action)
}

func TestResolveRules(t *testing.T) {
	t.Parallel()
	cfg := &config.ShimsConfig{
		Rules: map[string]*config.ShimRule{
			"build-restart": {Match: "npm run build", Action: "restart-watch"},
			"any-npm":       {Match: "npm *", Action: "pass"},
			"everything":    {Match: "*", Action: "ignore"},
		},
	}

	// Most specific (longest match) wins over wildcards.
	d := Resolve("npm", []string{"run", "build"}, cfg)
	assert.Equal(t, ActionRestartWatch, d.Action)
	assert.Equal(t, "build-restart", d.RuleName)

	// "npm *" beats bare "*".
	d = Resolve("npm", []string{"install"}, cfg)
	assert.Equal(t, ActionPass, d.Action)
	assert.Equal(t, "any-npm", d.RuleName)

	// Only "*" matches a non-npm command.
	d = Resolve("kill", []string{"1234"}, cfg)
	assert.Equal(t, ActionIgnore, d.Action)
	assert.Equal(t, "everything", d.RuleName)
}

// TestResolveSpecificityCountsLiterals pins the specificity fix: literal
// (non-*) characters decide, not raw pattern length.
func TestResolveSpecificityCountsLiterals(t *testing.T) {
	t.Parallel()
	cfg := &config.ShimsConfig{
		Rules: map[string]*config.ShimRule{
			"padded":  {Match: "npm * build*", Action: "pass"},           // 11 literals... no: "npm  build" = 10
			"literal": {Match: "npm run build", Action: "restart-watch"}, // 12 literals
		},
	}
	d := Resolve("npm", []string{"run", "build"}, cfg)
	assert.Equal(t, "literal", d.RuleName, "more literal anchors wins over wildcard-padded pattern")
}

func TestResolveRuleEmptyActionDefaultsRoute(t *testing.T) {
	t.Parallel()
	cfg := &config.ShimsConfig{
		Rules: map[string]*config.ShimRule{
			"dev": {Match: "npm run dev"},
		},
	}
	assert.Equal(t, ActionRoute, Resolve("npm", []string{"run", "dev"}, cfg).Action)
}

func TestResolveRerouteDir(t *testing.T) {
	t.Parallel()
	cfg := &config.ShimsConfig{
		Rules: map[string]*config.ShimRule{
			"oot": {Match: "go build*", Action: "reroute", Dir: "./.agnt-build"},
		},
	}
	d := Resolve("go", []string{"build", "./..."}, cfg)
	assert.Equal(t, ActionReroute, d.Action)
	assert.Equal(t, "./.agnt-build", d.Dir)
}

func TestGlobMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "anything at all", true},
		{"npm run dev", "npm run dev", true},
		{"npm run dev", "npm run build", false},
		{"npm *", "npm run dev", true},
		{"npm *", "npm", false},
		{"npm *", "yarn dev", false},
		{"*build*", "npm run build --prod", true},
		{"go build*", "go build ./...", true},
		{"go build*", "go test ./...", false},
		{"", "", true},
		{"a*", "a", true},
		{"*a", "a", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxb", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, globMatch(tc.pattern, tc.s), "globMatch(%q, %q)", tc.pattern, tc.s)
	}
}

func TestValidateAction(t *testing.T) {
	t.Parallel()
	for _, a := range []string{"route", "restart-watch", "reroute", "quiesce", "ignore", "block", "pass"} {
		assert.NoError(t, ValidateAction(a))
	}
	assert.Error(t, ValidateAction("explode"))
	assert.Error(t, ValidateAction(""))
}

func TestWatchScriptName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", WatchScriptName(nil))

	cfg := &config.AgntConfig{Scripts: map[string]*config.ScriptConfig{"dev": {}}}
	assert.Equal(t, "dev", WatchScriptName(cfg))

	cfg = &config.AgntConfig{
		Shims:   &config.ShimsConfig{WatchScript: "serve"},
		Scripts: map[string]*config.ScriptConfig{"dev": {}, "serve": {}},
	}
	assert.Equal(t, "serve", WatchScriptName(cfg))

	cfg = &config.AgntConfig{Scripts: map[string]*config.ScriptConfig{"build": {}}}
	assert.Equal(t, "", WatchScriptName(cfg))
}
