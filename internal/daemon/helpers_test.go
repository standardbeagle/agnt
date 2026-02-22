package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// rootPath returns the filesystem root for the current platform.
func rootPath() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

// shortTempDir creates a temp directory with a short path to stay within
// the unix socket path length limit (~108 chars) on Windows.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if runtime.GOOS == "windows" {
		// Use a short base to avoid exceeding socket path limits.
		base = `C:\tmp`
		os.MkdirAll(base, 0755)
	}
	dir, err := os.MkdirTemp(base, "d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// shortSockPath returns a socket path short enough for Windows unix sockets.
func shortSockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempDir(t), "s.sock")
}
