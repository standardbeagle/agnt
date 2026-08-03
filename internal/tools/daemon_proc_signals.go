package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SignalSet is the structured signal payload returned by `proc output`
// when the caller passes `extract: [...]`. Each field is independent —
// a caller asking only for "url" gets URLs populated and everything else
// at zero-value. The wait action consumes the same struct to decide
// whether a signal has appeared yet.
//
// ReadyLine is the line that triggered Ready=true (so the wait action can
// surface "what matched" in its result without re-scanning).
//
// BuildErrors holds *structured* parses of the same lines that populate
// Errors. The two fields coexist by design: Errors is the raw line-level
// fallback (no regression when a tool's output doesn't match any
// parser), BuildErrors is the token-efficient structured form the agent
// reads first. See parseBuildErrors() in build_error_parsers.go.
type SignalSet struct {
	URLs        []string     `json:"urls,omitempty"`
	Errors      []string     `json:"errors,omitempty"`
	BuildErrors []BuildError `json:"build_errors,omitempty"`
	Warnings    []string     `json:"warnings,omitempty"`
	Ports       []int        `json:"ports,omitempty"`
	Ready       bool         `json:"ready,omitempty"`
	ReadyLine   string       `json:"ready_line,omitempty"`
}

// Pre-compiled regexes for signal extraction. Kept package-level so the
// wait-action poll loop doesn't recompile on every tick.
//
// Coordination note: AlertScanner (internal/overlay/alerts_defaults.go)
// owns the *error/warning classification* regex bank. We do NOT import it
// here for two reasons:
//  1. It runs server-side over PTY output for toast surfacing — different
//     buffer, different lifecycle.
//  2. Its patterns are framework-specific (dotnet ENC errors, npm ERR!,
//     rust E\d+) — too narrow for a generic "is there an ERROR line?" pull.
//
// Our patterns deliberately overlap with AlertScanner on the broad
// strokes (ERROR, panic, traceback, WARN) but stay simpler so the
// extract-action behaviour is predictable across languages. If a caller
// needs framework-specific structured errors they should use `proc snapshot`
// (which feeds through the unified-error pipeline → AlertScanner classifications).
var (
	urlRegex = regexp.MustCompile(`https?://[^\s,;'"<>)]+`)
	// readyRegex matches the canonical ready phrases. (?i) for case
	// insensitivity. Word boundaries on "ready"/"started" prevent false
	// matches on "already" / "starteddevsvc". "listening on" / "compiled
	// successfully" are phrase matches.
	readyRegex = regexp.MustCompile(`(?i)(\bready\b|listening (?:on|at)|\bstarted\b|compiled successfully|server running)`)
	// errorRegex catches: "ERROR" prefix or anywhere word, panic:, fatal
	// error:, "error TS\d+", traceback header. Compile-error lines tend
	// to start with file:line:col which we cover via the "error " word.
	errorRegex = regexp.MustCompile(`(?i)(\bERROR\b|^panic:|^fatal error:|error TS\d+|error\[E\d+\]|Traceback \(most recent call last\)|Exception in thread)`)
	// warningRegex matches "WARN" / "warning:" — kept narrow so info-level
	// "WARNING:" headers in compiler output get caught but normal text
	// containing "warn" (e.g. "warned the user") doesn't.
	warningRegex = regexp.MustCompile(`(?i)(\bWARN\b|\bwarning:)`)
	// portRegex matches "port 3000" (decimal only) and ":3000" / "::3000"
	// patterns. Port range is 1-65535 but the regex is permissive on
	// digits; we range-check after parsing.
	portRegex = regexp.MustCompile(`(?i)(?:\bport\s+|::?)(\d{2,5})\b`)
)

