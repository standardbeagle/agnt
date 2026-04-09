package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPathMatches(t *testing.T) {
	tests := []struct {
		name        string
		projectPath string
		procCwd     string
		want        bool
	}{
		{"exact match", "/home/user/project", "/home/user/project", true},
		{"subdirectory", "/home/user/project", "/home/user/project/src", true},
		{"different project", "/home/user/project", "/home/user/other", false},
		{"empty cwd", "/home/user/project", "", false},
		{"empty project", "", "/home/user/project", false},
		{"sibling prefix", "/home/user/proj", "/home/user/project", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathMatches(tt.projectPath, tt.procCwd)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDevServerCommandsList(t *testing.T) {
	// Verify all required dev server commands are in the map
	required := []string{
		"node", "npm", "npx", "vite", "next", "webpack", "ts-node", "nodemon", "turbo",
		"go", "air",
		"dotnet",
		"flask", "uvicorn", "gunicorn", "python", "python3", "django",
		"cargo",
		"rails", "foreman", "bundle",
		"java", "mvn", "gradle",
	}

	for _, cmd := range required {
		_, ok := devServerCommands[cmd]
		assert.True(t, ok, "missing dev server command: %s", cmd)
	}
}

func TestDuplicateScannerNotify(t *testing.T) {
	// Test that the notification message is formatted correctly
	scanner := &DuplicateScanner{}

	var capturedMsg string
	scanner.OnNotify = func(msg string) {
		capturedMsg = msg
	}

	result := &CleanupResult{
		Killed: []KilledProcess{
			{PID: 12345, Command: "node", Reason: "duplicate node"},
			{PID: 12346, Command: "node", Reason: "duplicate node"},
			{PID: 12350, Command: "dotnet", Reason: "duplicate dotnet"},
		},
	}

	scanner.notify(result)

	assert.Contains(t, capturedMsg, "[agnt] cleaned up 3 duplicate process(es)")
	assert.Contains(t, capturedMsg, "dotnet (PID 12350)")
	assert.Contains(t, capturedMsg, "node (PID 12345, PID 12346)")
}

func TestDuplicateScannerNotifyEmpty(t *testing.T) {
	scanner := &DuplicateScanner{}
	called := false
	scanner.OnNotify = func(msg string) {
		called = true
	}

	scanner.notify(&CleanupResult{})
	assert.False(t, called, "notify should not be called for empty result")
}
