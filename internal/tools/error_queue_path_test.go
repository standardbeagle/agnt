package tools

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/pathutil"
)

// error_queue must name the project the way every other tool does — through
// getProjectPath (AGNT_PROJECT_PATH, then cwd) and in normalised form. It used
// raw os.Getwd(), which ignored AGNT_PROJECT_PATH and, on a case-insensitive
// filesystem, never compared equal to the normalised scope a query filters by.
func TestResolveQueueProjectPath_UsesSessionProjectAndNormalises(t *testing.T) {
	project := t.TempDir()
	t.Setenv("AGNT_PROJECT_PATH", project)

	got, err := resolveQueueProjectPath("")
	if err != nil {
		t.Fatal(err)
	}
	if want := pathutil.NormalizeProjectPath(project); got != want {
		t.Errorf("default project path = %q, want AGNT_PROJECT_PATH normalised %q", got, want)
	}
	if got != getProjectPath() {
		t.Errorf("error_queue resolved %q but getProjectPath() says %q — the two must agree", got, getProjectPath())
	}

	explicit := t.TempDir()
	got, err = resolveQueueProjectPath(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if want := pathutil.NormalizeProjectPath(explicit); got != want {
		t.Errorf("explicit project path = %q, want normalised %q", got, want)
	}
}
