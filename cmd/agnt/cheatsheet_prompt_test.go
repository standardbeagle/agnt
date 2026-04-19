//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/agentadapter"
)

// TestBuildAgntSystemPrompt_IncludesCheatSheetByDefault guards the wiring
// that appends the __devtool helpers cheat sheet to the default prompt.
// We run buildAgntSystemPrompt in a tmpdir with no .agnt.kdl (so
// defaults apply) and assert the cheat-sheet header + a promoted helper
// both appear in the rendered prompt.
func TestBuildAgntSystemPrompt_IncludesCheatSheetByDefault(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	// Non-existent socket path forces the daemon-less branch, which still
	// goes through agntConfig.BuildSystemPrompt and the cheat-sheet append.
	prompt := buildAgntSystemPrompt(filepath.Join(dir, "no-such-socket"))

	if !strings.Contains(prompt, "## Browser debugging helpers") {
		t.Errorf("expected cheat sheet header in default prompt; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "auditAccessibility(") {
		t.Errorf("expected promoted helper signature in default prompt; got:\n%s", prompt)
	}
}

// TestBuildAgntSystemPrompt_OmitsCheatSheetWhenDisabled verifies that
// `ai { helpers-cheat-sheet false }` in .agnt.kdl removes the cheat
// sheet from the prompt. This is the one-line config toggle contract.
func TestBuildAgntSystemPrompt_OmitsCheatSheetWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := `ai {
    helpers-cheat-sheet false
}
`
	if err := os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	chdir(t, dir)

	prompt := buildAgntSystemPrompt(filepath.Join(dir, "no-such-socket"))

	if strings.Contains(prompt, "## Browser debugging helpers") {
		t.Errorf("expected NO cheat sheet header when disabled; got:\n%s", prompt)
	}
	if strings.Contains(prompt, "auditAccessibility(") {
		t.Errorf("expected NO promoted helper when disabled; got:\n%s", prompt)
	}
}

// TestBuildAgntSystemPrompt_AdapterAgnosticContent verifies the cheat
// sheet delivered to the Claude --append-system-prompt flag is
// character-identical to what a stdin-injected adapter receives. The
// single-source-of-truth invariant is that prompt assembly happens in
// buildAgntSystemPrompt, and adapters are dumb passthroughs.
func TestBuildAgntSystemPrompt_AdapterAgnosticContent(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	prompt := buildAgntSystemPrompt(filepath.Join(dir, "no-such-socket"))
	if prompt == "" {
		t.Fatal("empty prompt")
	}

	reg := agentadapter.DefaultRegistry()
	claude := reg.Lookup("claude")
	if claude == nil || claude.Name() != "claude" {
		t.Fatalf("claude adapter not resolved: %v", claude)
	}
	gemini := reg.Lookup("gemini")
	if gemini == nil || gemini.Name() != "gemini" {
		t.Fatalf("gemini adapter not resolved: %v", gemini)
	}

	// Claude injects via argv flag.
	claudeArgs := claude.BuildArgs([]string{"claude"}, prompt)
	// Locate the --append-system-prompt argv payload and pull the next token.
	var claudePayload string
	for i := 0; i < len(claudeArgs)-1; i++ {
		if claudeArgs[i] == "--append-system-prompt" {
			claudePayload = claudeArgs[i+1]
			break
		}
	}
	if claudePayload == "" {
		t.Fatalf("claude adapter did not emit --append-system-prompt in %v", claudeArgs)
	}

	// Gemini injects via initial stdin.
	geminiPayload := string(gemini.InitialStdin(prompt))
	if geminiPayload == "" {
		t.Fatalf("gemini adapter did not emit stdin payload")
	}

	// The cheat sheet content must be byte-identical in both payloads.
	const needle = "## Browser debugging helpers"
	if !strings.Contains(claudePayload, needle) {
		t.Errorf("claude payload missing cheat sheet")
	}
	if !strings.Contains(geminiPayload, needle) {
		t.Errorf("gemini payload missing cheat sheet")
	}

	// Extract the tail starting at the cheat-sheet header from each
	// payload. The stdin adapter appends a trailing "\n" to make the
	// message look like a submitted line to the agent — normalize that
	// away before comparing. The cheat-sheet body itself must match
	// byte-for-byte; adapter-specific wrapping is fine, drift in the
	// cheat-sheet body is not.
	ci := strings.Index(claudePayload, needle)
	gi := strings.Index(geminiPayload, needle)
	claudeTail := strings.TrimRight(claudePayload[ci:], "\n")
	geminiTail := strings.TrimRight(geminiPayload[gi:], "\n")
	if claudeTail != geminiTail {
		t.Errorf("cheat sheet content differs between adapters:\nclaude:\n%s\ngemini:\n%s", claudeTail, geminiTail)
	}
	if !strings.Contains(claudeTail, "auditAccessibility(") {
		t.Errorf("cheat sheet tail missing auditAccessibility; got:\n%s", claudeTail)
	}
}

// chdir switches to dir for the lifetime of the test. Restores cwd in
// t.Cleanup. Shared helper across cheat-sheet prompt tests.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