// extractSignals scans output lines for the requested signals and returns
// a populated SignalSet. Unknown signal names in `wanted` are silently
// ignored — extract is a lenient hint, not a contract. Callers asking
// for an empty/nil wanted list get an empty SignalSet (cheap zero-value).
func extractSignals(lines []string, wanted []string) SignalSet {
	if len(wanted) == 0 {
		return SignalSet{}
	}
	want := make(map[string]bool, len(wanted))
	for _, w := range wanted {
		want[strings.ToLower(strings.TrimSpace(w))] = true
	}

	var out SignalSet
	seenPort := make(map[int]bool)
	seenURL := make(map[string]bool)

	for _, line := range lines {
		if line == "" {
			continue
		}
		if want["url"] {
			for _, m := range urlRegex.FindAllString(line, -1) {
				// Strip common trailing punctuation that the URL regex
				// will pick up despite the negated character class (e.g.
				// trailing "." at end of sentence).
				m = strings.TrimRight(m, ".")
				if !seenURL[m] {
					seenURL[m] = true
					out.URLs = append(out.URLs, m)
				}
			}
		}
		if want["ready"] && !out.Ready {
			if readyRegex.MatchString(line) {
				out.Ready = true
				out.ReadyLine = line
			}
		}
		if want["error"] {
			if errorRegex.MatchString(line) {
				out.Errors = append(out.Errors, line)
			}
		}
		if want["warning"] {
			if warningRegex.MatchString(line) {
				out.Warnings = append(out.Warnings, line)
			}
		}
		if want["port"] {
			for _, m := range portRegex.FindAllStringSubmatch(line, -1) {
				if len(m) < 2 {
					continue
				}
				p, err := strconv.Atoi(m[1])
				if err != nil || p < 1 || p > 65535 {
					continue
				}
				if !seenPort[p] {
					seenPort[p] = true
					out.Ports = append(out.Ports, p)
				}
			}
		}
	}

	// Structured-error pass: when the caller asked for "error" or
	// "warning", run the multi-line parser bank against the full line
	// slice (not per-line — rust/vite/jest/webpack/gotest all need
	// adjacent-line lookahead). Unknown formats fall through silently;
	// the raw Errors/Warnings slices populated above are the no-regression
	// fallback path. Severity filter respects which signals were requested.
	if want["error"] || want["warning"] {
		for _, be := range parseBuildErrors(lines) {
			switch be.Severity {
			case "warning":
				if want["warning"] {
					out.BuildErrors = append(out.BuildErrors, be)
				}
			default: // "error" or empty (parsers default to error)
				if want["error"] {
					out.BuildErrors = append(out.BuildErrors, be)
				}
			}
		}
	}
	return out
}

// signalMatchesAny is the wait-action core: given a SignalSet and the
// list of signal names the caller wants to wait on, return the first
// matching signal name and the line that triggered it. Empty match name
// = no signal hit yet. The order of `wanted` matters — earlier names win
// on ties (so callers can prioritise "ready" over "error" or vice versa).
func signalMatchesAny(s SignalSet, wanted []string) (match, line string) {
	for _, w := range wanted {
		switch strings.ToLower(strings.TrimSpace(w)) {
		case "ready":
			if s.Ready {
				return "ready", s.ReadyLine
			}
		case "error":
			if len(s.Errors) > 0 {
				return "error", s.Errors[0]
			}
		case "warning":
			if len(s.Warnings) > 0 {
				return "warning", s.Warnings[0]
			}
		case "url":
			if len(s.URLs) > 0 {
				return "url", s.URLs[0]
			}
		case "port":
			if len(s.Ports) > 0 {
				return "port", fmt.Sprintf("%d", s.Ports[0])
			}
		}
	}
	return "", ""
}

// processStream is one process's slice of output for the multi-stream
// `proc output` action. Lines is the post-filter list (after grep/tail
// have been applied per-process) so the multi-stream formatter doesn't
// have to re-run filters.
//
// Lowercase type name keeps it package-internal but the JSON tags are
// stable wire format — the field appears in ProcOutput.MultiStream which
// is the agent-visible payload.
type processStream struct {
	ProcessID string   `json:"process_id"`
	Lines     []string `json:"lines,omitempty"`
	// Signals holds extract output for this process when `extract` was
	// requested. nil otherwise.
	Signals *SignalSet `json:"signals,omitempty"`
	// Err captures a per-process fetch failure so partial multi-stream
	// success surfaces correctly (one bad ID doesn't fail the whole call).
	Err string `json:"error,omitempty"`
}

