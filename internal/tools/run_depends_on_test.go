package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// awaitDependencyTimeout polls PROC STATUS until processID's dependency wait
// has failed, and returns the failure reason.
func awaitDependencyTimeout(t *testing.T, dt *DaemonTools, processID string) string {
	t.Helper()
	var reason string
	require.Eventually(t, func() bool {
		st, err := dt.client.ProcStatus(processID)
		if err != nil {
			return false
		}
		reason = getString(st, "failure_reason")
		return getString(st, "state") == "failed" && reason != ""
	}, 15*time.Second, 100*time.Millisecond, "the dependency wait never ended")
	return reason
}

// The run tool advertised depends_on and depends_on_timeout but sent neither:
// a raw command went out on RUN, which has no dependency gate, so it started
// at once. It must now be held pending on its dependency, and fail when the
// requested wait runs out.
func TestRunTool_RawDependsOnHoldsLaunch(t *testing.T) {
	// No t.Parallel(): starts a real daemon and, before the fix, a process.
	dt, _ := attachTestDaemon(t)
	require.NoError(t, dt.ensureConnected())
	project, err := os.Getwd()
	require.NoError(t, err)
	self, err := os.Executable()
	require.NoError(t, err)

	res, out, err := dt.makeRunHandler()(context.Background(), nil, RunInput{
		Path:             project,
		Raw:              true,
		Command:          self,
		Args:             []string{"-test.run=^$"},
		ID:               "dependent",
		DependsOn:        []string{"never-started"},
		DependsOnTimeout: 1,
	})
	require.NoError(t, err)
	require.Nil(t, res, "run must not fail: %+v", out)
	assert.Equal(t, "pending", out.State, "a process with an unready dependency must not start")
	assert.Contains(t, out.Hint, "never-started")

	reason := awaitDependencyTimeout(t, dt, out.ProcessID)
	assert.Equal(t, "dependency_timeout:never-started", reason)
}

// A script declared in .agnt.kdl takes its dependencies from the config. An
// inline depends_on is refused out loud rather than dropped, and
// depends_on_timeout bounds the configured wait.
func TestRunTool_DeclaredScriptDependencies(t *testing.T) {
	// No t.Parallel(): starts a real daemon.
	dt, _ := attachTestDaemon(t)
	require.NoError(t, dt.ensureConnected())
	project, err := os.Getwd()
	require.NoError(t, err)
	// depends-on with no timeout waits without bound.
	kdl := "scripts {\n    db {\n        run \"echo db\"\n    }\n    api {\n        run \"echo api\"\n        depends-on \"db\"\n    }\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(project, ".agnt.kdl"), []byte(kdl), 0o644))

	res, _, err := dt.makeRunHandler()(context.Background(), nil, RunInput{
		Path: project, ScriptName: "api", DependsOn: []string{"db"},
	})
	require.NoError(t, err)
	require.NotNil(t, res, "depends_on on a declared script must be refused")
	assert.True(t, res.IsError)
	assert.True(t, strings.Contains(resultText(res), ".agnt.kdl"), "the refusal must say where the dependencies come from: %s", resultText(res))

	res, out, err := dt.makeRunHandler()(context.Background(), nil, RunInput{
		Path: project, ScriptName: "api", DependsOnTimeout: 1,
	})
	require.NoError(t, err)
	require.Nil(t, res, "run must not fail: %+v", out)
	assert.Equal(t, "pending", out.State)
	assert.Equal(t, "dependency_timeout:db", awaitDependencyTimeout(t, dt, out.ProcessID),
		"depends_on_timeout must bound the configured wait")
}
