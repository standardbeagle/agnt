package snapshot

import (
	"image/color"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitRepo makes a one-commit repository on branch snap-test in a temp dir and
// returns its path and full HEAD hash. Every git call sets cmd.Dir: the
// test's own working directory is not isolation for a child process.
func gitRepo(t *testing.T) (dir, head string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "snap-test")
	run("-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false",
		"commit", "-q", "--allow-empty", "-m", "init")
	return dir, run("rev-parse", "HEAD")
}

// A baseline records the project's git commit and branch. getGitInfo used to
// call Run and then Output on the same exec.Cmd, so the second call always
// failed ("already started") and both fields were always empty. git must also
// run in the project directory, not wherever the process happens to be.
func TestCreateBaseline_RecordsGitCommitAndBranch(t *testing.T) {
	repo, head := gitRepo(t)
	defer chdir(t, t.TempDir())() // the process cwd is not the project

	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)
	m.projectDir = repo
	b, err := m.CreateBaseline("g", []PageCapture{{URL: "http://a/", ScreenshotData: encodePNGBase64(t, 1, 1, color.RGBA{A: 255})}})
	require.NoError(t, err)
	assert.Equal(t, head[:7], b.GitCommit)
	assert.Equal(t, "snap-test", b.GitBranch)
}
