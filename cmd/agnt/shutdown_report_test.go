package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/standardbeagle/agnt/internal/selflog"
)

// waitErrExit runs a helper process that exits with status N and returns the
// Wait() error, giving a real *exec.ExitError to classify (rather than a
// hand-built fake). The helper is this test binary (helper_process_test.go),
// so it runs wherever the tests do.
func waitErrExit(t *testing.T, code int) error {
	t.Helper()
	return helperProcess(t, "exit:"+strconv.Itoa(code)).Run()
}

// TestClassifyChildExit covers clean and non-zero exits with and without a
// user interrupt. Signal deaths are in shutdown_report_unix_test.go: Windows
// has no signals, so a killed child there is just a non-zero exit status.
func TestClassifyChildExit(t *testing.T) {
	// Clean exit 0.
	if un, _, _ := classifyChildExit(waitErrExit(t, 0), false); un {
		t.Fatal("exit 0 should be clean")
	}

	// Non-zero exit → unexpected, reason names the status.
	un, kind, reason := classifyChildExit(waitErrExit(t, 2), false)
	if !un || kind != exitStatus || !strings.Contains(reason, "status 2") {
		t.Fatalf("exit 2: unexpected=%v reason=%q", un, reason)
	}

	// A shell-level SIGINT exit (130) while the user interrupted → clean.
	if un, _, _ := classifyChildExit(waitErrExit(t, 130), true); un {
		t.Fatal("exit 130 with user interrupt should be clean")
	}
	// Same code without a user interrupt → unexpected.
	if un, _, _ := classifyChildExit(waitErrExit(t, 130), false); !un {
		t.Fatal("exit 130 without user interrupt should be unexpected")
	}
}

// TestChildWaitError_ProcessStateOnlyExit is the regression test for the
// Windows `agnt run` path, whose ConPTY backend reports a finished child the way
// os.Process.Wait does: a nil error, the status only in ProcessState. A
// non-zero exit read as clean, so the unexpected-shutdown banner never showed.
// Process.Wait on a real helper reproduces that shape on every OS.
func TestChildWaitError_ProcessStateOnlyExit(t *testing.T) {
	waitState := func(code int) *os.ProcessState {
		t.Helper()
		c := helperProcess(t, "exit:"+strconv.Itoa(code))
		if err := c.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		ps, err := c.Process.Wait()
		if err != nil {
			t.Fatalf("wait helper: %v", err)
		}
		return ps
	}

	un, kind, reason := classifyChildExit(childWaitError(nil, waitState(3)), false)
	if !un || kind != exitStatus || !strings.Contains(reason, "status 3") {
		t.Fatalf("exit 3 via ProcessState: unexpected=%v kind=%v reason=%q", un, kind, reason)
	}

	if err := childWaitError(nil, waitState(0)); err != nil {
		t.Fatalf("exit 0 must stay clean, got %v", err)
	}
	if err := childWaitError(nil, nil); err != nil {
		t.Fatalf("no ProcessState must pass the nil through, got %v", err)
	}
	orig := os.ErrClosed
	if err := childWaitError(orig, waitState(4)); err != orig {
		t.Fatalf("a real Wait error must win, got %v", err)
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
	// Redirect selflog so we don't touch the real log. AGNT_ERROR_LOG is the
	// only override that works on every OS: os.UserCacheDir ignores HOME and
	// XDG_CACHE_HOME on Windows.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AGNT_ERROR_LOG", filepath.Join(t.TempDir(), "errors.log"))

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
	out := formatShutdownBanner("agent process exited with status 1", exitStatus,
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

// TestMeaningfulTail feeds the real frame noise a full-screen TUI agent (kimi)
// left in the tap when it was killed: box borders, a redrawn prompt row, and a
// status row repeated verbatim. Replaying that verbatim buried the one
// informative line and padded the banner with nothing.
func TestMeaningfulTail(t *testing.T) {
	raw := []string{
		"│ >",
		"╰──────────────────────────────────────",
		"auto  kimi-for-coding  …/ai-storyteller  main [+3 -3]",
		"● The dev server is warning about fs and path being imported in Edge Middleware.",
		"Let me check the import chain to understand the",
		"⠼ working... · Tip: ! to run a shell command",
		"╭──────────────────────────────────────",
		"│ >",
		"auto  kimi-for-coding  …/ai-storyteller  main [+3 -3]",
	}
	got := meaningfulTail(raw)
	want := []string{
		"● The dev server is warning about fs and path being imported in Edge Middleware.",
		"Let me check the import chain to understand the",
		"⠼ working... · Tip: ! to run a shell command",
		"auto  kimi-for-coding  …/ai-storyteller  main [+3 -3]",
	}
	assert.Equal(t, want, got, "box art dropped, repeated rows collapsed to their latest position")

	// A line with real words always survives, however it is decorated.
	assert.Equal(t, []string{"│ error: boom │"}, meaningfulTail([]string{"│ error: boom │", "╰───╯"}))
	assert.Empty(t, meaningfulTail([]string{"╭───╮", "│ > │", "⠼"}), "pure chrome leaves nothing")
}
