package config

import "testing"

func TestParseAgntConfigScopeDefaultGlobal(t *testing.T) {
	tests := []struct {
		input          string
		present, value bool
	}{
		{input: ""},
		{input: "scope {\n  default-global false\n}\n", present: true},
		{input: "scope {\n  default-global true\n}\n", present: true, value: true},
	}
	for _, tc := range tests {
		cfg, err := ParseAgntConfig(tc.input)
		if err != nil {
			t.Fatalf("ParseAgntConfig(%q): %v", tc.input, err)
		}
		if (cfg.Scope != nil) != tc.present {
			t.Fatalf("scope presence for %q = %v", tc.input, cfg.Scope != nil)
		}
		if tc.present && cfg.Scope.DefaultGlobal != tc.value {
			t.Fatalf("default-global for %q = %v", tc.input, cfg.Scope.DefaultGlobal)
		}
	}
}
