package agentadapter

import (
	"bytes"
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
		"codex", "qwen", "crush", "kimi-cli",
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

func TestDefaultRegistry_KimiSpellingsUseKimiCLIAdapter(t *testing.T) {
	r := DefaultRegistry()
	for _, command := range []string{"kimi", "kimi-cli", "/usr/local/bin/kimi"} {
		a := r.Lookup(command)
		if a == nil || a.Name() != "kimi-cli" {
			t.Errorf("Lookup(%q) = %v, want kimi-cli adapter", command, a)
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

func TestUniversal_UnknownCommandUsesStdinPromptStrategy(t *testing.T) {
	// Verb-driven behavior: any command launched under `agnt run` still gets
	// an adapter. The fallback is stdin-based (BuildArgs unchanged,
	// InitialStdin carries the prompt) for setup-mode delivery; normal coding
	// sessions decide at the PTY pipeline whether to inject it.
	a := Universal("/usr/local/bin/myagent")
	if a == nil {
		t.Fatal("Universal must never return nil")
	}
	if a.Name() != "myagent" {
		t.Errorf("Name = %q, want derived base name %q", a.Name(), "myagent")
	}

	base := []string{"--flag"}
	got := a.BuildArgs(base, "PROMPT")
	if !equalStrings(got, []string{"--flag"}) {
		t.Errorf("BuildArgs = %v, want unchanged (stdin-based)", got)
	}

	// A non-empty prompt is carried as stdin when the caller elects to inject
	// it (setup mode); an empty prompt injects nothing.
	if stdin := string(a.InitialStdin("PROMPT\n\n")); stdin != "PROMPT\n" {
		t.Errorf("InitialStdin = %q, want normalized prompt", stdin)
	}
	if stdin := a.InitialStdin(""); stdin != nil {
		t.Errorf("InitialStdin(\"\") = %v, want nil", stdin)
	}
}

func TestUniversal_EmptyCommandStillUsable(t *testing.T) {
	a := Universal("")
	if a == nil || a.Name() == "" {
		t.Fatalf("Universal(\"\") must yield a usable adapter, got %v", a)
	}
}

func TestRegistry_ConfigAliasResolvesToAdapter(t *testing.T) {
	// A wrapper/alias command agnt cannot otherwise recognize ("cdsp") is
	// mapped to claude via config aliases, so it gets flag-based injection
	// instead of the universal stdin fallback.
	r := DefaultRegistry()
	if a := r.Lookup("cdsp"); a != nil {
		t.Fatalf("precondition: cdsp should not match before aliasing, got %v", a)
	}
	r.SetOverrides(map[string]Override{"claude": {Aliases: []string{"cdsp"}}})

	a := r.Lookup("cdsp")
	if a == nil || a.Name() != "claude" {
		t.Fatalf("Lookup(cdsp) = %v, want claude adapter", a)
	}
	// Flag-based injection, not stdin.
	got := a.BuildArgs([]string{"--x"}, "PROMPT")
	if !equalStrings(got, []string{"--x", "--append-system-prompt", "PROMPT"}) {
		t.Errorf("aliased claude must inject via flag, got %v", got)
	}
	if a.InitialStdin("PROMPT") != nil {
		t.Error("aliased claude must not inject via stdin")
	}

	// Alias matching is base-name based: a path to the wrapper resolves too.
	if a := r.Lookup("/usr/local/bin/cdsp"); a == nil || a.Name() != "claude" {
		t.Errorf("path to aliased command should resolve to claude, got %v", a)
	}

	// Clearing overrides drops the alias.
	r.SetOverrides(nil)
	if a := r.Lookup("cdsp"); a != nil {
		t.Errorf("alias should be gone after SetOverrides(nil), got %v", a)
	}
}

func TestRegistry_AliasWinsOverBuiltinMatch(t *testing.T) {
	// "gemini" is a real adapter command. Aliasing it onto claude must take
	// precedence over gemini's own built-in Matches, since an explicit
	// project mapping is the more specific intent.
	r := DefaultRegistry()
	if a := r.Lookup("gemini"); a == nil || a.Name() != "gemini" {
		t.Fatalf("precondition: gemini should match its own adapter, got %v", a)
	}
	r.SetOverrides(map[string]Override{"claude": {Aliases: []string{"gemini"}}})

	a := r.Lookup("gemini")
	if a == nil || a.Name() != "claude" {
		t.Fatalf("Lookup(gemini) = %v, want claude (alias wins over builtin)", a)
	}
}

func TestRegistry_AliasToUnknownAdapterFallsThrough(t *testing.T) {
	// An alias pointing at a non-existent adapter name must not break
	// resolution: it falls through to built-in matching.
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{"nope": {Aliases: []string{"claude"}}})

	// "claude" still resolves to the real claude adapter via Matches,
	// despite the dead alias mapping claude → nope.
	if a := r.Lookup("claude"); a == nil || a.Name() != "claude" {
		t.Fatalf("Lookup(claude) = %v, want claude despite dead alias", a)
	}
}

func TestRegistry_DuplicateAliasLastWins(t *testing.T) {
	// When two adapters declare the same alias base name, the last one
	// applied wins. (debug.Log warns; behavior we pin here is the resolve.)
	r := DefaultRegistry()
	r.SetOverrides(map[string]Override{
		"claude": {Aliases: []string{"dup"}},
		"gemini": {Aliases: []string{"dup"}},
	})
	a := r.Lookup("dup")
	if a == nil {
		t.Fatal("Lookup(dup) = nil, want an adapter")
	}
	// Map iteration order is non-deterministic, so assert the invariant
	// that actually matters: exactly one adapter owns the alias, and it is
	// a known adapter — never a half-resolved or nil result.
	switch a.Name() {
	case "claude", "gemini":
	default:
		t.Errorf("Lookup(dup) = %q, want claude or gemini", a.Name())
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

func TestStdinAdapter_InitialStdinCarriesPrompt(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("gemini")
	got := a.InitialStdin("THE_FULL_PROMPT_BODY\n\n")
	if len(got) == 0 {
		t.Fatal("expected non-empty stdin")
	}
	// The adapter strategy is literal stdin delivery. The PTY pipeline decides
	// whether to call this (setup mode) or rely on the context file (coding
	// mode).
	if string(got) != "THE_FULL_PROMPT_BODY\n" {
		t.Errorf("stdin = %q, want prompt plus single trailing newline", got)
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

// TestKimiAdapterNeverAddsUnverifiedFlags pins the fix for a real failure: agnt
// shipped a kimi adapter that appended --agent-file, a flag the kimi on
// PATH (kimi-code) rejects, so `agnt run kimi` died instantly with
// "error: unknown option '--agent-file'". The named adapter remains
// stdin-capable so it can provide a safe fallback, but setup normally reaches
// Kimi through its startup-loaded AGENTS.md context instead.
func TestKimiAdapterNeverAddsUnverifiedFlags(t *testing.T) {
	r := DefaultRegistry()
	for _, cmd := range []string{"kimi", "kimi-cli"} {
		a := r.Lookup(cmd)
		if a == nil || a.Name() != "kimi-cli" {
			t.Fatalf("Lookup(%q) = %v; want canonical kimi-cli adapter", cmd, a)
		}
		base := []string{"--model", "k2"}
		if got := a.BuildArgs(base, "my prompt"); !equalStrings(got, base) {
			t.Errorf("Lookup(%q) modified argv: %v", cmd, got)
		}
		if a.InitialStdin("my prompt") == nil {
			t.Errorf("Lookup(%q) has no safe stdin fallback", cmd)
		}
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

// Sanity check: ensure adapter stdin normalizes trailing newlines without
// stripping internal prompt structure.
func TestStdinAdapter_NormalizesTrailingNewlines(t *testing.T) {
	r := DefaultRegistry()
	a := r.Lookup("cursor")
	out := a.InitialStdin("A\nB\n\n")
	if string(out) != "A\nB\n" {
		t.Errorf("stdin = %q, want internal newline preserved and trailing newlines normalized", out)
	}
}
