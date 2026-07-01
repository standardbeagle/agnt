package main

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// waitErrExit runs `sh -c "exit N"` and returns the Wait() error, giving a real
// *exec.ExitError to classify (rather than a hand-built fake).
func waitErrExit(t *testing.T, code int) error {
	t.Helper()
	c := exec.Command("sh", "-c", "exit "+strconv.Itoa(code))
	return c.Run()
}

// TestClassifyChildExit covers clean, non-zero, and signal exits with and
// without a user interrupt.
func TestClassifyChildExit(t *testing.T) {
	// Clean exit 0.
	if un, _ := classifyChildExit(waitErrExit(t, 0), false); un {
		t.Fatal("exit 0 should be clean")
	}

	// Non-zero exit → unexpected, reason names the status.
	un, reason := classifyChildExit(waitErrExit(t, 2), false)
	if !un || !strings.Contains(reason, "status 2") {
		t.Fatalf("exit 2: unexpected=%v reason=%q", un, reason)
	}

	// Killed by SIGKILL → unexpected, reason hints OOM/resource.
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	_ = c.Process.Kill()
	killErr := c.Wait()
	un, reason = classifyChildExit(killErr, false)
	if !un || !(strings.Contains(reason, "killed") || strings.Contains(reason, "SIGKILL")) {
		t.Fatalf("SIGKILL: unexpected=%v reason=%q", un, reason)
	}

	// A shell-level SIGINT exit (130) while the user interrupted → clean.
	if un, _ := classifyChildExit(waitErrExit(t, 130), true); un {
		t.Fatal("exit 130 with user interrupt should be clean")
	}
	// Same code without a user interrupt → unexpected.
	if un, _ := classifyChildExit(waitErrExit(t, 130), false); !un {
		t.Fatal("exit 130 without user interrupt should be unexpected")
	}
}

// TestResourceErrorTap verifies matching, dedup, capacity, and non-matches.
func TestResourceErrorTap(t *testing.T) {
	tap := newResourceErrorTap(3)
	tap.Observe("all good here")
	tap.Observe("build succeeded")
	if got := tap.Recent(); len(got) != 0 {
		t.Fatalf("non-matching lines captured: %v", got)
	}

	tap.Observe("Error: OS file watch limit reached")
	tap.Observe("Error: OS file watch limit reached") // consecutive dup dropped
	tap.Observe("EMFILE: too many open files, watch")
	tap.Observe("JavaScript heap out of memory")
	tap.Observe("write: no space left on device")
	got := tap.Recent()
	if len(got) != 3 {
		t.Fatalf("cap not enforced: %d entries: %v", len(got), got)
	}
	// Oldest (watch limit) should have been evicted; newest retained.
	if !strings.Contains(got[len(got)-1], "no space left") {
		t.Fatalf("newest not retained: %v", got)
	}
	for _, g := range got {
		if !resourceErrorPattern.MatchString(g) {
			t.Fatalf("captured non-resource line: %q", g)
		}
	}
}

// TestReportUnexpectedShutdown checks the persistent banner content and that a
// clean exit stays silent.
func TestReportUnexpectedShutdown(t *testing.T) {
	// Redirect selflog under a temp HOME so we don't touch the real log.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	var buf bytes.Buffer
	orig := shutdownWriter
	shutdownWriter = &buf
	t.Cleanup(func() { shutdownWriter = orig })

	// Clean exit → nothing printed.
	reportUnexpectedShutdown(nil, false, nil)
	if buf.Len() != 0 {
		t.Fatalf("clean exit printed: %q", buf.String())
	}

	// Unexpected exit with a resource line → banner with reason + bullet.
	buf.Reset()
	reportUnexpectedShutdown(waitErrExit(t, 137), false, []string{"OS file watch limit reached"})
	out := buf.String()
	for _, want := range []string{"agnt session ended unexpectedly", "reason:", "OS file watch limit reached", "agnt hook log"} {
		if !strings.Contains(out, want) {
			t.Fatalf("banner missing %q in:\n%s", want, out)
		}
	}
}
