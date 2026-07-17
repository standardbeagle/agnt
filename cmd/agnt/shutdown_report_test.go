package main

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/selflog"
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

// TestChildOutputTapResource verifies resource matching, dedup, capacity, and
// that non-matching lines stay out of the resource list.
func TestChildOutputTapResource(t *testing.T) {
	tap := newChildOutputTap(10, 3)
	tap.Observe("all good here")
	tap.Observe("build succeeded")
	if got := tap.Resource(); len(got) != 0 {
		t.Fatalf("non-matching lines captured as resource errors: %v", got)
	}

	tap.Observe("Error: OS file watch limit reached")
	tap.Observe("Error: OS file watch limit reached") // consecutive dup dropped
	tap.Observe("EMFILE: too many open files, watch")
	tap.Observe("JavaScript heap out of memory")
	tap.Observe("write: no space left on device")
	got := tap.Resource()
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

// TestChildOutputTapTail pins the property the resource-only tap lacked: an
// arbitrary fatal error the pattern list knows nothing about is still
// captured, so the shutdown report can quote it.
func TestChildOutputTapTail(t *testing.T) {
	tap := newChildOutputTap(3, 5)
	tap.Observe("  ")   // blank dropped
	tap.Observe("\t\n") // whitespace-only dropped
	if got := tap.Tail(); len(got) != 0 {
		t.Fatalf("blank lines captured: %v", got)
	}

	// None of these match resourceErrorPattern — that is the point.
	tap.Observe("starting up")
	tap.Observe("error: unknown option '--agent-file'")
	tap.Observe("error: unknown option '--agent-file'") // consecutive dup dropped
	if got := tap.Resource(); len(got) != 0 {
		t.Fatalf("non-resource lines leaked into resource list: %v", got)
	}
	got := tap.Tail()
	if len(got) != 2 || got[len(got)-1] != "error: unknown option '--agent-file'" {
		t.Fatalf("tail did not capture the fatal line: %v", got)
	}

	// Capacity evicts oldest, keeps newest.
	tap.Observe("second")
	tap.Observe("third")
	got = tap.Tail()
	if len(got) != 3 || got[0] != "error: unknown option '--agent-file'" || got[2] != "third" {
		t.Fatalf("cap/order wrong: %v", got)
	}

	// Over-long lines are truncated rather than flooding the banner.
	tap.Observe(strings.Repeat("x", 500))
	last := tap.Tail()[2]
	if len(last) > 320 || !strings.HasSuffix(last, "…") {
		t.Fatalf("long line not truncated: len=%d", len(last))
	}
}

// TestInjectedArgs covers the append case, the no-injection case, and the
// defensive "adapter rewrote args" case that must report nothing.
func TestInjectedArgs(t *testing.T) {
	base := []string{"--model", "opus"}
	if got := injectedArgs(base, []string{"--model", "opus", "--agent-file", "/tmp/x"}); len(got) != 2 || got[0] != "--agent-file" {
		t.Fatalf("append case: %v", got)
	}
	if got := injectedArgs(base, []string{"--model", "opus"}); got != nil {
		t.Fatalf("no injection should report nothing: %v", got)
	}
	if got := injectedArgs(base, []string{"--rewritten", "opus", "--extra"}); got != nil {
		t.Fatalf("rewritten prefix should report nothing rather than guess: %v", got)
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

	kimi := agentLaunch{
		Command:  "kimi",
		Args:     []string{"--agent-file", "/tmp/agnt-kimi-1/agent.yaml"},
		Injected: []string{"--agent-file", "/tmp/agnt-kimi-1/agent.yaml"},
		Adapter:  "kimi-cli",
	}

	// Clean exit → nothing printed.
	reportUnexpectedShutdown(nil, false, kimi, nil, nil)
	if buf.Len() != 0 {
		t.Fatalf("clean exit printed: %q", buf.String())
	}

	// Unexpected exit with a resource line → banner with reason + bullet.
	buf.Reset()
	reportUnexpectedShutdown(waitErrExit(t, 137), false, kimi, nil, []string{"OS file watch limit reached"})
	out := buf.String()
	for _, want := range []string{"ended unexpectedly", "status 137", "OS file watch limit reached", "agnt hook log"} {
		if !strings.Contains(out, want) {
			t.Fatalf("banner missing %q in:\n%s", want, out)
		}
	}

	// The regression this reporter exists for: an agent that dies on a flag
	// agnt itself appended. The banner must quote the agent's own error,
	// attribute it to the agent, show the injected flag, and name the config
	// key that turns injection off.
	buf.Reset()
	reportUnexpectedShutdown(waitErrExit(t, 1), false, kimi,
		[]string{"error: unknown option '--agent-file'"}, nil)
	out = buf.String()
	for _, want := range []string{
		"kimi ended unexpectedly",
		"from kimi itself",
		"error: unknown option '--agent-file'",
		"agnt appended --agent-file /tmp/agnt-kimi-1/agent.yaml",
		"kimi --agent-file /tmp/agnt-kimi-1/agent.yaml",
		"adapters { kimi-cli { disabled true } }",
		"How to resolve:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("banner missing %q in:\n%s", want, out)
		}
	}

	// selflog must carry the decisive line — the banner sends the user to
	// `agnt hook log`, so a record without the cause is a dead end.
	logged, err := os.ReadFile(selflog.DefaultPath())
	if err != nil {
		t.Fatalf("read selflog: %v", err)
	}
	if !strings.Contains(string(logged), "error: unknown option '--agent-file'") {
		t.Fatalf("selflog lacks the agent's error:\n%s", logged)
	}
}

// TestFormatShutdownBanner_NoInjection checks the stdin-adapter shape: no
// argv/config section (agnt appended nothing, so it cannot be the cause) and
// no claim that the agent printed something when it printed nothing.
func TestFormatShutdownBanner_NoInjection(t *testing.T) {
	out := formatShutdownBanner("agent process exited with status 1",
		agentLaunch{Command: "opencode", Adapter: "opencode"}, nil, nil)
	if strings.Contains(out, "agnt appended") || strings.Contains(out, "disabled true") {
		t.Fatalf("injection guidance shown when nothing was injected:\n%s", out)
	}
	for _, want := range []string{"opencode ended unexpectedly", "printed nothing before exiting", "agnt hook log"} {
		if !strings.Contains(out, want) {
			t.Fatalf("banner missing %q in:\n%s", want, out)
		}
	}
}
