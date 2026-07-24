//go:build !windows

package platform

import "strings"

// ShellQuote single-quotes s for a POSIX shell, escaping embedded single
// quotes ('foo'\”bar'). Always quotes — the safe choice for remote and
// generated commands. Shared by the shim script writer, the sshclient
// session commands, and cmd/agnt's resolver, which previously carried three
// copies (one with a subtly different quote-only-if-needed policy).
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
