//go:build windows

package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// The version comes from the install layout: running chrome.exe to ask it
// starts a browser that never exits.
func TestCheckVersionAt_ReadsInstallLayout(t *testing.T) {
	app := t.TempDir()
	for _, dir := range []string{"9.0.1.2", "146.0.7680.164", "SetupMetrics"} {
		if err := os.Mkdir(filepath.Join(app, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A path that is not executable proves nothing was run to get the answer.
	chrome := filepath.Join(app, "chrome.exe")
	if err := os.WriteFile(chrome, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}

	info := CheckVersionAt(chrome)
	if info.FullVersion != "146.0.7680.164" || info.MajorVersion != 146 {
		t.Fatalf("version = %q (major %d), want the numerically highest version directory", info.FullVersion, info.MajorVersion)
	}
	if info.Path != chrome {
		t.Fatalf("Path = %q, want %q", info.Path, chrome)
	}
}

func TestCheckVersionAt_NoVersionDirectory(t *testing.T) {
	chrome := filepath.Join(t.TempDir(), "chrome.exe")
	info := CheckVersionAt(chrome)
	if info.FullVersion != "" || info.Path != chrome {
		t.Fatalf("unknown layout must yield an empty version, got %+v", info)
	}
}
