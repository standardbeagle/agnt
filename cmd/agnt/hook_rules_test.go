package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makePreToolUse(t *testing.T, toolName, command string) []byte {
	t.Helper()
	input, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	p, err := json.Marshal(map[string]any{"tool_name": toolName, "tool_input": json.RawMessage(input)})
	require.NoError(t, err)
	return p
}

// agntRunEnv returns a getenv stub that reports an active `agnt run` session,
// so block rules exercise the exit-2 path.
func agntRunEnv(k string) string {
	if k == "AGNT_RUN" {
		return "1"
	}
	return ""
}

// TestCheckBashBlocks verifies the exit-2 + stderr redirect path for
// `npm run dev` — the flagship acceptance case. A hard block requires an
// active `agnt run` session (AGNT_RUN set), so the stub reports one.
func TestCheckBashBlocks(t *testing.T) {
	payload := makePreToolUse(t, "Bash", "npm run dev")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, "", agntRunEnv)
	assert.Equal(t, 2, code)
	out := stderr.String()
	assert.Contains(t, out, "agnt.run")
	assert.Contains(t, out, "npm run dev")
	assert.Contains(t, out, "bypass")
}

// TestCheckBashBlockDowngradesOutsideSession verifies the judicious-exit
// contract: outside an `agnt run` session, a would-be block does NOT return an
// error (exit 2) — it downgrades to a non-error soft-warn (exit 0) so the
// command still runs while the agent learns the agnt-native alternative.
func TestCheckBashBlockDowngradesOutsideSession(t *testing.T) {
	payload := makePreToolUse(t, "Bash", "npm run dev")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, "", func(string) string { return "" })
	assert.Equal(t, 0, code, "block must not return exit 2 outside an agnt run session")
	assert.Contains(t, stderr.String(), "npm run dev", "still nudges via soft-warn")
}

// TestCheckBashBypassEnv confirms AGNT_HOOK_BYPASS=1 short-circuits.
func TestCheckBashBypassEnv(t *testing.T) {
	payload := makePreToolUse(t, "Bash", "npm run dev")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, "", func(k string) string {
		if k == "AGNT_HOOK_BYPASS" {
			return "1"
		}
		return ""
	})
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
}

// TestCheckBashBypassMarker confirms the inline comment bypass.
func TestCheckBashBypassMarker(t *testing.T) {
	payload := makePreToolUse(t, "Bash", "npm run dev # agnt-allow")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, "", func(string) string { return "" })
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
}

// TestCheckBashScopeGuard: unrelated project paths with no .agnt.kdl
// should short-circuit to allow. We use a tmp dir as a stand-in.
func TestCheckBashScopeGuard(t *testing.T) {
	tmp := t.TempDir()
	payload := makePreToolUse(t, "Bash", "npm run dev")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, tmp, func(string) string { return "" })
	assert.Equal(t, 0, code, "no .agnt.kdl in tmp dir should disable interception")
}

// TestCheckBashScopeGuardActiveWithConfig: once .agnt.kdl is present, the
// interceptor engages even in a tmp dir.
func TestCheckBashScopeGuardActiveWithConfig(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, writeTempFile(tmp+"/.agnt.kdl", ""))
	payload := makePreToolUse(t, "Bash", "npm run dev")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, tmp, agntRunEnv)
	assert.Equal(t, 2, code)
}

// TestCheckBashNonBashTool: non-Bash tool calls exit 0 without reading
// the regex engine.
func TestCheckBashNonBashTool(t *testing.T) {
	payload := makePreToolUse(t, "Edit", "npm run dev")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, "", func(string) string { return "" })
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
}

// TestCheckBashMalformedJSON: garbage on stdin fails open.
func TestCheckBashMalformedJSON(t *testing.T) {
	var stderr bytes.Buffer
	code := runCheckBashImpl(strings.NewReader("not json"), &stderr, "", func(string) string { return "" })
	assert.Equal(t, 0, code)
}

// TestCheckBashSoftWarn: warning rules don't block.
func TestCheckBashSoftWarn(t *testing.T) {
	payload := makePreToolUse(t, "Bash", "lsof -i :3000")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, "", func(string) string { return "" })
	assert.Equal(t, 0, code)
	assert.Contains(t, stderr.String(), "alternative:")
}

// TestCheckPromptMatches: the prompt hook emits <system-reminder> on
// matching intent.
func TestCheckPromptMatches(t *testing.T) {
	payload, err := json.Marshal(map[string]string{"prompt": "please start the dev server"})
	require.NoError(t, err)
	var stdout bytes.Buffer
	runCheckPromptImpl(bytes.NewReader(payload), &stdout, "")
	out := stdout.String()
	assert.Contains(t, out, "<system-reminder>")
	assert.Contains(t, out, "</system-reminder>")
	assert.Contains(t, out, "agnt.run")
}

// TestCheckPromptNoMatch: unrelated prompts produce no output.
func TestCheckPromptNoMatch(t *testing.T) {
	payload, err := json.Marshal(map[string]string{"prompt": "refactor the login button"})
	require.NoError(t, err)
	var stdout bytes.Buffer
	runCheckPromptImpl(bytes.NewReader(payload), &stdout, "")
	assert.Empty(t, stdout.String())
}

func writeTempFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
