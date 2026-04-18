package agentadapter

import (
	"bytes"
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
	for _, name := range []string{
		"claude", "gemini", "copilot", "aider",
		"cursor", "cursor-agent", "opencode",
		"kimi", "kimi-cli", "auggie",
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
