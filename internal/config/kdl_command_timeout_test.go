package config

import (
	"strings"
	"testing"
)

// projectCommandKDL builds a minimal per-project config document with one
// command whose body is the given lines (e.g. `cmd "go"` + `timeout 0.5`).
func projectCommandKDL(name, body string) string {
	return "commands {\n    " + name + " {\n" + body + "\n    }\n}\n"
}

// langCommandKDL builds a full config document with one Go-language command.
func langCommandKDL(name, body string) string {
	return "version \"1.0\"\nlanguages {\n    go {\n        commands {\n            " + name + " {\n" + body + "\n            }\n        }\n    }\n}\n"
}

// TestCommandTimeout_FractionalRefused pins the core bug: a fractional per-command
// timeout must be REFUSED with an actionable error, never silently truncated to 0
// (= the "no timeout / run forever" sentinel). kdl-go coerces 0.5 into the int
// consumer field as 0 with a nil error, so without the whole-seconds guard a
// half-second limit becomes an unbounded run.
//
// FAIL-on-revert: remove the guard and ParseProjectConfig succeeds with nil error,
// failing this test which requires a non-nil error naming the command, the value,
// and the granularity limit (provenance: our validation, not a generic parse error).
func TestCommandTimeout_FractionalRefused(t *testing.T) {
	_, err := ParseProjectConfig(projectCommandKDL("test", "        cmd \"go\"\n        timeout 0.5"))
	if err == nil {
		t.Fatalf("expected fractional command timeout to be refused, got nil error (silent truncation to 0 = no timeout)")
	}
	msg := err.Error()
	for _, want := range []string{"test", "timeout", "0.5", "sub-second", "whole number"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got: %s", want, msg)
		}
	}
}

// TestCommandTimeout_FractionalRefusedInLanguageBlock asserts the same guard
// covers the other parse path: commands nested under languages.<lang>.
func TestCommandTimeout_FractionalRefusedInLanguageBlock(t *testing.T) {
	_, err := ParseKDLConfig(langCommandKDL("build", "                cmd \"go\"\n                timeout 1.5"))
	if err == nil {
		t.Fatalf("expected fractional language-command timeout to be refused, got nil error")
	}
	msg := err.Error()
	for _, want := range []string{"build", "timeout", "1.5", "sub-second"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got: %s", want, msg)
		}
	}
}

// TestCommandTimeout_ZeroSentinelPreserved asserts timeout 0 keeps its
// "no timeout" meaning and is NOT mistaken for an invalid value.
func TestCommandTimeout_ZeroSentinelPreserved(t *testing.T) {
	cfg, err := ParseProjectConfig(projectCommandKDL("test", "        cmd \"go\"\n        timeout 0"))
	if err != nil {
		t.Fatalf("timeout 0 must parse cleanly (no timeout sentinel), got: %v", err)
	}
	if got := cfg.Commands["test"].Timeout; got != 0 {
		t.Errorf("timeout 0 must resolve to 0 (no timeout), got %v", got)
	}
}

// TestCommandTimeout_WholeSecondsAccepted asserts integer values still work
// end to end and land losslessly in the int consumer field.
func TestCommandTimeout_WholeSecondsAccepted(t *testing.T) {
	cfg, err := ParseProjectConfig(projectCommandKDL("test", "        cmd \"go\"\n        timeout 30"))
	if err != nil {
		t.Fatalf("whole-second command timeout must parse, got: %v", err)
	}
	if got := cfg.Commands["test"].Timeout; got != 30 {
		t.Errorf("timeout 30 = %v, want 30", got)
	}
}
