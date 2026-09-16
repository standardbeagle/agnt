//go:build unix

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/agentadapter"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/daemonclient"
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

// TestPromptDelivery_CheatSheetViaFlagAndContextFile verifies prompt assembly
// stays single-source (buildAgntSystemPrompt) while normal coding sessions do
// NOT dump guidance into the conversation: Claude gets it via the
// --append-system-prompt flag (invisible), and stdin agents get it through
// their persistent context file (GEMINI.md).
func TestPromptDelivery_CheatSheetViaFlagAndContextFile(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	prompt := buildAgntSystemPrompt(filepath.Join(dir, "no-such-socket"))
	if prompt == "" {
		t.Fatal("empty prompt")
	}
	const needle = "## Browser debugging helpers"

	reg := agentadapter.DefaultRegistry()
	claude := reg.Lookup("claude")
	gemini := reg.Lookup("gemini")
	if claude == nil || gemini == nil {
		t.Fatalf("adapters not resolved: claude=%v gemini=%v", claude, gemini)
	}

	// Claude: full cheat sheet via the --append-system-prompt flag (invisible
	// to the conversation).
	claudeArgs := claude.BuildArgs([]string{"claude"}, prompt)
	var claudePayload string
	for i := 0; i < len(claudeArgs)-1; i++ {
		if claudeArgs[i] == "--append-system-prompt" {
			claudePayload = claudeArgs[i+1]
			break
		}
	}
	if !strings.Contains(claudePayload, needle) {
		t.Errorf("claude flag payload missing cheat sheet; got:\n%s", claudePayload)
	}

	// The full cheat sheet reaches the stdin agent via its always-loaded
	// context file, not a normal-session stdin message.
	writePersistentContext(gemini.Name(), dir, prompt)
	ctxFile, err := os.ReadFile(filepath.Join(dir, "GEMINI.md"))
	if err != nil {
		t.Fatalf("context file not written: %v", err)
	}
	if !strings.Contains(string(ctxFile), needle) {
		t.Errorf("context file missing cheat sheet:\n%s", ctxFile)
	}
	if !strings.Contains(string(ctxFile), "auditAccessibility(") {
		t.Errorf("context file missing promoted helper; got:\n%s", ctxFile)
	}
}

// TestProxyHintLinesIncludeDiagnose pins the per-proxy runtime hint block:
// each running proxy gets a diagnose hint line pointing at the click flow,
// alongside the existing proxylog/get_incidents hints.
func TestProxyHintLinesIncludeDiagnose(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	socketPath := filepath.Join(t.TempDir(), "hint-daemon.sock")
	_ = daemon.NewForTest(t, daemon.DaemonConfig{SocketPath: socketPath})
	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fixture"))
	}))
	t.Cleanup(backend.Close)
	if _, err := client.ProxyStart("hint-proxy", backend.URL, 0, 0, dir); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() { _ = client.ProxyStop("hint-proxy") })

	prompt := buildAgntSystemPrompt(socketPath)
	want := `diagnose {action: "click", proxy_id: "hint-proxy"}`
	if !strings.Contains(prompt, want) {
		t.Errorf("runtime proxy hints missing %q; got:\n%s", want, prompt)
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
