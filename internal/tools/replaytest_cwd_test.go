package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/replaytest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplaytestWritesToCallerDirNotCwd pins the fix for the cwd-relative write
// defect: replaytest scenario/report files must land in the caller-supplied
// directory, never in the process working directory. The test has teeth two
// ways — it positively reads the scenario back out of the caller dir and
// asserts the store never appears under the process cwd, and it proves a write
// action with no explicit destination refuses rather than falling back to cwd
// (which would dirty the working tree — the AGNT.md-leak defect class).
// Reverting replaytestDir to the old getProjectPath() cwd fallback fails this
// test on both the ambient-record error and the cwd-cleanliness assertion.
func TestReplaytestWritesToCallerDirNotCwd(t *testing.T) {
	// Isolate cwd and env: getProjectPath()'s cwd fallback (if reintroduced)
	// would resolve to workDir, and no ambient AGNT_PROJECT_PATH may leak in.
	workDir := t.TempDir()
	origWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	origEnv, hadEnv := os.LookupEnv("AGNT_PROJECT_PATH")
	require.NoError(t, os.Unsetenv("AGNT_PROJECT_PATH"))
	t.Cleanup(func() {
		if hadEnv {
			_ = os.Setenv("AGNT_PROJECT_PATH", origEnv)
		}
	})

	fake := &fakeLogClient{
		target: "http://localhost:3000",
		entries: []proxy.LogEntry{{
			Type: proxy.LogTypeHTTP,
			HTTP: &proxy.HTTPLogEntry{
				ID: "r1", Method: "GET", URL: "http://localhost:3000/api/x",
				StatusCode: 200, ResponseBody: `{"ok":true}`,
			},
		}},
	}
	h := newReplaytestHandler(grantingManager(t), func() (replaytestLogClient, error) { return fake, nil })

	// --- Positive: an explicit caller directory receives the artifact. ---
	projectDir := t.TempDir() // distinct from workDir (the cwd)
	rec, _, err := h.handle(context.Background(), ReplaytestInput{Action: "record", Name: "checkout", ProxyID: "px", Directory: projectDir})
	require.NoError(t, err)
	require.False(t, rec.IsError, resultText(rec))
	stop, stopOut, err := h.handle(context.Background(), ReplaytestInput{Action: "stop", Name: "checkout", Directory: projectDir})
	require.NoError(t, err)
	require.False(t, stop.IsError, resultText(stop))
	require.True(t, stopOut.Success)

	// The scenario is readable back out of the caller-supplied dir...
	sc, err := replaytest.NewStore(projectDir).LoadScenario("checkout")
	require.NoError(t, err)
	assert.Equal(t, "checkout", sc.Name)
	assert.FileExists(t, filepath.Join(projectDir, ".agnt", "replaytests", "checkout.json"))

	// ...and NOT in the process working directory.
	assert.NoDirExists(t, filepath.Join(workDir, ".agnt"),
		"replaytest must not write the scenario store into the process cwd")

	// --- Teeth: a write action with no explicit dir and no AGNT_PROJECT_PATH
	// must ERROR, never fall back to cwd. On revert, record would seed the
	// session with workDir (cwd) and a following stop would write the scenario
	// into workDir/.agnt. ---
	rec2, _, err := h.handle(context.Background(), ReplaytestInput{Action: "record", Name: "ambient", ProxyID: "px", Directory: ""})
	require.NoError(t, err)
	require.True(t, rec2.IsError, "record with no explicit directory must refuse the ambient cwd")
	assert.Contains(t, resultText(rec2), "project directory")

	// Even if a stop somehow followed, nothing may have been written to cwd.
	_, _, _ = h.handle(context.Background(), ReplaytestInput{Action: "stop", Name: "ambient", Directory: ""})
	assert.NoDirExists(t, filepath.Join(workDir, ".agnt"),
		"a directory-less write action must not create the store in the process cwd")
}
