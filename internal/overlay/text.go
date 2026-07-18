package overlay

import "unicode/utf8"

// TruncateRunes shortens s to at most maxRunes runes, appending suffix only when
// it actually truncates. It counts and cuts on rune boundaries, so unlike a
// byte-offset cut (s[:n]) it can never split a multibyte UTF-8 sequence — a
// split produces invalid UTF-8 that corrupts anything downstream treating the
// result as text, most importantly the agent's PTY stdin. maxRunes <= 0 returns
// s unchanged.
func TruncateRunes(s string, maxRunes int, suffix string) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	count := 0
	for i := range s { // ranging a string steps by rune-start byte index
		if count == maxRunes {
			return s[:i] + suffix
		}
		count++
	}
	return s + suffix // unreachable: the RuneCount guard proves a cut point exists
}
