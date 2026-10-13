package daemonclient

import (
	"regexp"
	"testing"
)

// Session codes were "<command>-<UnixNano()%10000>": ten thousand values at
// best, and SESSION REGISTER treats a repeated code as a reconnect. The suffix
// must be random and wide enough that concurrent sessions do not share one.
func TestSessionGenerateCode_IsRandomAndWide(t *testing.T) {
	t.Parallel()
	c := NewClientWithPath("unused.sock") // generation is local; never connects

	shape := regexp.MustCompile(`^claude-[0-9a-f]{8}$`)
	const n = 5000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		code, err := c.SessionGenerateCode("claude")
		if err != nil {
			t.Fatalf("SessionGenerateCode: %v", err)
		}
		if !shape.MatchString(code) {
			t.Fatalf("code %q does not match %s", code, shape)
		}
		seen[code] = struct{}{}
	}
	// With the old 10^4 space, 5000 draws could not exceed ~3900 distinct
	// values; with 32 random bits a repeat is a ~0.3% event, so allow one.
	if len(seen) < n-1 {
		t.Fatalf("%d codes drawn, only %d distinct — the code space is too small", n, len(seen))
	}
}
