package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/standardbeagle/agnt/internal/selflog"
)

// resourceErrorPattern matches child-output lines that signal a host resource
// limit — the usual invisible cause of an abrupt agent shutdown (inotify
// exhaustion, fd exhaustion, OOM, thread/heap limits). When the agent dies we
// replay the most recent match so the user sees *why* instead of a bare shell.
var resourceErrorPattern = regexp.MustCompile(`(?i)(` + strings.Join([]string{
	`os file watch limit reached`,
	`inotify[^\n]*limit`,
	`too many open files`,
	`\bemfile\b|\benfile\b`,
	`no space left on device|\benospc\b`,
	`cannot allocate memory|\benomem\b`,
	`out of memory|\boom\b|oom-kill`,
	`resource temporarily unavailable`,
	`pthread_create failed|can't create thread|failed to create thread|thread limit`,
	`javascript heap out of memory|heap out of memory`,
}, "|") + `)`)

// childOutputTap keeps a rolling tail of the agent's own output so an abrupt
// exit can be explained in the agent's own words, plus the subset of those
// lines that match a host-resource limit for the dedicated resource callout.
//
// The tail is pattern-free on purpose: the cause of a fatal exit is whatever
// the agent printed on its way out (a rejected flag, a missing API key, a
// config parse error), and no pattern list can enumerate that. Matching only
// known patterns is what left users with a bare "exit status 1" and an empty
// log. Safe for concurrent Observe (from the output-copy goroutine) and
// Tail/Resource (at exit).
type childOutputTap struct {
	mu       sync.Mutex
	maxTail  int
	maxRes   int
	tail     []string
	resource []string
}

func newChildOutputTap(maxTail, maxResource int) *childOutputTap {
	if maxTail <= 0 {
		maxTail = 10
	}
	if maxResource <= 0 {
		maxResource = 5
	}
	return &childOutputTap{maxTail: maxTail, maxRes: maxResource}
}