// formatMultiStreamCompact renders interleaved per-process output with
// a "[process_id]" prefix on each line. This is the agent-friendly
// default — one block of text the agent can scan visually.
//
// When any stream has structured BuildErrors (extract was requested and
// at least one parser hit), a `=== Build Errors (N) ===` summary block
// is appended after the raw lines. This is the token-efficient surface
// the agent reads first; the raw lines remain available for context.
func formatMultiStreamCompact(streams []processStream) string {
	var b strings.Builder
	for _, s := range streams {
		if s.Err != "" {
			fmt.Fprintf(&b, "[%s] (error: %s)\n", s.ProcessID, s.Err)
			continue
		}
		for _, line := range s.Lines {
			if line == "" {
				continue
			}
			fmt.Fprintf(&b, "[%s] %s\n", s.ProcessID, line)
		}
	}
	appendBuildErrorsSection(&b, streams)
	return b.String()
}

// appendSingleStreamBuildErrors is the single-process counterpart to
// appendBuildErrorsSection. It returns the supplied raw output text with
// a trailing "=== Build Errors (N) ===" block when at least one parser
// hit. Callers pass the un-prefixed output (no [process_id] tags) and
// the BuildErrors slice from extractSignals; the helper handles empty
// inputs by returning out unchanged.
func appendSingleStreamBuildErrors(out string, errs []BuildError) string {
	if len(errs) == 0 {
		return out
	}
	var b strings.Builder
	b.WriteString(out)
	if !strings.HasSuffix(out, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n=== Build Errors (%d) ===\n", len(errs))
	for _, be := range errs {
		b.WriteString(formatBuildErrorCompact(be))
		b.WriteByte('\n')
	}
	return b.String()
}

// appendBuildErrorsSection writes a "=== Build Errors (N) ===" block to
// b for every BuildError found across the supplied streams. The block
// is suppressed when no stream has structured errors so empty extract
// calls keep their existing single-block output.
//
// Per-stream attribution uses the same `[process_id]` prefix convention
// as the raw line dump above. Errors are rendered via formatBuildErrorCompact
// to enforce the ~120-char line budget.
func appendBuildErrorsSection(b *strings.Builder, streams []processStream) {
	total := 0
	for _, s := range streams {
		if s.Signals != nil {
			total += len(s.Signals.BuildErrors)
		}
	}
	if total == 0 {
		return
	}
	fmt.Fprintf(b, "\n=== Build Errors (%d) ===\n", total)
	for _, s := range streams {
		if s.Signals == nil {
			continue
		}
		for _, be := range s.Signals.BuildErrors {
			fmt.Fprintf(b, "[%s] %s\n", s.ProcessID, formatBuildErrorCompact(be))
		}
	}
}

// assembleMultiStreamOutput builds the ProcOutput payload for the
// multi-stream `proc output` action. Pure function — takes already-fetched
// per-process streams (the IPC layer is the caller's job) plus the extract
// list and raw flag, returns a fully-populated ProcOutput.
//
// When extract is non-empty each stream's Signals field is populated
// (including for streams with empty/no matching content — Signals is
// non-nil but its slices are empty). This lets the agent distinguish
// "extract was requested, found nothing" from "extract was not requested
// at all" (Signals == nil).
func assembleMultiStreamOutput(streams []processStream, extract []string, raw bool) ProcOutput {
	// Run signal extraction per stream so each process gets its own
	// signal block (different processes hit different signals — agent
	// usually wants build's "error" and server's "ready" both).
	if len(extract) > 0 {
		for i := range streams {
			if streams[i].Err != "" {
				continue
			}
			s := extractSignals(streams[i].Lines, extract)
			streams[i].Signals = &s
		}
	}

	out := ProcOutput{
		MultiStream: streams,
	}
	// Count non-empty lines across all streams.
	for _, s := range streams {
		for _, line := range s.Lines {
			if line != "" {
				out.Lines++
			}
		}
	}
	if raw {
		out.Output = formatMultiStreamNDJSON(streams)
	} else {
		out.Output = formatMultiStreamCompact(streams)
	}
	return out
}

// WaitResult is the payload returned by the `proc wait` action. Mirrors
// the shape from the task spec exactly so the wire format is testable.
//
// TimedOut is true when the wait budget elapsed without a signal match;
// when true the other fields are zero-valued. Per the spec, timeout is
// not an error — the agent decides what to do (give up, increase the
// timeout, switch tactics).
type WaitResult struct {
	Signal      string `json:"signal,omitempty"`
	MatchedLine string `json:"matched_line,omitempty"`
	ElapsedMs   int64  `json:"elapsed_ms"`
	TimedOut    bool   `json:"timeout,omitempty"`
}

// waitForSignal polls fetchLines until any of `wanted` signals appears in
// the extracted SignalSet, or the timeout elapses. Pure-ish (does sleep
// in real time but uses the supplied fetch closure for the IPC).
//
// timeoutMs / pollIntervalMs are millisecond budgets. A pollIntervalMs
// of 0 falls back to 200ms (the conservative default that keeps the
// daemon load bounded for short waits).
func waitForSignal(fetchLines func() ([]string, error), wanted []string, timeoutMs, pollIntervalMs int) WaitResult {
	if pollIntervalMs <= 0 {
		pollIntervalMs = 200
	}
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	start := time.Now()
	pollInterval := time.Duration(pollIntervalMs) * time.Millisecond

	for {
		lines, err := fetchLines()
		if err == nil {
			signals := extractSignals(lines, wantedToExtractList(wanted))
			match, line := signalMatchesAny(signals, wanted)
			if match != "" {
				return WaitResult{
					Signal:      match,
					MatchedLine: line,
					ElapsedMs:   time.Since(start).Milliseconds(),
				}
			}
		}
		// Timeout check after each fetch — even one fetch counts as work
		// and we should return the elapsed budget.
		if time.Now().After(deadline) {
			return WaitResult{
				ElapsedMs: time.Since(start).Milliseconds(),
				TimedOut:  true,
			}
		}
		// Short-sleep until next poll, but never sleep past the deadline.
		remaining := time.Until(deadline)
		sleep := pollInterval
		if sleep > remaining {
			sleep = remaining
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}
}

// wantedToExtractList maps wait-signal names to the broader extract names.
// They're already 1:1 today (ready→ready, error→error, …) but keeping the
// indirection makes it cheap to add aliases later (e.g. "url" wait could
// imply both "url" and "ready" extracts).
func wantedToExtractList(wanted []string) []string {
	out := make([]string, 0, len(wanted))
	seen := make(map[string]bool, len(wanted))
	for _, w := range wanted {
		key := strings.ToLower(strings.TrimSpace(w))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// formatMultiStreamNDJSON renders one JSON object per line. Used when
// the caller passes raw=true and wants a machine-parseable stream that
// preserves per-line process attribution. Each object has shape:
//
//	{"process_id":"build","line":"compiling..."}
//
// Errors per process are emitted as a single object with "error" key.
func formatMultiStreamNDJSON(streams []processStream) string {
	var b strings.Builder
	for _, s := range streams {
		if s.Err != "" {
			payload, _ := json.Marshal(map[string]string{"process_id": s.ProcessID, "error": s.Err})
			b.Write(payload)
			b.WriteByte('\n')
			continue
		}
		for _, line := range s.Lines {
			if line == "" {
				continue
			}
			payload, _ := json.Marshal(map[string]string{"process_id": s.ProcessID, "line": line})
			b.Write(payload)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
