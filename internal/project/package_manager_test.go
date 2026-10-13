package project

import (
	"os"
	"path/filepath"
	"testing"
)

// detectPackageManager goes by lockfile. Bun has two: the binary bun.lockb and
// the text bun.lock it writes by default since 1.2. Only the former was
// recognised, so current bun projects were detected as npm.
func TestDetectPackageManager_Lockfiles(t *testing.T) {
	cases := map[string]string{
		"pnpm-lock.yaml": "pnpm",
		"yarn.lock":      "yarn",
		"bun.lock":       "bun",
		"bun.lockb":      "bun",
		"":               "npm",
	}
	for lockfile, want := range cases {
		dir := t.TempDir()
		if lockfile != "" {
			if err := os.WriteFile(filepath.Join(dir, lockfile), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := detectPackageManager(dir); got != want {
			t.Errorf("lockfile %q: detectPackageManager = %q, want %q", lockfile, got, want)
		}
	}
}

func TestDetectGo_WailsFrontendUsesBunLock(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":            "module example.com/app\n",
		"wails.json":        "{}",
		"frontend/bun.lock": "",
	} {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj := detectGo(dir)
	if proj == nil {
		t.Fatal("detectGo returned nil")
	}
	if got := proj.Metadata["frontend_package_manager"]; got != "bun" {
		t.Errorf("frontend_package_manager = %q, want bun", got)
	}
}
