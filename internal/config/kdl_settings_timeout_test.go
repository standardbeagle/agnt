package config

import (
	"strings"
	"testing"
	"time"
)

// settingsKDL builds a minimal config document with the given settings body.
func settingsKDL(body string) string {
	return "version \"1.0\"\nsettings {\n" + body + "\n}\n"
}

// TestSettingsTimeout_FractionalDefaultTimeoutRefused pins the core bug: a
// fractional default-timeout must be REFUSED with an actionable error, never
// silently truncated to 0 (= the "no timeout / run forever" sentinel).
//
// FAIL-on-revert: if the whole-seconds guard is removed, kdl-go coerces 0.5 to
// 0 with a nil error and ParseKDLConfig succeeds — this test then fails because
// it requires a non-nil error whose message names the field, the value, and the
// granularity limit (provenance: our validation, not a generic parse error).
func TestSettingsTimeout_FractionalDefaultTimeoutRefused(t *testing.T) {
	_, err := ParseKDLConfig(settingsKDL("    default-timeout 0.5"))
	if err == nil {
		t.Fatalf("expected fractional default-timeout to be refused, got nil error (silent truncation to 0 = no timeout)")
	}
	msg := err.Error()
	for _, want := range []string{"default-timeout", "0.5", "sub-second", "whole number"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got: %s", want, msg)
		}
	}
}

// TestSettingsTimeout_FractionalGracefulTimeoutRefused aligns graceful-timeout
// with the same no-silent-truncation rule.
func TestSettingsTimeout_FractionalGracefulTimeoutRefused(t *testing.T) {
	_, err := ParseKDLConfig(settingsKDL("    graceful-timeout 0.5"))
	if err == nil {
		t.Fatalf("expected fractional graceful-timeout to be refused, got nil error")
	}
	msg := err.Error()
	for _, want := range []string{"graceful-timeout", "0.5", "sub-second"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got: %s", want, msg)
		}
	}
}

// TestSettingsTimeout_ZeroSentinelPreserved asserts default-timeout 0 keeps its
// "no timeout" meaning and is NOT mistaken for an invalid value.
func TestSettingsTimeout_ZeroSentinelPreserved(t *testing.T) {
	cfg, err := ParseKDLConfig(settingsKDL("    default-timeout 0\n    graceful-timeout 5"))
	if err != nil {
		t.Fatalf("default-timeout 0 must parse cleanly (no timeout sentinel), got: %v", err)
	}
	if cfg.Settings.DefaultTimeout != 0 {
		t.Errorf("default-timeout 0 must resolve to 0 (no timeout), got %v", cfg.Settings.DefaultTimeout)
	}
}

// TestSettingsTimeout_WholeSecondsAccepted asserts integer values still work
// end to end for both fields.
func TestSettingsTimeout_WholeSecondsAccepted(t *testing.T) {
	cfg, err := ParseKDLConfig(settingsKDL("    default-timeout 3\n    graceful-timeout 10"))
	if err != nil {
		t.Fatalf("whole-second timeouts must parse, got: %v", err)
	}
	if got := cfg.Settings.DefaultTimeout; got != 3*time.Second {
		t.Errorf("default-timeout 3 = %v, want 3s", got)
	}
	if got := cfg.Settings.GracefulTimeout; got != 10*time.Second {
		t.Errorf("graceful-timeout 10 = %v, want 10s", got)
	}
}
