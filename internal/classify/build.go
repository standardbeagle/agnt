package classify

import (
	"regexp"
	"strconv"
	"strings"
)

// BuildError is a structured error parsed from a build tool's output. The
// fields are deliberately optional — different tools surface different
// metadata (webpack often omits line/col, pytest omits col entirely) — so
// the JSON output uses `omitempty` aggressively. Tool is always set when
// the parser fires; callers can use it as a "did anything match?" check.
type BuildError struct {
	Tool     string `json:"tool"`               // "tsc","eslint","vite","webpack","go","rust","pytest","jest","gotest"
	Severity string `json:"severity,omitempty"` // "error" (default) or "warning"; eslint surfaces both
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	Code     string `json:"code,omitempty"`    // e.g. "TS2322", "E0308"
	Rule     string `json:"rule,omitempty"`    // e.g. "no-console" (eslint only)
	Test     string `json:"test,omitempty"`    // pytest/jest/gotest test identifier
	Message  string `json:"message,omitempty"` // diagnostic text minus location
	RawLine  string `json:"raw_line,omitempty"`
}

// Pre-compiled regex bank. All patterns are package-level so the
// per-line scan in ParseBuildErrors() doesn't recompile on every call.
//
// Naming convention: <tool>_<form>Re. Some tools have multiple forms
// (tsc has --pretty and default; go has ./-prefixed and bare).
var (
	// tsc default: src/foo.ts(12,3): error TS2322: Type 'x' ...
	tscParenRe = regexp.MustCompile(`^([^\s()]+)\((\d+),(\d+)\):\s+(error|warning)\s+(TS\d+):\s+(.+)$`)
	// tsc --pretty: src/foo.ts:12:3 - error TS2322: Type 'x' ...
	tscColonRe = regexp.MustCompile(`^([^\s:]+):(\d+):(\d+)\s+-\s+(error|warning)\s+(TS\d+):\s+(.+)$`)

	// eslint stylish: file header on its own line (absolute path or relative
	// starting with / or ./ or alphabetic). Followed by indented rows:
	//   12:3  error  Unexpected console statement  no-console
	eslintFileHeaderRe = regexp.MustCompile(`^(/[^\s]+|\.\.?/[^\s]+|[A-Za-z][^\s:]*\.(ts|tsx|js|jsx|mjs|cjs|vue|svelte))$`)
	eslintRowRe        = regexp.MustCompile(`^\s+(\d+):(\d+)\s+(error|warning)\s+(.+?)\s\s+([A-Za-z0-9@/_-]+)\s*$`)

	// vite/esbuild: header `✗ [ERROR] msg` or `✘ [ERROR] msg`, followed
	// by indented `    file:line:col:` on the next line.
	viteHeaderRe   = regexp.MustCompile(`^[✗✘]\s+\[(ERROR|WARNING)\]\s+(.+)$`)
	viteLocationRe = regexp.MustCompile(`^\s+([^\s:]+):(\d+):(\d+):?\s*$`)

	// webpack: `ERROR in ./src/foo.ts` then `Module build failed: ...`
	// Also matches WARNING in ... for warning surfacing.
	webpackHeaderRe = regexp.MustCompile(`^(ERROR|WARNING)\s+in\s+(\S+)`)

	// Go compiler: ./foo.go:12:3: undefined: Bar
	// Path may have leading ./ or be relative; must end in .go before the colon.
	goCompileRe = regexp.MustCompile(`^(\.{1,2}/)?([^\s:]+\.go):(\d+):(\d+):\s+(.+)$`)

	// Rust/cargo: "error[E0308]: mismatched types" then "  --> src/main.rs:12:3"
	rustHeaderRe   = regexp.MustCompile(`^(error|warning)\[([A-Z]\d+)\]:\s+(.+)$`)
	rustLocationRe = regexp.MustCompile(`^\s*-->\s+([^\s:]+):(\d+):(\d+)\s*$`)

	// pytest: "FAILED tests/test_foo.py::test_bar - AssertionError: ..."
	pytestRe = regexp.MustCompile(`^FAILED\s+([^\s:]+)::([^\s]+)(?:\s+-\s+(.+))?$`)

	// jest/vitest: "  ● TestName › subtest" then "    at … (file:line:col)"
	jestHeaderRe   = regexp.MustCompile(`^\s*●\s+(.+)$`)
	jestLocationRe = regexp.MustCompile(`\s+at\s+[^\s]+\s+\(([^():]+):(\d+):(\d+)\)`)

	// go test: "--- FAIL: TestFoo (0.01s)" then "    foo_test.go:12: msg"
	goTestHeaderRe   = regexp.MustCompile(`^---\s+FAIL:\s+([^\s]+)`)
	goTestLocationRe = regexp.MustCompile(`^\s+([^\s:]+\.go):(\d+):\s+(.+)$`)
)

