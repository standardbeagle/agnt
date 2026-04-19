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

// TestCheckBashBlocks verifies the exit-2 + stderr redirect path for
// `npm run dev` — the flagship acceptance case from the task.
func TestCheckBashBlocks(t *testing.T) {
	payload := makePreToolUse(t, "Bash", "npm run dev")
	var stderr bytes.Buffer
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, "", func(string) string { return "" })
	assert.Equal(t, 2, code)
	out := stderr.String()
	assert.Contains(t, out, "agnt.run")
	assert.Contains(t, out, "npm run dev")
	assert.Contains(t, out, "bypass")
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
	code := runCheckBashImpl(bytes.NewReader(payload), &stderr, tmp, func(string) string { return "" })
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
