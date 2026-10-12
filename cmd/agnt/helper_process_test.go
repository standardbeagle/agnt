package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testHelperEnv turns this test binary into a stand-in child process. Tests
// that need a real child with a known fate used to exec `sh -c "exit N"` and
// `sleep 30`, neither of which exists as an executable on native Windows; the
// re-exec'd test binary behaves the same on every OS. TestMain checks it
// before anything else (see testmain_test.go).
//
// Values: "exit:<code>" exits with that status; "sleep" blocks until killed
// (bounded, so a leaked helper cannot outlive the suite by long).
const testHelperEnv = "AGNT_CMDTEST_HELPER"

// runTestHelper is the child side. It never touches the filesystem.
func runTestHelper(spec string) int {
	switch {
	case strings.HasPrefix(spec, "exit:"):
		code, err := strconv.Atoi(strings.TrimPrefix(spec, "exit:"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad %s value %q: %v\n", testHelperEnv, spec, err)
			return 2
		}
		return code
	case spec == "sleep":
		time.Sleep(30 * time.Second)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown %s value %q\n", testHelperEnv, spec)
		return 2
	}
}

// helperProcess returns an unstarted command that re-execs the test binary in
// the given helper mode, with its working directory pinned to a temp dir so
// the child can never resolve anything against the repository tree.
func helperProcess(t *testing.T, spec string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating test binary for helper process: %v", err)
	}
	c := exec.Command(exe)
	c.Dir = t.TempDir()
	c.Env = append(os.Environ(), testHelperEnv+"="+spec)
	return c
}
