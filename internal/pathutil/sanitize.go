// Package pathutil provides shared path sanitization utilities for
// user-controlled path components used in filesystem operations.
package pathutil

import "strings"

// SafePathComponent sanitizes a string for safe use as a single path component.
// It strips directory separators, path traversal sequences, null bytes, and
// other characters that are problematic in filenames. The result is safe to
// embed in filepath.Join calls without risk of path traversal.
func SafePathComponent(s string) string {
	// Strip null bytes (can truncate paths on some systems)
	s = strings.ReplaceAll(s, "\x00", "")

	// Replace directory separators and traversal-dangerous characters
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "",
		"<", "",
		">", "",
		"|", "-",
		" ", "_",
	)
	s = replacer.Replace(s)

	// Strip leading dots to prevent hidden files or ".." traversal remnants
	s = strings.TrimLeft(s, ".")

	// Collapse any remaining ".." sequences that survived replacement
	// (e.g., if input was "..-.." after separator replacement)
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", "")
	}

	// Strip leading dashes left over from collapsed patterns
	s = strings.TrimLeft(s, "-")

	// Limit length to avoid filesystem issues
	if len(s) > 50 {
		s = s[:50]
	}

	// If nothing remains, return a safe fallback
	if s == "" {
		return "_"
	}

	return s
}
