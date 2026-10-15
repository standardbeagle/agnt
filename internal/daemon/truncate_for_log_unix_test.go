//go:build unix

package daemon

import (
	"testing"
	"unicode/utf8"
)

// truncateForLog promises n runes. Cutting on bytes split multi-byte
// characters (a cmdline with a non-ASCII path) into invalid UTF-8 and kept
// fewer characters than asked.
func TestTruncateForLog_CutsOnRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdef", 3, "abc…"},
		{"/home/zoë/プロジェクト/agnt run", 12, "/home/zoë/プロ…"},
		{"ééé", 3, "ééé"}, // 6 bytes, 3 runes: nothing to clip
		{"anything", 0, "anything"},
	}
	for _, tc := range cases {
		got := truncateForLog(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("truncateForLog(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncateForLog(%q, %d) = %q is not valid UTF-8", tc.in, tc.n, got)
		}
	}
}
