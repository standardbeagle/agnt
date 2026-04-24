package agentadapter

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultRegistry_MatchesClaudeByName(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("claude")
	if a == nil {
		t.Fatal("expected claude adapter, got nil")
	}
	if a.Name() != "claude" {
		t.Errorf("Name() = %q, want %q", a.Name(), "claude")
	}
}

func TestDefaultRegistry_MatchesClaudeByAbsolutePath(t *testing.T) {
	r := DefaultRegistry()
	for _, path := range []string{"/usr/bin/claude", "/opt/homebrew/bin/claude", "C:\\Users\\me\\claude.exe"} {
		if a := r.Lookup(path); a == nil || a.Name() != "claude" {
			t.Errorf("Lookup(%q) did not resolve to claude adapter", path)
		}
	}
}

func TestDefaultRegistry_MatchesAiderByRelativePath(t *testing.T) {
	r := DefaultRegistry()
	if a := r.Lookup("./aider"); a == nil || a.Name() != "aider" {
		t.Errorf("Lookup(./aider) did not resolve to aider adapter")
	}
}

func TestDefaultRegistry_MatchesAllKnownAgents(t *testing.T) {
	r := DefaultRegistry()
	// Most agents map command→name 1:1.
	for _, name := range []string{
		"claude", "gemini", "copilot", "aider",
		"cursor", "cursor-agent", "opencode", "auggie",
	} {
		a := r.Lookup(name)
		if a == nil {
			t.Errorf("Lookup(%q) returned nil", name)
			continue
		}
		if a.Name() != name {
			t.Errorf("Lookup(%q).Name() = %q, want %q", name, a.Name(), name)
		}
	}

	// kimi and kimi-cli both resolve to the kimiAdapter whose canonical
	// name is "kimi-cli" — the two command spellings share one adapter.
	for _, cmd := range []string{"kimi", "kimi-cli"} {
		a := r.Lookup(cmd)
		if a == nil {
			t.Errorf("Lookup(%q) returned nil", cmd)
			continue
		}
		if a.Name() != "kimi-cli" {
			t.Errorf("Lookup(%q).Name() = %q, want %q", cmd, a.Name(), "kimi-cli")
		}
	}
}

func TestDefaultRegistry_CursorAgentNotConfusedWithCursor(t *testing.T) {
	r := DefaultRegistry()
	if a := r.Lookup("cursor-agent"); a == nil || a.Name() != "cursor-agent" {
		t.Errorf("cursor-agent resolved to %v, want cursor-agent", a)
	}
	if a := r.Lookup("cursor"); a == nil || a.Name() != "cursor" {
		t.Errorf("cursor resolved to %v, want cursor", a)
	}
}

func TestDefaultRegistry_UnknownCommandReturnsNil(t *testing.T) {
	r := DefaultRegistry()
	if a := r.Lookup("ls"); a != nil {
		t.Errorf("Lookup(ls) = %v, want nil", a)
	}
	if a := r.Lookup(""); a != nil {
		t.Errorf("Lookup(\"\") = %v, want nil", a)
	}
}

func TestClaudeAdapter_BuildArgsAppendsFlag(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("claude")
	base := []string{"--verbose"}
	got := a.BuildArgs(base, "hello prompt")
	want := []string{"--verbose", "--append-system-prompt", "hello prompt"}
	if !equalStrings(got, want) {
		t.Errorf("BuildArgs = %v, want %v", got, want)
	}
	// Must not mutate the caller's base slice.
	if len(base) != 1 || base[0] != "--verbose" {
		t.Errorf("BuildArgs mutated baseArgs: %v", base)
	}
}

func TestClaudeAdapter_EmptyPromptLeavesArgsAlone(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("claude")
	got := a.BuildArgs([]string{"x", "y"}, "")
	if !equalStrings(got, []string{"x", "y"}) {
		t.Errorf("empty prompt changed args: %v", got)
	}
}

