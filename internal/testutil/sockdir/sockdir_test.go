package sockdir

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The 104-byte sun_path limit is pinned here, not by a TMPDIR setting in a CI
// workflow: if Short ever hands out a directory too long to hold a socket
// name, this fails on every OS with the numbers, instead of some unrelated
// test failing on macOS with "bind: invalid argument".
func TestShortLeavesRoomForASocketName(t *testing.T) {
	dir := Short(t)
	if room := MaxPath - len(dir); room < NameBudget {
		t.Fatalf("sockdir.Short returned %q (%d bytes), leaving %d bytes for the separator and socket name; "+
			"tests are promised %d. A Unix socket path may be at most %d bytes (sun_path is 104 on macOS, less the NUL). "+
			"Base() is %q: on Unix it should be /tmp, on Windows C:\\tmp.",
			dir, len(dir), room, NameBudget, MaxPath, Base())
	}
}

// The promise is only worth something if a socket of the budgeted length
// really binds: this is the check that fails where the limit is tighter than
// MaxPath says.
func TestShortDirectoryHoldsASocketAtTheFullBudget(t *testing.T) {
	dir := Short(t)
	name := strings.Repeat("n", NameBudget-1-len(".sock")) + ".sock" // budget includes the separator
	path := filepath.Join(dir, name)
	if len(path) != len(dir)+NameBudget {
		t.Fatalf("test bug: path is %d bytes, want dir (%d) + budget (%d)", len(path), len(dir), NameBudget)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("binding a %d-byte socket path under Short's directory: %v", len(path), err)
	}
	ln.Close()
}

func TestShortIsFreshAndRemovedAfterTheTest(t *testing.T) {
	var first, second string
	t.Run("inner", func(t *testing.T) {
		first, second = Short(t), Short(t)
		if first == second {
			t.Fatalf("Short returned the same directory twice: %s", first)
		}
		for _, dir := range []string{first, second} {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("Short directory %s is not a fresh empty directory: entries=%v err=%v", dir, entries, err)
			}
		}
	})
	for _, dir := range []string{first, second} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("Short directory %s outlived its test: %v", dir, err)
		}
	}
}
