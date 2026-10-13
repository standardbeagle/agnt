package aichannel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// npm (and pnpm/yarn, which reuse the cmd-shim format) put a CLI on PATH on
// Windows as a `<name>.cmd` batch file. A batch file only runs through
// cmd.exe, and cmd.exe cannot carry a multi-line argument — which an agent
// prompt always is. So a shim is never executed here: it is read to recover
// what it would run, and that is started directly with a real argv.
//
// The two shapes cmd-shim has generated, both ending in one launch line:
//
//	"%_prog%"  "%dp0%\node_modules\pkg\cli.js" %*          (current)
//	node  "%~dp0\node_modules\pkg\cli.js" %*               (older)
//
// %dp0% / %~dp0 is the shim's own directory. Newer packages ship a native
// binary and the shim launches it with no interpreter:
//
//	"%dp0%\node_modules\pkg\bin\tool.exe"   %*
//
// Anything else is reported as not-a-shim rather than guessed at.

// shimTarget is what an npm command shim launches, relative to the shim's own
// directory.
type shimTarget struct {
	// Entry is the script (or native executable) path relative to the shim's
	// directory, in the shim's own backslash form, e.g.
	// `node_modules\@scope\pkg\cli.js`.
	Entry string
	// Node reports whether Entry is a script run by node. False means Entry is
	// a native executable launched directly.
	Node bool
	// NodeArgs are interpreter flags the shim places between node and Entry.
	NodeArgs []string
}

// shimDirMarker replaces the batch spellings of "the shim's directory" while a
// launch line is being parsed.
const shimDirMarker = "\x00dp0\x00"

