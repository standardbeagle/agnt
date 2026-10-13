package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/selflog"
)

// `agnt hook log --help` must name the file the command reads on THIS machine.
// It used to print ${XDG_CACHE_HOME:-$HOME/.cache}/agnt/errors.log everywhere,
// which is wrong on macOS (~/Library/Caches) and Windows (%LocalAppData%).
func TestHookLogHelp_NamesTheRealLogPath(t *testing.T) {
	path := selflog.DefaultPath()
	if path == "" {
		t.Skip("no cache or home directory on this machine: there is no default path to print")
	}
	long := hookLogCmd.Long
	if !strings.Contains(long, path) {
		t.Fatalf("help does not contain the real log path %q:\n%s", path, long)
	}
	if strings.Contains(long, "${XDG_CACHE_HOME:-$HOME/.cache}/agnt/errors.log") {
		t.Fatalf("help still states the Linux-only path as if it applied everywhere:\n%s", long)
	}
}

func TestHookLogLong_FollowsThePathItIsGiven(t *testing.T) {
	custom := filepath.Join("some", "where", "errors.log")
	if got := hookLogLong(custom); !strings.Contains(got, custom) {
		t.Fatalf("hookLogLong(%q) does not mention it:\n%s", custom, got)
	}
	unknown := hookLogLong("")
	if !strings.Contains(unknown, "--path") || !strings.Contains(unknown, "AGNT_ERROR_LOG") {
		t.Fatalf("with no resolvable path the help must say how to supply one:\n%s", unknown)
	}
}
