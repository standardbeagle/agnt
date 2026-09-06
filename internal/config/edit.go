package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Surgical .agnt.kdl edits.
//
// These functions edit the config file as TEXT rather than re-marshalling the
// parsed struct. A round trip through the marshaller would drop every comment
// and reorder the file — and the shipped default config is mostly comments,
// documenting the keys a developer has not turned on yet. Losing those to a
// one-key change made from the overlay would be a poor trade.
//
// Every edit is validated by re-parsing the result before anything is written,
// and written atomically. An edit that would produce a file the daemon cannot
// load is refused, leaving the original untouched.

// SetProxyStatusURL sets the display-only `status-url` of one proxy. An empty
// url removes the key. The proxies block and the proxy's own block are created
// when absent, so this works on a config that has never declared the proxy.
func SetProxyStatusURL(path, proxyID, statusURL string) error {
	if proxyID == "" {
		return fmt.Errorf("proxy id is required")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	edited, err := setNestedProperty(string(original), []string{"proxies", proxyID}, "status-url", statusURL)
	if err != nil {
		return err
	}

	// Validate before writing: the file the daemon reads must always parse.
	cfg, err := ParseAgntConfig(edited)
	if err != nil {
		return fmt.Errorf("edit would produce an unparseable config (not written): %w", err)
	}
	if pc := cfg.Proxies[proxyID]; statusURL != "" && (pc == nil || pc.StatusURL != statusURL) {
		return fmt.Errorf("edit did not take effect for proxy %q (not written)", proxyID)
	}

	return writeFileAtomic(path, edited)
}

// setNestedProperty sets `key value` inside the block addressed by names,
// creating the blocks it needs. An empty value deletes the key. The returned
// text keeps every other line — including comments — byte for byte.
func setNestedProperty(text string, names []string, key, value string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no block path given")
	}
	lines := strings.Split(text, "\n")

	// Walk down the block path, remembering where each level's body starts and
	// ends so a missing level can be created at the right depth.
	start, end := 0, len(lines)
	indent := ""
	for _, name := range names {
		bodyStart, bodyEnd, found := findBlockBody(lines, start, end, name)
		if !found {
			// Create this level and everything under it, then set the key.
			created := renderBlockPath(names, indexOf(names, name), key, value, indent)
			if value == "" {
				return text, nil // nothing to delete
			}
			lines = insertLines(lines, end, created)
			return strings.Join(lines, "\n"), nil
		}
		start, end = bodyStart, bodyEnd
		indent = leadingWhitespace(lines[bodyStart-1]) + "    "
	}

	// The block exists: replace the key in place if present, else append it as
	// the block's first entry so it lands next to its siblings.
	for i := start; i < end; i++ {
		if propertyKeyOf(lines[i]) == key {
			if value == "" {
				return strings.Join(append(append([]string{}, lines[:i]...), lines[i+1:]...), "\n"), nil
			}
			lines[i] = leadingWhitespace(lines[i]) + key + " " + quoteKDL(value)
			return strings.Join(lines, "\n"), nil
		}
	}
	if value == "" {
		return text, nil // already absent
	}
	lines = insertLines(lines, start, []string{indent + key + " " + quoteKDL(value)})
	return strings.Join(lines, "\n"), nil
}

// findBlockBody locates `name { … }` between lines[from:to] and returns the
// half-open line range of its body. Nested braces are tracked so an inner block
// never terminates the search early.
func findBlockBody(lines []string, from, to int, name string) (bodyStart, bodyEnd int, found bool) {
	for i := from; i < to && i < len(lines); i++ {
		line := stripComment(lines[i])
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasSuffix(strings.TrimSpace(line), "{") {
			continue
		}
		if unquoteKDL(fields[0]) != name {
			continue
		}
		depth := 1
		for j := i + 1; j < len(lines); j++ {
			body := stripComment(lines[j])
			depth += strings.Count(body, "{") - strings.Count(body, "}")
			if depth == 0 {
				return i + 1, j, true
			}
		}
		return 0, 0, false // unbalanced braces: refuse rather than guess
	}
	return 0, 0, false
}

// renderBlockPath renders names[depth:] as nested blocks carrying key/value.
func renderBlockPath(names []string, depth int, key, value, indent string) []string {
	var out []string
	pad := indent
	for _, name := range names[depth:] {
		out = append(out, pad+quoteIfNeeded(name)+" {")
		pad += "    "
	}
	out = append(out, pad+key+" "+quoteKDL(value))
	for i := len(names) - 1; i >= depth; i-- {
		pad = pad[:len(pad)-4]
		out = append(out, pad+"}")
	}
	return out
}

// propertyKeyOf returns the bare key a line declares, or "" for anything that
// is not a simple `key value` entry (comments, block openers, blank lines).
func propertyKeyOf(line string) string {
	trimmed := strings.TrimSpace(stripComment(line))
	if trimmed == "" || strings.HasSuffix(trimmed, "{") || strings.HasPrefix(trimmed, "}") {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}
	return unquoteKDL(fields[0])
}

// stripComment removes a trailing `//` comment. Quoted strings are respected so
// a URL's "//" is never mistaken for one.
func stripComment(line string) string {
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			if i == 0 || line[i-1] != '\\' {
				inQuote = !inQuote
			}
		case '/':
			if !inQuote && i+1 < len(line) && line[i+1] == '/' {
				return line[:i]
			}
		}
	}
	return line
}

func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func insertLines(lines []string, at int, add []string) []string {
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:at]...)
	out = append(out, add...)
	return append(out, lines[at:]...)
}

func indexOf(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return 0
}

func quoteKDL(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func quoteIfNeeded(name string) string {
	if name == "" || strings.ContainsAny(name, " \t\"{}/\\") {
		return quoteKDL(name)
	}
	return name
}

func unquoteKDL(token string) string {
	return strings.Trim(token, `"`)
}

// writeFileAtomic writes via a temp file in the same directory then renames, so
// a crash mid-write can never leave a half-written config the daemon would
// refuse to load.
func writeFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".agnt.kdl.*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	// Match the mode of the file being replaced when it exists.
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmpName, info.Mode())
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
