package pathutil

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeProjectPath(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "."} {
		if got := NormalizeProjectPath(in); got != "." {
			t.Errorf("NormalizeProjectPath(%q) = %q, want \".\"", in, got)
		}
	}

	dir := filepath.Join(t.TempDir(), "Mixed", "Case")
	got := NormalizeProjectPath(filepath.Join(dir, "..", "Case"))
	want := dir
	if runtime.GOOS == "windows" {
		want = strings.ToLower(dir)
	}
	if got != want {
		t.Errorf("NormalizeProjectPath = %q, want %q (absolute, cleaned, lowercased only on Windows)", got, want)
	}
	if again := NormalizeProjectPath(got); again != got {
		t.Errorf("not idempotent: %q -> %q", got, again)
	}
}
