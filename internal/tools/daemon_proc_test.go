package tools

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPopulateLastExitFields verifies that a daemon response map
// containing last_exit_* keys populates the ProcOutput correctly. This
// is the wire-level contract for the death record surfaced through
// proc status.
func TestPopulateLastExitFields(t *testing.T) {
	t.Run("full record", func(t *testing.T) {
		resp := map[string]interface{}{
			"last_exit_at":     "2026-04-13T16:30:00Z",
			"last_exit_code":   float64(1), // JSON numbers decode as float64
			"last_exit_reason": "crash",
			"last_uptime":      "12m34s",
			"last_stderr_tail": "[vite] Internal server error",
		}

		var out ProcOutput
		populateLastExitFields(&out, resp)

		assert.Equal(t, "2026-04-13T16:30:00Z", out.LastExitAt)
		require.NotNil(t, out.LastExitCode, "LastExitCode must be populated")
		assert.Equal(t, 1, *out.LastExitCode)
		assert.Equal(t, "crash", out.LastExitReason)
		assert.Equal(t, "12m34s", out.LastUptime)
		assert.Equal(t, "[vite] Internal server error", out.LastStderrTail)
	})

	t.Run("missing last_exit_code leaves pointer nil", func(t *testing.T) {
		resp := map[string]interface{}{
			"last_exit_at": "2026-04-13T16:30:00Z",
		}
		var out ProcOutput
		populateLastExitFields(&out, resp)
		assert.Nil(t, out.LastExitCode, "absent field must leave pointer nil (distinguishes from a real 0)")
		assert.Equal(t, "2026-04-13T16:30:00Z", out.LastExitAt)
	})

	t.Run("zero exit code is still populated when key present", func(t *testing.T) {
		resp := map[string]interface{}{
			"last_exit_code":   float64(0),
			"last_exit_reason": "stopped",
		}
		var out ProcOutput
		populateLastExitFields(&out, resp)
		require.NotNil(t, out.LastExitCode)
		assert.Equal(t, 0, *out.LastExitCode, "zero is distinguishable from absent via pointer")
		assert.Equal(t, "stopped", out.LastExitReason)
	})

	t.Run("empty response is a no-op", func(t *testing.T) {
		var out ProcOutput
		populateLastExitFields(&out, map[string]interface{}{})
		assert.Empty(t, out.LastExitAt)
		assert.Nil(t, out.LastExitCode)
	})
}

// TestPopulateLastExitFieldsEntry verifies the per-entry variant used by
// proc list (operates on ProcEntry instead of ProcOutput).
func TestPopulateLastExitFieldsEntry(t *testing.T) {
	resp := map[string]interface{}{
		"last_exit_at":     "2026-04-13T16:30:00Z",
		"last_exit_code":   float64(42),
		"last_exit_reason": "crash",
		"last_uptime":      "5s",
		"last_stderr_tail": "panic: runtime error",
	}
	var entry ProcEntry
	populateLastExitFieldsEntry(&entry, resp)
	assert.Equal(t, "2026-04-13T16:30:00Z", entry.LastExitAt)
	require.NotNil(t, entry.LastExitCode)
	assert.Equal(t, 42, *entry.LastExitCode)
	assert.Equal(t, "crash", entry.LastExitReason)
	assert.Equal(t, "5s", entry.LastUptime)
	assert.Equal(t, "panic: runtime error", entry.LastStderrTail)
}

