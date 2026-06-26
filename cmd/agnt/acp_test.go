//go:build unix

package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveACPLaunch(t *testing.T) {
	defer func() { acpModel = "" }()

	tests := []struct {
		name  string
		agent string
		model string
		extra []string
		want  []string
	}{
		{"gemini bare", "gemini", "", nil, []string{"gemini", "--experimental-acp"}},
		{"gemini with model", "gemini", "gemini-2.0-flash", nil, []string{"gemini", "--experimental-acp", "-m", "gemini-2.0-flash"}},
		{"opencode acp subcommand", "opencode", "", nil, []string{"opencode", "acp"}},
		{"opencode ignores model (no flag)", "opencode", "anthropic/claude", nil, []string{"opencode", "acp"}},
		{"claude adapter", "claude", "", nil, []string{"claude-code-acp"}},
		{"unknown verbatim", "mycli", "", nil, []string{"mycli"}},
		{"unknown with extra args", "mycli", "", []string{"acp", "--flag"}, []string{"mycli", "acp", "--flag"}},
		{"known with extra args", "gemini", "", []string{"--debug"}, []string{"gemini", "--experimental-acp", "--debug"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acpModel = tt.model
			got := resolveACPLaunch(tt.agent, tt.extra)
			if len(got) != len(tt.want) {
				t.Fatalf("len mismatch: got %v want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("argv[%d]: got %q want %q (full got %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

func TestSanitizeAgentName(t *testing.T) {
	cases := map[string]string{
		"gemini":             "gemini",
		"/path/to/agent":     "agent",
		"agent.bin":          "agent",
		"/tmp/termagent.bin": "termagent",
		"":                   "agent",
		"opencode":           "opencode",
	}
	for in, want := range cases {
		if got := sanitizeAgentName(in); got != want {
			t.Errorf("sanitizeAgentName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestACPArgs drives a real cobra parse so ArgsLenAtDash is populated the
// same way it is in production.
func TestACPArgs(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		wantAgent  string
		wantPrompt string
		wantExtra  []string
	}{
		{"agent only", []string{"gemini"}, "gemini", "", nil},
		{"agent + prompt", []string{"gemini", "fix the lint"}, "gemini", "fix the lint", nil},
		{"agent + dash extra", []string{"mycli", "--", "acp", "--flag"}, "mycli", "", []string{"acp", "--flag"}},
		{"agent + prompt + dash extra", []string{"opencode", "do it", "--", "acp"}, "opencode", "do it", []string{"acp"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAgent, gotPrompt string
			var gotExtra []string
			cmd := &cobra.Command{
				Use:  "acp",
				Args: cobra.MinimumNArgs(1),
				Run: func(c *cobra.Command, args []string) {
					gotAgent, gotPrompt, gotExtra = acpArgs(c, args)
				},
			}
			cmd.SetArgs(tt.argv)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if gotAgent != tt.wantAgent {
				t.Errorf("agent: got %q want %q", gotAgent, tt.wantAgent)
			}
			if gotPrompt != tt.wantPrompt {
				t.Errorf("prompt: got %q want %q", gotPrompt, tt.wantPrompt)
			}
			if len(gotExtra) != len(tt.wantExtra) {
				t.Fatalf("extra: got %v want %v", gotExtra, tt.wantExtra)
			}
			for i := range gotExtra {
				if gotExtra[i] != tt.wantExtra[i] {
					t.Errorf("extra[%d]: got %q want %q", i, gotExtra[i], tt.wantExtra[i])
				}
			}
		})
	}
}
