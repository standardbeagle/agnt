package tools

import (
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/classify"
)

// BuildError aliases the classify type. The parser bank (tsc / eslint / vite /
// webpack / go / rust / pytest / jest / gotest) lives in internal/classify —
// the single source of truth shared with the overlay AlertScanner. Only the
// agent-facing compact renderer (formatBuildErrorCompact) is tools-specific and
// stays here.
type BuildError = classify.BuildError

// parseBuildErrors scans output lines for structured build errors. Thin wrapper
// over classify.ParseBuildErrors so existing call sites are unchanged.
func parseBuildErrors(lines []string) []BuildError {
	return classify.ParseBuildErrors(lines)
}

// formatBuildErrorCompact renders a single BuildError as the agent-facing
// one-line summary. Acceptance contract: ~120 chars for typical errors,
// hard ceiling enforced by tests. Fields that are zero-valued are
// omitted (no "::0:0", no "TS:"). Format:
//
//	[<tool>:<severity>] <file>[:<line>[:<col>]][ — <code>: ]<message>
//
// Examples:
//
//	[tsc:error] src/components/Foo.tsx:42:7 — TS2345: Argument of type ...
//	[webpack:error] ./src/foo.ts — Module build failed: SyntaxError: ...
//	[gotest:error] foo_test.go:12 (TestFoo) — assertion failed: ...
func formatBuildErrorCompact(be BuildError) string {
	var b strings.Builder
	sev := be.Severity
	if sev == "" {
		sev = "error"
	}
	fmt.Fprintf(&b, "[%s:%s] ", be.Tool, sev)

	if be.File != "" {
		b.WriteString(be.File)
		if be.Line > 0 {
			fmt.Fprintf(&b, ":%d", be.Line)
			if be.Col > 0 {
				fmt.Fprintf(&b, ":%d", be.Col)
			}
		}
	}

	if be.Test != "" {
		fmt.Fprintf(&b, " (%s)", be.Test)
	}

	// Em dash separator before message; only when we have a file/test
	// preface to keep the line readable when the file is absent.
	if be.File != "" || be.Test != "" {
		b.WriteString(" — ")
	}

	if be.Code != "" {
		fmt.Fprintf(&b, "%s: ", be.Code)
	}
	if be.Rule != "" {
		fmt.Fprintf(&b, "(%s) ", be.Rule)
	}
	b.WriteString(be.Message)

	out := b.String()
	// Truncate over-long messages to keep the compact line bounded.
	// The rendered prefix is ~40-60 chars; leave at least 60 chars for
	// the message portion. Hard ceiling 200 BYTES (tests assert ≤130 for
	// typical input, but pathological compiler messages can be huge).
	// "…" is 3 bytes in UTF-8 so we cap the prefix at maxLen-3.
	const maxLen = 200
	const ellipsis = "…"
	if len(out) > maxLen {
		out = out[:maxLen-len(ellipsis)] + ellipsis
	}
	return out
}
