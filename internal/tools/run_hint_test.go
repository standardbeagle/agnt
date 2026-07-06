package tools

import "testing"

// TestWebServerRunHint covers the proxy/browser-debug nudge: it fires for
// background web/dev-server launches (by script name or command) and stays
// silent for non-server commands and non-background modes.
func TestWebServerRunHint(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   RunInput
		command string
		want    bool // true => hint expected
	}{
		{"dev script", RunInput{ScriptName: "dev"}, "npm run dev", true},
		{"vite command", RunInput{Raw: true}, "vite --host", true},
		{"next start", RunInput{ScriptName: "start"}, "next start", true},
		{"dotnet watch", RunInput{Raw: true}, "dotnet watch run", true},
		{"django runserver", RunInput{Raw: true}, "python manage.py runserver", true},
		{"test script - no hint", RunInput{ScriptName: "test"}, "go test ./...", false},
		{"lint - no hint", RunInput{ScriptName: "lint"}, "eslint .", false},
		{"foreground dev - no hint", RunInput{ScriptName: "dev", Mode: RunModeForeground}, "npm run dev", false},
		{"foreground-raw serve - no hint", RunInput{Mode: RunModeForegroundRaw}, "http-server", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := webServerRunHint(tc.input, tc.command)
			if tc.want && got == "" {
				t.Errorf("expected a hint, got none")
			}
			if !tc.want && got != "" {
				t.Errorf("expected no hint, got %q", got)
			}
		})
	}
}
