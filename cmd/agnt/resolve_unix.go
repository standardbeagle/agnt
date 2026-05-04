//go:build unix

package main

import (
	"os"
	"os/exec"
	"strings"
)

// commandWithArgs creates an exec.Cmd. If the command is not found in
// PATH, it wraps the command in the user's interactive login shell so
// shell aliases and functions defined in shell config files are honored.
func commandWithArgs(name string, args ...string) *execCmd {
	if _, err := exec.LookPath(name); err == nil {
		return newExecCmd(name, args...)
	}
	return wrapInShell(name, args...)
}

// wrapInShell wraps a command in the user's login shell with interactive mode.
// This enables shell aliases and functions defined in shell config files.
func wrapInShell(name string, args ...string) *execCmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	var cmdParts []string
	cmdParts = append(cmdParts, shellQuote(name))
	for _, arg := range args {
		cmdParts = append(cmdParts, shellQuote(arg))
	}
	fullCmd := strings.Join(cmdParts, " ")

	// -i for interactive mode (loads rc files with aliases),
	// -c to execute a command string.
	return newExecCmd(shell, "-ic", fullCmd)
}

// shellQuote quotes a string for safe use in shell commands.
// It uses single quotes and handles embedded single quotes.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}();<>&|") {
		return s
	}
	// Use single quotes, escaping any embedded single quotes
	// 'foo'\''bar' -> foo'bar
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