// Observe records every non-empty line in the rolling tail, and additionally
// in the resource-error list when it matches. Lines arrive already
// ANSI-stripped from the activity monitor.
func (t *childOutputTap) Observe(line string) {
	if t == nil {
		return
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	t.mu.Lock()
	t.tail = appendCapped(t.tail, s, t.maxTail)
	if resourceErrorPattern.MatchString(s) {
		t.resource = appendCapped(t.resource, s, t.maxRes)
	}
	t.mu.Unlock()
}

// Tail returns a copy of the most recent output lines, oldest first.
func (t *childOutputTap) Tail() []string { return t.snapshot(func() []string { return t.tail }) }

// Resource returns a copy of the buffered resource-error lines.
func (t *childOutputTap) Resource() []string {
	return t.snapshot(func() []string { return t.resource })
}

func (t *childOutputTap) snapshot(pick func() []string) []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	src := pick()
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// appendCapped appends s to buf, dropping a consecutive duplicate and evicting
// the oldest entry once max is exceeded.
func appendCapped(buf []string, s string, max int) []string {
	if n := len(buf); n > 0 && buf[n-1] == s {
		return buf
	}
	buf = append(buf, s)
	if len(buf) > max {
		buf = buf[len(buf)-max:]
	}
	return buf
}

// classifyChildExit inspects the child's Wait() error and reports whether the
// exit was unexpected (warrants a persistent notice) plus a human reason.
// userInterrupted is true when agnt itself received SIGINT/SIGTERM (the user
// asked to quit) — that path is always treated as clean. Uses ProcessState
// text ("signal: killed", "exit status 1") rather than syscall.WaitStatus so
// it compiles unchanged on Windows.
func classifyChildExit(waitErr error, userInterrupted bool) (unexpected bool, reason string) {
	if waitErr == nil {
		return false, "" // exit 0
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		if userInterrupted {
			return false, ""
		}
		return true, fmt.Sprintf("agent process ended abnormally: %v", waitErr)
	}

	code := ee.ExitCode() // -1 when terminated by a signal (Unix)
	desc := ee.String()   // e.g. "signal: killed", "exit status 2"
	lower := strings.ToLower(desc)

	if strings.Contains(lower, "signal:") {
		if userInterrupted && (strings.Contains(lower, "interrupt") || strings.Contains(lower, "terminated") || strings.Contains(lower, "hangup")) {
			return false, ""
		}
		if strings.Contains(lower, "killed") {
			return true, "agent process was killed (SIGKILL) — likely out-of-memory or a host resource limit"
		}
		return true, fmt.Sprintf("agent process crashed (%s)", desc)
	}

	if code == 0 {
		return false, ""
	}
	// 130 = SIGINT-via-shell, 143 = SIGTERM-via-shell: clean when the user quit.
	if userInterrupted && (code == 130 || code == 143) {
		return false, ""
	}
	return true, fmt.Sprintf("agent process exited with status %d", code)
}

// shutdownWriter is the sink for the persistent notice. Overridable in tests.
var shutdownWriter io.Writer = os.Stdout

// reportUnexpectedShutdown surfaces an abrupt agent exit two ways: a persistent
// terminal banner (plain stdout, so it survives in scrollback after the overlay
// is torn down and the user returns to the tab) and a selflog record (persists
// across sessions, shown as the overlay ⚠ notice on the next run and via
// `agnt hook log`). No-op on a clean exit.
//
// The banner is written for the human who just watched their agent vanish: it
// attributes the failure to the agent rather than to agnt, quotes the agent's
// own last words, shows the argv agnt actually launched (including anything
// agnt appended, which is the one failure agnt itself can cause), and offers
// concrete next steps.
func reportUnexpectedShutdown(waitErr error, userInterrupted bool, launch agentLaunch, tail, resourceLines []string) {
	unexpected, reason := classifyChildExit(waitErr, userInterrupted)
	if !unexpected {
		return
	}
	if reason == "" {
		reason = "agent session ended unexpectedly"
	}

	recordShutdown(reason, tail, resourceLines)
	fmt.Fprint(shutdownWriter, formatShutdownBanner(reason, launch, tail, resourceLines))
}

// recordShutdown writes the one-line selflog entry. The agent's last output
// line is the decisive detail — without it `agnt hook log` cannot answer the
// question the banner sends the user there to ask.
func recordShutdown(reason string, tail, resourceLines []string) {
	parts := []string{reason}
	if len(tail) > 0 {
		parts = append(parts, "last output: "+tail[len(tail)-1])
	}
	if len(resourceLines) > 0 {
		parts = append(parts, "resource: "+strings.Join(resourceLines, " ; "))
	}
	selflog.Record("run", "%s", strings.Join(parts, " | "))
}

// formatShutdownBanner renders the persistent terminal notice. Lines are
// CRLF-terminated because the terminal is still in raw mode when it prints.
func formatShutdownBanner(reason string, launch agentLaunch, tail, resourceLines []string) string {
	agent := launch.Command
	if agent == "" {
		agent = "the agent"
	}

	var b strings.Builder
	b.WriteString("\r\n\x1b[1;33m⚠ " + agent + " ended unexpectedly — " + reason + "\x1b[0m\r\n")
	b.WriteString("  The error below is from " + agent + " itself; agnt only launched it.\r\n")

	if len(tail) > 0 {
		b.WriteString("\r\n  Last output from " + agent + ":\r\n")
		for _, l := range tail {
			b.WriteString("    " + l + "\r\n")
		}
	} else {
		b.WriteString("\r\n  " + agent + " printed nothing before exiting.\r\n")
	}

	if len(resourceLines) > 0 {
		b.WriteString("\r\n  Host resource limits hit:\r\n")
		for _, l := range resourceLines {
			b.WriteString("    • " + l + "\r\n")
		}
	}

	if len(launch.Injected) > 0 {
		b.WriteString("\r\n  agnt launched:\r\n")
		b.WriteString("    " + launch.CommandLine() + "\r\n")
		b.WriteString("  agnt appended " + strings.Join(launch.Injected, " ") + " to inject its system prompt.\r\n")
	}

	b.WriteString("\r\n  How to resolve:\r\n")
	for _, step := range resolutionSteps(launch) {
		b.WriteString("    • " + step + "\r\n")
	}
	return b.String()
}

// resolutionSteps lists what the user can actually do next, most-likely fix
// first. When agnt appended flags, that is the first thing to rule out — it is
// the only part of the launch the user did not write.
func resolutionSteps(launch agentLaunch) []string {
	var steps []string
	agent := launch.Command
	if agent == "" {
		agent = "the agent"
	}
	if len(launch.Injected) > 0 {
		steps = append(steps,
			"Run `"+agent+"` on its own. If it starts fine, an appended flag above is the cause.",
			"Stop agnt from appending them — add to .agnt.kdl:\r\n        ai { adapters { "+launch.Adapter+" { disabled true } } }",
			"Or point agnt at the right flag instead: ai { adapters { "+launch.Adapter+" { flag-name \"--your-flag\" } } }",
		)
	} else {
		steps = append(steps, "Run `"+agent+"` on its own to see whether it fails the same way outside agnt.")
	}
	return append(steps, "Full log: `agnt hook log`  ("+selflog.DefaultPath()+")")
}
