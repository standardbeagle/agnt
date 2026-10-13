package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shipped template is what the overlay's config editor opens for a project
// with no .agnt.kdl. It used to carry `hooks {}` and `toast {}` blocks the
// strict parser has no field for, so the editor opened on a file it then
// refused to save.
func TestDefaultAgntConfigKDL_Parses(t *testing.T) {
	cfg, err := ParseAgntConfig(DefaultAgntConfigKDL())
	require.NoError(t, err)
	assert.Empty(t, cfg.Warnings, "the template must not ship keys that have no effect")
}

// uncommentTemplateExamples turns every example line of the template into
// live KDL. Per the convention on DefaultAgntConfigKDL, a comment whose text
// starts with a lowercase letter, a quote or a brace is an example; anything
// else is prose and is dropped.
func uncommentTemplateExamples(tmpl string) string {
	var out []string
	for _, line := range strings.Split(tmpl, "\n") {
		text := strings.TrimSpace(line)
		if !strings.HasPrefix(text, "//") {
			out = append(out, line)
			continue
		}
		for strings.HasPrefix(text, "//") {
			text = strings.TrimSpace(strings.TrimPrefix(text, "//"))
		}
		if text == "" {
			continue
		}
		c := text[0]
		if (c >= 'a' && c <= 'z') || c == '"' || c == '{' || c == '}' {
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n")
}

func TestDefaultAgntConfigKDL_ExamplesParse(t *testing.T) {
	live := uncommentTemplateExamples(DefaultAgntConfigKDL())
	cfg, err := ParseAgntConfig(live)
	require.NoError(t, err, "uncommented template:\n%s", live)

	// The walk really did uncomment the examples.
	assert.Contains(t, cfg.Scripts, "dev-bash")
	assert.Contains(t, cfg.Proxies, "lan")
	require.NotNil(t, cfg.Alerts)
	assert.Contains(t, cfg.Alerts.Patterns, "my-custom")
	require.NotNil(t, cfg.AI)
	assert.NotEmpty(t, cfg.AI.Context)
	assert.True(t, cfg.Proxies["lan"].AllowExternal, "a 0.0.0.0 example must carry allow-external")
	assert.Empty(t, cfg.Warnings, "no example may use a key that has no effect")
}

func TestParseAgntConfig_RejectsInvalidValues(t *testing.T) {
	const twoScripts = "scripts {\n    a {\n        run \"x\"\n        autostart true\n    }\n    b {\n        run \"y\"\n        depends-on %s\n    }\n}\n"
	dep := func(body string) string { return strings.Replace(twoScripts, "%s", body, 1) }

	cases := []struct {
		name, kdl string
		want      []string // substrings the error must carry
	}{
		{"port-conflict typo", "project {\n    port-conflict \"autokill\"\n}\n",
			[]string{"port-conflict", "autokill", "prompt, auto-kill, skip, fail"}},
		{"alerts preset typo", "alerts {\n    preset \"claude\"\n}\n",
			[]string{"alerts", "preset", "claude-code, universal"}},
		{"depends-on misspelt property", dep("\"a\" timout=30"),
			[]string{"depends-on", "timout", "timeout="}},
		{"depends-on child misspelt property", dep("{\n            a timout=30\n        }"),
			[]string{"depends-on \"a\"", "timout"}},
		{"depends-on negative timeout", dep("\"a\" timeout=-5"),
			[]string{"depends-on", "negative"}},
		{"listen-port too large", "proxies {\n    dev {\n        url \"http://localhost:1\"\n        listen-port 99999999\n    }\n}\n",
			[]string{"proxies.dev.listen-port", "65535"}},
		{"listen-port negative", "proxies {\n    dev {\n        url \"http://localhost:1\"\n        listen-port -5\n    }\n}\n",
			[]string{"proxies.dev.listen-port", "65535"}},
		{"fallback-port fractional", "proxies {\n    dev {\n        script \"x\"\n        fallback-port 3000.5\n    }\n}\n",
			[]string{"proxies.dev.fallback-port", "whole number"}},
		{"ports fractional", "scripts {\n    dev {\n        run \"x\"\n        ports 3000.7\n    }\n}\n",
			[]string{"scripts.dev.ports", "whole number"}},
		{"ports zero", "scripts {\n    dev {\n        run \"x\"\n        ports 0\n    }\n}\n",
			[]string{"scripts.dev.ports", "between 1 and 65535"}},
		{"max-sessions fractional", "automation {\n    max-sessions 2.9\n}\n",
			[]string{"automation.max-sessions", "whole number"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAgntConfig(tc.kdl)
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestParseAgntConfig_AcceptsValidValues(t *testing.T) {
	for _, policy := range validPortConflictPolicies {
		_, err := ParseAgntConfig("project {\n    port-conflict \"" + policy + "\"\n}\n")
		assert.NoError(t, err, policy)
	}
	for _, preset := range validAlertPresets {
		_, err := ParseAgntConfig("alerts {\n    preset \"" + preset + "\"\n}\n")
		assert.NoError(t, err, preset)
	}

	cfg, err := ParseAgntConfig("scripts {\n    dev {\n        run \"x\"\n        ports 3000 3001\n    }\n}\n" +
		"proxies {\n    dev {\n        url \"http://localhost:1\"\n        listen-port 65535\n    }\n}\n" +
		// A non-positive max-sessions restores the default (documented).
		"automation {\n    max-sessions 0\n}\n")
	require.NoError(t, err)
	assert.Equal(t, []int{3000, 3001}, cfg.Scripts["dev"].Ports)
	assert.Equal(t, 65535, cfg.Proxies["dev"].ListenPort)
	assert.Equal(t, DefaultAutomationMaxSessions, cfg.Automation.MaxSessionsOrDefault())
}

// Keys that parse but that nothing reads must keep loading (existing configs
// carry them) and must say so through cfg.Warnings, the channel the daemon
// surfaces to the session log.
func TestParseAgntConfig_WarnsOnKeysWithNoEffect(t *testing.T) {
	cfg, err := ParseAgntConfig("proxies {\n    dev {\n        url \"http://localhost:1\"\n        websocket true\n    }\n}\n" +
		"ai {\n    skill \"debugging\"\n    env {\n        FOO \"bar\"\n    }\n}\n")
	require.NoError(t, err)
	require.Len(t, cfg.Warnings, 3)
	joined := strings.Join(cfg.Warnings, "\n")
	assert.Contains(t, joined, "proxies.dev.websocket has no effect")
	assert.Contains(t, joined, "ai.skill has no effect")
	assert.Contains(t, joined, "ai.env has no effect")

	clean, err := ParseAgntConfig("proxies {\n    dev {\n        url \"http://localhost:1\"\n    }\n}\n")
	require.NoError(t, err)
	assert.Empty(t, clean.Warnings)
}

// The global config's dead keys keep loading and are reported the same way.
func TestParseKDLConfig_WarnsOnKeysWithNoEffect(t *testing.T) {
	cfg, err := ParseKDLConfig("version \"1.0\"\nlanguages {\n    go {\n        priority 5\n    }\n}\n")
	require.NoError(t, err)
	joined := strings.Join(cfg.Warnings, "\n")
	assert.Contains(t, joined, "version has no effect")
	assert.Contains(t, joined, "languages has no effect")

	clean, err := ParseKDLConfig("settings {\n    graceful-timeout 5\n}\n")
	require.NoError(t, err)
	assert.Empty(t, clean.Warnings)
}