// ParseBuildErrors scans a slice of output lines and returns the
// structured errors recognised by any of the parsers in the bank.
// Multi-line formats (rust header → location, vite header → location,
// jest header → at-frame, go test header → location, webpack header →
// build-failed line, eslint file-header → indented row) consume two
// adjacent lines and emit one BuildError; the parser advances past the
// consumed location line.
//
// Unknown formats are silently dropped — the caller's line-level
// classification surfaces them via ClassifyLine / the catch-all.
//
// The parsers are mutually exclusive on the *header* line: a tsc-style
// error TS#### line cannot also match the rust header (different prefix);
// a Go compile error cannot also match a go test location (which is
// indented and emitted only after a "--- FAIL:" header). Order of
// attempts matters only when two formats could plausibly fire on the
// same line; today none do.
func ParseBuildErrors(lines []string) []BuildError {
	var out []BuildError
	var eslintFile string

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			eslintFile = "" // blank line ends an eslint group
			continue
		}

		// tsc — paren form (default formatter).
		if m := tscParenRe.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			out = append(out, BuildError{
				Tool: "tsc", Severity: m[4], File: m[1],
				Line: ln, Col: col, Code: m[5],
				Message: m[6], RawLine: line,
			})
			continue
		}
		// tsc — colon form (--pretty).
		if m := tscColonRe.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			out = append(out, BuildError{
				Tool: "tsc", Severity: m[4], File: m[1],
				Line: ln, Col: col, Code: m[5],
				Message: m[6], RawLine: line,
			})
			continue
		}

		// Webpack header: "ERROR in ./src/foo.ts" — capture file, then
		// fold the next non-empty line as the message body.
		if m := webpackHeaderRe.FindStringSubmatch(line); m != nil {
			be := BuildError{
				Tool: "webpack", Severity: strings.ToLower(m[1]),
				File: m[2], RawLine: line,
			}
			// Fold the subsequent message line if there is one.
			if i+1 < len(lines) && lines[i+1] != "" {
				be.Message = strings.TrimSpace(lines[i+1])
				be.RawLine = line + "\n" + lines[i+1]
				i++
			}
			out = append(out, be)
			continue
		}

		// Go compile — must precede generic eslint header check (a path
		// like "foo.go" without colon would falsely match the eslint file
		// header regex).
		if m := goCompileRe.FindStringSubmatch(line); m != nil {
			ln, _ := strconv.Atoi(m[3])
			col, _ := strconv.Atoi(m[4])
			file := m[2]
			if m[1] != "" {
				file = m[1] + m[2]
			}
			out = append(out, BuildError{
				Tool: "go", Severity: "error", File: file,
				Line: ln, Col: col, Message: m[5], RawLine: line,
			})
			continue
		}

		// Rust header → location pair.
		if m := rustHeaderRe.FindStringSubmatch(line); m != nil {
			be := BuildError{
				Tool: "rust", Severity: m[1], Code: m[2],
				Message: m[3], RawLine: line,
			}
			// Look ahead for the --> location line.
			if i+1 < len(lines) {
				if loc := rustLocationRe.FindStringSubmatch(lines[i+1]); loc != nil {
					ln, _ := strconv.Atoi(loc[2])
					col, _ := strconv.Atoi(loc[3])
					be.File = loc[1]
					be.Line = ln
					be.Col = col
					be.RawLine = line + "\n" + lines[i+1]
					i++
				}
			}
			out = append(out, be)
			continue
		}

		// Vite/esbuild header → location pair.
		if m := viteHeaderRe.FindStringSubmatch(line); m != nil {
			be := BuildError{
				Tool: "vite", Severity: strings.ToLower(m[1]),
				Message: m[2], RawLine: line,
			}
			// Look ahead for the indented `    file:line:col:` line.
			if i+1 < len(lines) {
				if loc := viteLocationRe.FindStringSubmatch(lines[i+1]); loc != nil {
					ln, _ := strconv.Atoi(loc[2])
					col, _ := strconv.Atoi(loc[3])
					be.File = loc[1]
					be.Line = ln
					be.Col = col
					be.RawLine = line + "\n" + lines[i+1]
					i++
				}
			}
			out = append(out, be)
			continue
		}

		// pytest summary line.
		if m := pytestRe.FindStringSubmatch(line); m != nil {
			be := BuildError{
				Tool: "pytest", Severity: "error",
				File: m[1], Test: m[2], RawLine: line,
			}
			if len(m) > 3 {
				be.Message = m[3]
			}
			out = append(out, be)
			continue
		}

		// jest/vitest header (●) → at-frame pair. The header carries the
		// test name; the at-frame carries file:line:col.
		if m := jestHeaderRe.FindStringSubmatch(line); m != nil {
			be := BuildError{
				Tool: "jest", Severity: "error",
				Test: strings.TrimSpace(m[1]), RawLine: line,
			}
			// Look ahead a few lines for the first at-frame; jest emits
			// a couple of trace-context lines between header and frame
			// in some configs.
			for look := 1; look <= 5 && i+look < len(lines); look++ {
				if loc := jestLocationRe.FindStringSubmatch(lines[i+look]); loc != nil {
					ln, _ := strconv.Atoi(loc[2])
					col, _ := strconv.Atoi(loc[3])
					be.File = loc[1]
					be.Line = ln
					be.Col = col
					be.RawLine = line + "\n" + lines[i+look]
					i += look
					break
				}
			}
			// If we didn't find an at-frame, still emit the header — the
			// agent at least gets the test name and the raw line.
			out = append(out, be)
			continue
		}

		// go test header → location pair.
		if m := goTestHeaderRe.FindStringSubmatch(line); m != nil {
			be := BuildError{
				Tool: "gotest", Severity: "error",
				Test: m[1], RawLine: line,
			}
			if i+1 < len(lines) {
				if loc := goTestLocationRe.FindStringSubmatch(lines[i+1]); loc != nil {
					ln, _ := strconv.Atoi(loc[2])
					be.File = loc[1]
					be.Line = ln
					be.Message = loc[3]
					be.RawLine = line + "\n" + lines[i+1]
					i++
				}
			}
			out = append(out, be)
			continue
		}

		// eslint — file-header on its own line, sets the active file
		// for subsequent indented row matches. Header check comes after
		// goCompile so a "foo.go" file header doesn't shadow a real
		// Go error (the goCompile regex requires a `:line:col:` suffix
		// which the header lacks, so they never collide on the same
		// line — but the order keeps reasoning simple).
		if eslintFileHeaderRe.MatchString(line) {
			eslintFile = line
			continue
		}
		if eslintFile != "" {
			if m := eslintRowRe.FindStringSubmatch(line); m != nil {
				ln, _ := strconv.Atoi(m[1])
				col, _ := strconv.Atoi(m[2])
				out = append(out, BuildError{
					Tool: "eslint", Severity: m[3],
					File: eslintFile, Line: ln, Col: col,
					Message: m[4], Rule: m[5], RawLine: line,
				})
				continue
			}
		}
	}
	return out
}
