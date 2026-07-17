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
	"unicode"

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

// exitKind distinguishes deaths the agent chose from deaths done to it. The
// two need opposite explanations: a non-zero status is the agent's own verdict
// and its output usually says why, while a signal came from outside the agent
// entirely — blaming the agent for it sends the user hunting through their own
// code for a bug that is not there.
type exitKind int

const (
	// exitStatus — the agent ran, decided to stop, and set a non-zero code.
	exitStatus exitKind = iota
	// exitSignal — something outside the agent killed it.
	exitSignal
	// exitAbnormal — Wait() failed in a way that is neither of the above.
	exitAbnormal
)

// classifyChildExit inspects the child's Wait() error and reports whether the
// exit was unexpected (warrants a persistent notice), how it died, and a human
// reason. userInterrupted is true when agnt itself received SIGINT/SIGTERM (the
// user asked to quit) — that path is always treated as clean. Uses ProcessState
// text ("signal: killed", "exit status 1") rather than syscall.WaitStatus so
// it compiles unchanged on Windows.
func classifyChildExit(waitErr error, userInterrupted bool) (unexpected bool, kind exitKind, reason string) {
	if waitErr == nil {
		return false, exitStatus, "" // exit 0
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		if userInterrupted {
			return false, exitAbnormal, ""
		}
		return true, exitAbnormal, fmt.Sprintf("agent process ended abnormally: %v", waitErr)
	}

	code := ee.ExitCode() // -1 when terminated by a signal (Unix)
	desc := ee.String()   // e.g. "signal: killed", "exit status 2"
	lower := strings.ToLower(desc)

	if strings.Contains(lower, "signal:") {
		if userInterrupted && (strings.Contains(lower, "interrupt") || strings.Contains(lower, "terminated") || strings.Contains(lower, "hangup")) {
			return false, exitSignal, ""
		}
		if strings.Contains(lower, "killed") {
			return true, exitSignal, "agent process was killed (SIGKILL) — likely out-of-memory or a host resource limit"
		}
		return true, exitSignal, fmt.Sprintf("agent process was terminated (%s)", desc)
	}

	if code == 0 {
		return false, exitStatus, ""
	}
	// 130 = SIGINT-via-shell, 143 = SIGTERM-via-shell: clean when the user quit.
	if userInterrupted && (code == 130 || code == 143) {
		return false, exitStatus, ""
	}
	return true, exitStatus, fmt.Sprintf("agent process exited with status %d", code)
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
	unexpected, kind, reason := classifyChildExit(waitErr, userInterrupted)
	if !unexpected {
		return
	}
	if reason == "" {
		reason = "agent session ended unexpectedly"
	}

	tail = meaningfulTail(tail)
	recordShutdown(reason, tail, resourceLines)
	fmt.Fprint(shutdownWriter, formatShutdownBanner(reason, kind, launch, tail, resourceLines))
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

// meaningfulTail strips what a full-screen TUI agent leaves in the tap: box
// borders, and the same status/prompt rows redrawn over and over. Replaying
// those verbatim buries any real line in frame noise and pads the banner with
// text that tells the reader nothing.
//
// The filter is deliberately dumb — drop rows with nothing but punctuation and
// box-drawing, collapse repeats — because anything cleverer starts guessing at
// which lines "look important", which is the pattern-matching mistake the tap
// exists to avoid. A line with real words always survives.
func meaningfulTail(tail []string) []string {
	out := make([]string, 0, len(tail))
	seen := make(map[string]int, len(tail))
	for _, l := range tail {
		if !hasWordContent(l) {
			continue // box border, rule, bare prompt glyph
		}
		// A redrawn frame repeats rows verbatim; keep the latest position only.
		if i, dup := seen[l]; dup {
			out = append(out[:i], out[i+1:]...)
			for k, v := range seen {
				if v > i {
					seen[k] = v - 1
				}
			}
		}
		seen[l] = len(out)
		out = append(out, l)
	}
	return out
}

// hasWordContent reports whether a line carries at least one letter or digit.
// Box art, rules, and spinner glyphs do not.
func hasWordContent(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// formatShutdownBanner renders the persistent terminal notice. Lines are
// CRLF-terminated because the terminal is still in raw mode when it prints.
func formatShutdownBanner(reason string, kind exitKind, launch agentLaunch, tail, resourceLines []string) string {
	agent := launch.Command
	if agent == "" {
		agent = "the agent"
	}

	var b strings.Builder
	b.WriteString("\r\n\x1b[1;33m⚠ " + agent + " ended unexpectedly — " + reason + "\x1b[0m\r\n")
	b.WriteString("  " + attribution(agent, kind) + "\r\n")

	if len(tail) > 0 {
		b.WriteString("\r\n  Last output from " + agent + " (may be a redraw, not an error):\r\n")
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

	if kind == exitStatus && len(launch.Injected) > 0 {
		b.WriteString("\r\n  agnt launched:\r\n")
		b.WriteString("    " + launch.CommandLine() + "\r\n")
		b.WriteString("  agnt appended " + strings.Join(launch.Injected, " ") + " to inject its system prompt.\r\n")
	}

	b.WriteString("\r\n  How to resolve:\r\n")
	for _, step := range resolutionSteps(kind, launch) {
		b.WriteString("    • " + step + "\r\n")
	}
	return b.String()
}

// attribution says who is responsible for the death, because the honest answer
// differs by kind and the wrong one wastes the reader's time. A signal means
// some other process killed the agent; saying "the error below is from the
// agent" there is simply false — there may be no error at all.
func attribution(agent string, kind exitKind) string {
	switch kind {
	case exitSignal:
		return "A signal killed " + agent + " from the outside — this is not an error " + agent + " reported."
	case exitAbnormal:
		return "agnt could not determine how " + agent + " ended."
	default:
		return "The output below is from " + agent + " itself; agnt only launched it."
	}
}

// resolutionSteps lists what the user can actually do next, most-likely cause
// first. The list differs by kind: for a signal the question is who sent it, and
// nothing in the agent's own output will answer that.
func resolutionSteps(kind exitKind, launch agentLaunch) []string {
	agent := launch.Command
	if agent == "" {
		agent = "the agent"
	}
	logStep := "Full log: `agnt hook log`  (" + selflog.DefaultPath() + ")"

	if kind == exitSignal {
		return []string{
			"Check `agnt hook log` for an agnt session-cleanup record at the same time — agnt reaps a session's whole process group when the daemon decides the session ended.",
			"Rule out the host: an OOM kill shows up in `dmesg`, and closing the terminal signals the whole group.",
			logStep,
		}
	}

	var steps []string
	if len(launch.Injected) > 0 {
		steps = append(steps,
			"Run `"+agent+"` on its own. If it starts fine, an appended flag above is the cause.",
			"Stop agnt from appending them — add to .agnt.kdl:\r\n        ai { adapters { "+launch.Adapter+" { disabled true } } }",
			"Or point agnt at the right flag instead: ai { adapters { "+launch.Adapter+" { flag-name \"--your-flag\" } } }",
		)
	} else {
		steps = append(steps, "Run `"+agent+"` on its own to see whether it fails the same way outside agnt.")
	}
	return append(steps, logStep)
}
