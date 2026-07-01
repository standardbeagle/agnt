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

// resourceErrorTap keeps the most recent resource-error output lines so the
// shutdown reporter can explain an abrupt exit after the fact. Safe for
// concurrent Observe (from the output-copy goroutine) and Recent (at exit).
type resourceErrorTap struct {
	mu   sync.Mutex
	max  int
	last []string
}

func newResourceErrorTap(max int) *resourceErrorTap {
	if max <= 0 {
		max = 5
	}
	return &resourceErrorTap{max: max}
}

// Observe records line if it looks like a resource error. Cheap no-op otherwise
// so it is safe to feed every output line through it.
func (t *resourceErrorTap) Observe(line string) {
	if t == nil {
		return
	}
	s := strings.TrimSpace(line)
	if s == "" || !resourceErrorPattern.MatchString(s) {
		return
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	t.mu.Lock()
	if n := len(t.last); n == 0 || t.last[n-1] != s { // drop consecutive dups
		t.last = append(t.last, s)
		if len(t.last) > t.max {
			t.last = t.last[len(t.last)-t.max:]
		}
	}
	t.mu.Unlock()
}

// Recent returns a copy of the buffered resource-error lines.
func (t *resourceErrorTap) Recent() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.last))
	copy(out, t.last)
	return out
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
// `agnt hook log`). No-op on a clean exit with no resource errors seen.
func reportUnexpectedShutdown(waitErr error, userInterrupted bool, resourceLines []string) {
	unexpected, reason := classifyChildExit(waitErr, userInterrupted)
	if !unexpected {
		return
	}
	if reason == "" {
		reason = "agent session ended unexpectedly"
	}

	if len(resourceLines) > 0 {
		selflog.Record("run", "%s | resource: %s", reason, strings.Join(resourceLines, " ; "))
	} else {
		selflog.Record("run", "%s", reason)
	}

	var b strings.Builder
	b.WriteString("\r\n\x1b[1;33m⚠ agnt session ended unexpectedly\x1b[0m\r\n")
	b.WriteString("  reason: " + reason + "\r\n")
	if len(resourceLines) > 0 {
		b.WriteString("  resource errors detected:\r\n")
		for _, l := range resourceLines {
			b.WriteString("    • " + l + "\r\n")
		}
	}
	b.WriteString("  details: run `agnt hook log`  (" + selflog.DefaultPath() + ")\r\n")
	fmt.Fprint(shutdownWriter, b.String())
}
