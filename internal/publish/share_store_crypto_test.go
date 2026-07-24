package publish

import (
	"os"
	"strings"
	"testing"
)

// TestVerifyUsesConstantTimeCompare pins INV-3 / Deviations #4 at the source
// level: token verification must go through crypto/subtle.ConstantTimeCompare and
// must never fall back to a plain == / bytes.Equal early-return. crypto/rand must
// be the token source. This is the pinned-primitive style the spec calls for
// ("unit test asserts the code path calls subtle.ConstantTimeCompare").
func TestVerifyUsesConstantTimeCompare(t *testing.T) {
	src, err := os.ReadFile("share_store.go")
	if err != nil {
		t.Fatalf("read share_store.go: %v", err)
	}
	s := string(src)

	if !strings.Contains(s, `"crypto/rand"`) {
		t.Fatalf("token source must be crypto/rand (CSPRNG), not math/rand")
	}
	if strings.Contains(s, `"math/rand"`) {
		t.Fatalf("math/rand must never be used for tokens")
	}
	if !strings.Contains(s, `"crypto/subtle"`) {
		t.Fatalf("verify must import crypto/subtle")
	}
	if !strings.Contains(s, "subtle.ConstantTimeCompare") {
		t.Fatalf("token verify must call subtle.ConstantTimeCompare")
	}
	// Guard against a plain-== fallback comparing the token hash for acceptance.
	if strings.Contains(s, "sh.TokenHash == hash") || strings.Contains(s, "hash == sh.TokenHash") {
		t.Fatalf("token verify must not use a plain == on the hash")
	}
}
