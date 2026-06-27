// Package selflog is agnt's always-on persistent log for its OWN
// fire-and-forget failures — the errors that, by design, must stay silent
// to their caller and so would otherwise vanish.
//
// Unlike internal/debug (which only writes when debug mode is enabled),
// selflog always writes, to a single rotating file at
// ${XDG_CACHE_HOME:-$HOME/.cache}/agnt/errors.log. The two writers that
// matter most run when the daemon is unreachable — the `agnt hook`
// dispatcher (a separate short-lived process) and the incident pinger — so
// the log is file-based with no daemon dependency: the viewer command
// (`agnt hook log`) and the overlay status notice both read it directly,
// and it works even with the daemon down.
//
// The package is a leaf (no agnt imports) so the latency-budgeted hook
// dispatcher and the import-cycle-sensitive overlay can both use it.
package selflog

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxFileSize caps the log before a single-generation rotation kicks in.
// errors.log is meant to hold recent self-failures, not a full history.
const maxFileSize = 1 << 20 // 1 MiB

// maxMessageLen bounds one record's message so a pathological error string
// cannot bloat the file or smear across the line-oriented format. Kept
// short enough that a typical record stays within the POSIX PIPE_BUF
// atomic-append guarantee against concurrent appenders on the same inode.
const maxMessageLen = 400

// Entry is one parsed self-error record.
type Entry struct {
	Time      time.Time
	Component string
	Message   string
}

// DefaultPath resolves the persistent error-log location. AGNT_ERROR_LOG
// wins (tests and power users redirect with one env var); otherwise it
// follows the XDG cache convention, matching internal/debug's log dir.
func DefaultPath() string {
	if p := os.Getenv("AGNT_ERROR_LOG"); p != "" {
		return p
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "agnt", "errors.log")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "agnt", "errors.log")
	}
	return ""
}

// Record appends one self-error to the default log. Component is a
// space-free token identifying the source (e.g. "hook", "pinger"). Write
// failures are deliberately swallowed — the caller is already on a failure
// path and a failing log write must never escalate.
func Record(component, format string, args ...any) {
	RecordTo(DefaultPath(), component, fmt.Sprintf(format, args...))
}

// RecordTo appends one record to an explicit path. Used by callers that
// inject the path (the hook dispatcher honours its --drop-log / test
// override). A trailing rotation keeps the file bounded.
func RecordTo(path, component, message string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	rotateIfLarge(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	// Newlines/whitespace in component or message would corrupt the
	// line-oriented format and let a multi-line error inject extra
	// records, so flatten and bound them.
	comp := sanitizeToken(component)
	if comp == "" {
		comp = "agnt"
	}
	msg := strings.ReplaceAll(message, "\n", " ")
	if len(msg) > maxMessageLen {
		msg = msg[:maxMessageLen] + "…"
	}
	// Format: <RFC3339> <component> <message> — mirrors the legacy
	// hook-drop line so existing tooling/expectations still parse.
	_, _ = fmt.Fprintf(f, "%s %s %s\n", time.Now().UTC().Format(time.RFC3339), comp, msg)
}

// Read returns the most recent up-to-limit entries from the log at path,
// oldest-first. A missing file is not an error (returns nil). limit <= 0
// returns all entries.
func Read(path string, limit int) ([]Entry, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if e, ok := parseLine(line); ok {
			entries = append(entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return entries, err
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// CountSince returns how many entries have a timestamp at or after t.
// Used by the overlay to decide whether to raise a status-bar notice.
// A missing file counts as zero.
func CountSince(path string, t time.Time) (int, error) {
	entries, err := Read(path, 0)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.Time.Before(t) {
			n++
		}
	}
	return n, nil
}

// Clear removes the log (and its rotated generation). A missing file is
// not an error.
func Clear(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(path + ".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// rotateIfLarge renames an over-cap log to path+".1" (replacing any prior
// generation) so the active file restarts empty. Best-effort: any failure
// leaves the existing file in place and the append proceeds.
func rotateIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxFileSize {
		return
	}
	_ = os.Rename(path, path+".1")
}

func parseLine(line string) (Entry, bool) {
	// <RFC3339> <component> <message...>
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		return Entry{}, false
	}
	ts, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return Entry{}, false
	}
	return Entry{Time: ts, Component: parts[1], Message: parts[2]}, true
}

func sanitizeToken(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "-")
}