func TestClaudeAdapter_InitialStdinIsNil(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("claude")
	if a.InitialStdin("prompt") != nil {
		t.Error("claude InitialStdin should always be nil")
	}
	if a.StdinDelay() != 0 {
		t.Error("claude StdinDelay should be 0")
	}
}

func TestStdinAdapter_BuildArgsUnchanged(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("aider")
	base := []string{"--model", "gpt-4"}
	got := a.BuildArgs(base, "prompt body")
	if !equalStrings(got, base) {
		t.Errorf("stdin adapter changed args: %v", got)
	}
}

func TestStdinAdapter_InitialStdinContainsPromptAndNote(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("gemini")
	got := a.InitialStdin("PROMPT_BODY")
	if len(got) == 0 {
		t.Fatal("expected non-empty stdin")
	}
	if !bytes.Contains(got, []byte("PROMPT_BODY")) {
		t.Errorf("stdin missing prompt body: %s", got)
	}
	if !bytes.Contains(got, []byte("agnt")) {
		t.Errorf("stdin missing agnt note: %s", got)
	}
	if !bytes.HasSuffix(got, []byte("\n")) {
		t.Errorf("stdin should end with newline: %q", got)
	}
}

func TestStdinAdapter_EmptyPromptReturnsNil(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("aider")
	if got := a.InitialStdin(""); got != nil {
		t.Errorf("empty prompt should yield nil stdin, got %q", got)
	}
}

func TestStdinAdapter_DefaultDelayIs500ms(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("aider")
	if a.StdinDelay() != 500*time.Millisecond {
		t.Errorf("default delay = %v, want 500ms", a.StdinDelay())
	}
}

func TestOverride_DisablesInjection(t *testing.T) {
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"aider":  {Disabled: true},
		"claude": {Disabled: true},
	})
	aider := r.Lookup("aider")
	if got := aider.InitialStdin("prompt"); got != nil {
		t.Errorf("disabled aider emitted stdin: %q", got)
	}
	if got := aider.BuildArgs([]string{"x"}, "prompt"); !equalStrings(got, []string{"x"}) {
		t.Errorf("disabled aider changed args: %v", got)
	}
	claude := r.Lookup("claude")
	if got := claude.BuildArgs([]string{"x"}, "prompt"); !equalStrings(got, []string{"x"}) {
		t.Errorf("disabled claude appended flag anyway: %v", got)
	}
}

func TestOverride_CustomFlagNameForClaude(t *testing.T) {
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"claude": {FlagName: "--system-prompt"},
	})
	a := r.Lookup("claude")
	got := a.BuildArgs(nil, "prompt")
	if !equalStrings(got, []string{"--system-prompt", "prompt"}) {
		t.Errorf("custom flag not applied: %v", got)
	}
}

func TestOverride_CustomStdinDelayForStdinAdapter(t *testing.T) {
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"aider": {StdinDelay: 2 * time.Second},
	})
	a := r.Lookup("aider")
	if got := a.StdinDelay(); got != 2*time.Second {
		t.Errorf("custom delay = %v, want 2s", got)
	}
}

func TestOverride_ZeroDelayInheritsDefault(t *testing.T) {
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"aider": {FlagName: "unused"}, // non-empty override, zero delay
	})
	a := r.Lookup("aider")
	if got := a.StdinDelay(); got != DefaultStdinDelay {
		t.Errorf("zero override delay should inherit default, got %v", got)
	}
}

func TestOverride_CaseInsensitiveKeys(t *testing.T) {
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"CLAUDE": {Disabled: true},
	})
	a := r.Lookup("claude")
	if got := a.BuildArgs(nil, "prompt"); len(got) != 0 {
		t.Errorf("case-insensitive override not applied: %v", got)
	}
}

func TestRegistry_RegisterAppendsCustomAdapter(t *testing.T) {
	r := NewRegistry()
	r.Register(newStdinAdapter("mybot", []string{"mybot"}))
	a := r.Lookup("/usr/local/bin/mybot")
	if a == nil || a.Name() != "mybot" {
		t.Errorf("custom adapter not registered")
	}
}

