//go:build !windows

package platform

import "strings"

// parseStatFieldsAfterComm returns the whitespace-separated fields of a
// /proc/<pid>/stat record that come AFTER the comm field. The stat layout
// is:
//
//	pid (comm) state ppid pgrp session ...
//
// The comm field (the executable name) is wrapped in parentheses and is the
// only field permitted to contain whitespace or embedded parentheses. To
// skip it safely we scan to the LAST ')' in the record — comm like
// "(sh -c)" or "((weird))" would defeat a first-paren or field-count split,
// but the last ')' always closes comm because every field after it is a
// number, char flag, or unsigned value with no parens.
//
// The returned slice is 0-indexed relative to the first field after comm:
//
//	fields[0] = state
//	fields[1] = ppid
//	fields[2] = pgrp (pgid)
//	...
//
// Returns nil when the record has no ')' (malformed / empty input) so callers
// can treat "no fields" uniformly. A record whose ')' is the final byte
// yields an empty (non-nil-significant) result via strings.Fields("") == nil,
// which callers already guard with their len() checks.
func parseStatFieldsAfterComm(data []byte) []string {
	idx := strings.LastIndex(string(data), ")")
	if idx < 0 {
		return nil
	}
	return strings.Fields(string(data[idx+1:]))
}