// parseNpmCmdShim extracts the launch target from the text of an npm-style
// `.cmd` shim. ok is false when the text does not match a known shim shape.
// Pure: no filesystem access, identical on every OS.
func parseNpmCmdShim(content string) (target shimTarget, ok bool) {
	// pnpm's variant exports NODE_PATH before launching; starting the script
	// without it would resolve modules differently from the shim. Not a shape
	// this can reproduce faithfully, so it is not claimed.
	if strings.Contains(strings.ToUpper(content), "NODE_PATH") {
		return shimTarget{}, false
	}
	line := shimLaunchLine(content)
	if line == "" {
		return shimTarget{}, false
	}
	tokens, balanced := splitBatchTokens(line)
	if !balanced || len(tokens) < 2 || tokens[len(tokens)-1] != "%*" {
		return shimTarget{}, false
	}
	tokens = tokens[:len(tokens)-1] // drop %*

	// The launched file is always the last token and always shim-relative.
	entry, rel := shimRelative(tokens[len(tokens)-1])
	if !rel || entry == "" {
		return shimTarget{}, false
	}

	if len(tokens) == 1 {
		// No interpreter: only a native executable can be launched this way.
		if !strings.EqualFold(filepath.Ext(strings.ReplaceAll(entry, `\`, "/")), ".exe") {
			return shimTarget{}, false
		}
		return shimTarget{Entry: entry}, true
	}

	if !shimProgIsNode(tokens[0], content) {
		return shimTarget{}, false
	}
	nodeArgs := tokens[1 : len(tokens)-1]
	for _, a := range nodeArgs {
		// A flag is fine; anything needing cmd.exe to expand it is not.
		if !strings.HasPrefix(a, "-") || strings.ContainsAny(a, "%^&|<>") {
			return shimTarget{}, false
		}
	}
	return shimTarget{Entry: entry, Node: true, NodeArgs: append([]string(nil), nodeArgs...)}, true
}

// shimLaunchLine returns the command that forwards `%*`: the last such line,
// reduced to the part after the final `&` (the current shim chains
// `endLocal & … & "%_prog%" …` on one line).
func shimLaunchLine(content string) string {
	var launch string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if strings.HasSuffix(line, "%*") {
			launch = line
		}
	}
	if launch == "" {
		return ""
	}
	if i := lastUnquoted(launch, '&'); i >= 0 {
		launch = launch[i+1:]
	}
	return strings.TrimPrefix(strings.TrimSpace(launch), "@")
}

// lastUnquoted returns the index of the last c in s outside double quotes, or
// -1.
func lastUnquoted(s string, c byte) int {
	last, inQuote := -1, false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == c && !inQuote:
			last = i
		}
	}
	return last
}

// splitBatchTokens splits a batch command on whitespace, keeping a
// double-quoted run (which may contain spaces) as one token with its quotes
// removed. balanced is false for an unterminated quote.
func splitBatchTokens(line string) (tokens []string, balanced bool) {
	var cur strings.Builder
	inQuote, have := false, false
	flush := func() {
		if have {
			tokens = append(tokens, cur.String())
			cur.Reset()
			have = false
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			have = true // "" is still a token
		case (c == ' ' || c == '\t') && !inQuote:
			flush()
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	flush()
	return tokens, !inQuote
}

// shimRelative reports whether tok is a path under the shim's directory and
// returns the part after it. A path that still needs variable expansion is
// rejected.
func shimRelative(tok string) (rel string, ok bool) {
	// Longest spellings first so `%~dp0\` is not left with a stray backslash.
	for _, v := range []string{`%~dp0\`, `%dp0%\`, `%~dp0`, `%dp0%`} {
		if len(tok) >= len(v) && strings.EqualFold(tok[:len(v)], v) {
			rel = tok[len(v):]
			if strings.Contains(rel, "%") {
				return "", false
			}
			return strings.TrimLeft(rel, `\/`), true
		}
	}
	return "", false
}

// shimProgIsNode reports whether the launch line's program token means node:
// the literal `node`, the shim-local `node.exe`, or `%_prog%` in a shim whose
// own fallback assigns `_prog=node`.
func shimProgIsNode(prog, content string) bool {
	switch strings.ToLower(prog) {
	case "node", "node.exe":
		return true
	case "%_prog%":
		return strings.Contains(strings.ToLower(content), `set "_prog=node"`)
	}
	if rel, ok := shimRelative(prog); ok {
		return strings.EqualFold(rel, "node.exe")
	}
	return false
}

// resolvedShim is a shim reduced to a directly startable program.
type resolvedShim struct {
	// Path is the executable to start: node, or the package's native binary.
	Path string
	// Args are the arguments that precede the caller's own (node flags and
	// the script path). Empty for a native binary.
	Args []string
}

// resolveNpmCmdShim reads the shim at shimPath and works out what to start
// instead of it. It prefers the node.exe beside the shim — the one that
// install uses — and falls back to lookPath("node"). The error says why a
// shim could not be resolved; callers decide whether that is fatal.
func resolveNpmCmdShim(shimPath string, lookPath func(string) (string, error)) (resolvedShim, error) {
	data, err := os.ReadFile(shimPath)
	if err != nil {
		return resolvedShim{}, fmt.Errorf("cannot read %s: %w", shimPath, err)
	}
	target, ok := parseNpmCmdShim(string(data))
	if !ok {
		return resolvedShim{}, fmt.Errorf("%s is not a recognized npm command shim", shimPath)
	}

	dir := filepath.Dir(shimPath)
	entry := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(target.Entry, `\`, "/")))
	if info, err := os.Stat(entry); err != nil || info.IsDir() {
		return resolvedShim{}, fmt.Errorf("%s points at %s, which does not exist", shimPath, entry)
	}
	if !target.Node {
		return resolvedShim{Path: entry}, nil
	}

	node := filepath.Join(dir, "node.exe")
	if info, err := os.Stat(node); err != nil || info.IsDir() {
		node, err = lookPath("node")
		if err != nil {
			return resolvedShim{}, fmt.Errorf("%s needs node, which is neither beside the shim nor on PATH", shimPath)
		}
	}
	return resolvedShim{Path: node, Args: append(append([]string(nil), target.NodeArgs...), entry)}, nil
}

// isBatchShimPath reports whether path names a batch file.
func isBatchShimPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return true
	}
	return false
}