// kimi adapter tests

func TestKimiAdapter_NameIsKimiCLI(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("kimi-cli")
	if a == nil {
		t.Fatal("expected kimi-cli adapter, got nil")
	}
	if a.Name() != "kimi-cli" {
		t.Errorf("Name() = %q, want %q", a.Name(), "kimi-cli")
	}
}

func TestKimiAdapter_MatchesBothSpellings(t *testing.T) {
	r := DefaultRegistry()
	for _, cmd := range []string{"kimi", "kimi-cli", "/usr/local/bin/kimi-cli", "./kimi"} {
		a := r.Lookup(cmd)
		if a == nil {
			t.Errorf("Lookup(%q) returned nil", cmd)
		}
	}
}

func TestKimiAdapter_BuildArgsAppendsAgentFile(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("kimi-cli")
	base := []string{"--chat"}
	got := a.BuildArgs(base, "my prompt")

	// First arg must be the base arg.
	if len(got) < 1 || got[0] != "--chat" {
		t.Fatalf("base args not preserved: %v", got)
	}
	// Must contain --agent-file followed by a non-empty path.
	flagIdx := -1
	for i, arg := range got {
		if arg == "--agent-file" {
			flagIdx = i
			break
		}
	}
	if flagIdx == -1 {
		t.Fatalf("--agent-file flag not found in args: %v", got)
	}
	if flagIdx+1 >= len(got) || got[flagIdx+1] == "" {
		t.Fatalf("--agent-file has no path argument: %v", got)
	}
	// Clean up temp dir.
	os.RemoveAll(filepath.Dir(got[flagIdx+1])) //nolint:errcheck
}

func TestKimiAdapter_EmptyPromptNoAgentFile(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("kimi-cli")
	base := []string{"--chat"}
	got := a.BuildArgs(base, "")
	for _, arg := range got {
		if arg == "--agent-file" {
			t.Errorf("empty prompt should not add --agent-file, got %v", got)
		}
	}
}

func TestKimiAdapter_InitialStdinIsNil(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("kimi-cli")
	if a.InitialStdin("prompt") != nil {
		t.Error("kimi InitialStdin should always be nil (file-based injection)")
	}
}

func TestKimiAdapter_StdinDelayIsZero(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("kimi-cli")
	if a.StdinDelay() != 0 {
		t.Errorf("kimi StdinDelay() = %v, want 0", a.StdinDelay())
	}
}

func TestKimiAdapter_TransportSSEAddsFlag(t *testing.T) {
	a := newKimiAdapterWithOptions(KimiOptions{Transport: KimiTransportSSE})
	got := a.BuildArgs(nil, "prompt")
	found := false
	for i, arg := range got {
		if arg == "--transport" && i+1 < len(got) && got[i+1] == "sse" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--transport sse not found in args: %v", got)
	}
	// Clean up temp file.
	for i, arg := range got {
		if arg == "--agent-file" && i+1 < len(got) {
			os.RemoveAll(filepath.Dir(got[i+1])) //nolint:errcheck
		}
	}
}

func TestKimiAdapter_TransportStreamableAddsFlag(t *testing.T) {
	a := newKimiAdapterWithOptions(KimiOptions{Transport: KimiTransportStreamable})
	got := a.BuildArgs(nil, "prompt")
	found := false
	for i, arg := range got {
		if arg == "--transport" && i+1 < len(got) && got[i+1] == "streamable" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--transport streamable not found in args: %v", got)
	}
	for i, arg := range got {
		if arg == "--agent-file" && i+1 < len(got) {
			os.RemoveAll(filepath.Dir(got[i+1])) //nolint:errcheck
		}
	}
}