// TestClassifyProcess verifies that classifyProcess correctly identifies
// process roles and what they produce based on command strings.
func TestClassifyProcess(t *testing.T) {
	tests := []struct {
		name         string
		command      string
		wantRole     string
		wantProduces []string // must all appear in produces
	}{
		{
			name:         "dotnet watch",
			command:      "dotnet watch run",
			wantRole:     "build-watch",
			wantProduces: []string{"build-output"},
		},
		{
			name:         "dotnet-watch hyphenated",
			command:      "/usr/bin/dotnet-watch run --project backend.csproj",
			wantRole:     "build-watch",
			wantProduces: []string{"build-output"},
		},
		{
			name:         "tsc watch",
			command:      "tsc --watch --noEmitOnError",
			wantRole:     "build-watch",
			wantProduces: []string{"build-output"},
		},
		{
			name:         "webpack dev server",
			command:      "webpack-dev-server --hot",
			wantRole:     "build-watch",
			wantProduces: []string{"build-output", "hot-reload"},
		},
		{
			name:         "webpack serve",
			command:      "webpack serve",
			wantRole:     "build-watch",
			wantProduces: []string{"build-output"},
		},
		{
			name:         "vite dev server",
			command:      "vite --port 3000",
			wantRole:     "dev-server",
			wantProduces: []string{"build-output", "hot-reload"},
		},
		{
			name:         "cargo watch",
			command:      "cargo watch -x run",
			wantRole:     "build-watch",
			wantProduces: []string{"build-output"},
		},
		{
			name:         "next dev",
			command:      "next dev",
			wantRole:     "dev-server",
			wantProduces: []string{"build-output", "hot-reload"},
		},
		{
			name:         "generic --watch flag",
			command:      "some-compiler --watch --output dist",
			wantRole:     "build-watch",
			wantProduces: []string{"build-output"},
		},
		{
			name:         "unknown process",
			command:      "python manage.py runserver",
			wantRole:     "unknown",
			wantProduces: []string{"logs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyProcess(tt.command)
			assert.Equal(t, tt.wantRole, got.Role)
			for _, p := range tt.wantProduces {
				assert.Contains(t, got.Produces, p, "produces should contain %q", p)
			}
			assert.NotEmpty(t, got.OutputHint, "output_hint should be non-empty")
		})
	}
}

// TestBuildWhatPattern verifies that buildWhatPattern returns valid regex
// patterns for known intent strings and errors on unknown ones.
func TestBuildWhatPattern(t *testing.T) {
	t.Run("build-warnings dotnet returns valid pattern", func(t *testing.T) {
		pattern, err := buildWhatPattern("build-warnings", "dotnet watch run")
		require.NoError(t, err)
		re, reErr := regexp.Compile(pattern)
		require.NoError(t, reErr, "pattern must compile")
		assert.True(t, re.MatchString("src/Foo.cs(12,5): warning CS0168: unused variable"), "must match dotnet warning")
		assert.False(t, re.MatchString("Build succeeded."), "must not match non-warning line")
	})

	t.Run("build-errors generic", func(t *testing.T) {
		pattern, err := buildWhatPattern("build-errors", "webpack serve")
		require.NoError(t, err)
		re, reErr := regexp.Compile(pattern)
		require.NoError(t, reErr)
		assert.True(t, re.MatchString("ERROR in ./src/index.ts"), "must match webpack error")
	})

	t.Run("type-errors tsc", func(t *testing.T) {
		pattern, err := buildWhatPattern("type-errors", "tsc --watch")
		require.NoError(t, err)
		re, reErr := regexp.Compile(pattern)
		require.NoError(t, reErr)
		assert.True(t, re.MatchString("src/app.ts(10,3): error TS2345: Argument of type"), "must match tsc error")
	})

	t.Run("test-failures", func(t *testing.T) {
		pattern, err := buildWhatPattern("test-failures", "go test ./...")
		require.NoError(t, err)
		re, reErr := regexp.Compile(pattern)
		require.NoError(t, reErr)
		assert.True(t, re.MatchString("--- FAIL: TestFoo (0.01s)"), "must match go test failure")
		assert.True(t, re.MatchString("FAILED"), "must match FAILED line")
	})

	t.Run("compile-errors is alias for build-errors", func(t *testing.T) {
		p1, err1 := buildWhatPattern("compile-errors", "dotnet watch")
		p2, err2 := buildWhatPattern("build-errors", "dotnet watch")
		require.NoError(t, err1)
		require.NoError(t, err2)
		assert.Equal(t, p1, p2, "compile-errors and build-errors should produce the same pattern for the same tool")
	})

	t.Run("unknown what returns error", func(t *testing.T) {
		_, err := buildWhatPattern("nonsense", "dotnet watch")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown what")
	})
}