func TestKimiAdapter_TransportCommandNoFlag(t *testing.T) {
	a := newKimiAdapterWithOptions(KimiOptions{Transport: KimiTransportCommand})
	got := a.BuildArgs(nil, "prompt")
	for _, arg := range got {
		if arg == "--transport" {
			t.Errorf("command transport should not add --transport flag, got %v", got)
		}
	}
	for i, arg := range got {
		if arg == "--agent-file" && i+1 < len(got) {
			os.RemoveAll(filepath.Dir(got[i+1])) //nolint:errcheck
		}
	}
}

func TestKimiAdapter_ExtraArgsPassedThrough(t *testing.T) {
	a := newKimiAdapterWithOptions(KimiOptions{
		ExtraArgs: []string{"--config", "/path/to/config", "--verbose"},
	})
	got := a.BuildArgs(nil, "")

	wantArgs := []string{"--config", "/path/to/config", "--verbose"}
	for _, want := range wantArgs {
		found := false
		for _, arg := range got {
			if arg == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("extra arg %q not found in %v", want, got)
		}
	}
}

func TestKimiAdapter_OverrideFlagName(t *testing.T) {
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"kimi-cli": {FlagName: "--system-file"},
	})
	a := r.Lookup("kimi-cli")
	got := a.BuildArgs(nil, "the prompt")
	flagIdx := -1
	for i, arg := range got {
		if arg == "--system-file" {
			flagIdx = i
			break
		}
	}
	if flagIdx == -1 {
		t.Fatalf("override flag --system-file not found: %v", got)
	}
	if flagIdx+1 >= len(got) {
		t.Fatalf("no path after override flag: %v", got)
	}
	os.RemoveAll(filepath.Dir(got[flagIdx+1])) //nolint:errcheck

	// Default flag must NOT appear.
	for _, arg := range got {
		if arg == "--agent-file" {
			t.Errorf("default --agent-file still present after override: %v", got)
		}
	}
}

func TestKimiAdapter_DisableInjection(t *testing.T) {
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"kimi-cli": {Disabled: true},
	})
	a := r.Lookup("kimi-cli")
	got := a.BuildArgs([]string{"--chat"}, "my prompt")
	if !equalStrings(got, []string{"--chat"}) {
		t.Errorf("disabled kimi still modified args: %v", got)
	}
	if a.InitialStdin("my prompt") != nil {
		t.Error("disabled kimi should return nil stdin")
	}
}

func TestWriteKimiAgentSpec(t *testing.T) {
	specPath, err := writeKimiAgentSpec("test content")
	if err != nil {
		t.Fatalf("writeKimiAgentSpec() error = %v", err)
	}
	defer os.RemoveAll(filepath.Dir(specPath))

	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("ReadFile agent.yaml error = %v", err)
	}
	if !strings.Contains(string(spec), "system_prompt_path: ./prompt.md") {
		t.Errorf("agent.yaml missing system_prompt_path: %s", spec)
	}

	promptPath := filepath.Join(filepath.Dir(specPath), "prompt.md")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("ReadFile prompt.md error = %v", err)
	}
	if string(data) != "test content" {
		t.Errorf("prompt.md content = %q, want %q", string(data), "test content")
	}
}

func TestBaseNameOf_HandlesWindowsPathsAndExe(t *testing.T) {
	cases := map[string]string{
		"claude":              "claude",
		"/usr/bin/claude":     "claude",
		"C:\\bin\\claude.exe": "claude",
		"./aider":             "aider",
		"CURSOR.EXE":          "cursor",
		"/opt/cursor-agent":   "cursor-agent",
	}
	for input, want := range cases {
		if got := baseNameOf(input); got != want {
			t.Errorf("baseNameOf(%q) = %q, want %q", input, got, want)
		}
	}
}

// helpers

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Sanity check: ensure the adapter note text survives round-tripping
// through a bytes buffer (guards against accidentally stripping the
// trailing newline in a future refactor).
func TestStdinAdapter_NoteIsSelfContainedLine(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("cursor")
	out := a.InitialStdin("X")
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("expected single line, got %d: %q", len(lines), out)
	}
}
