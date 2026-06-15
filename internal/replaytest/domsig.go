package replaytest

import (
	"encoding/hex"
	"regexp"
	"strings"

	"lukechampine.com/blake3"
)

var wsRun = regexp.MustCompile(`\s+`)
var wsAfterTag = regexp.MustCompile(`>\s+`)

// DOMSignature returns a stable hash of an HTML fragment with volatile
// attributes removed and whitespace collapsed, so cosmetic noise does not
// register as a regression.
func DOMSignature(html string, volatileAttrs []string) string {
	norm := html
	for _, attr := range volatileAttrs {
		re := regexp.MustCompile(regexp.QuoteMeta(attr) + `="[^"]*"`)
		norm = re.ReplaceAllString(norm, "")
	}
	norm = wsRun.ReplaceAllString(norm, " ")
	norm = wsAfterTag.ReplaceAllString(norm, ">")
	norm = strings.TrimSpace(norm)
	sum := blake3.Sum256([]byte(norm))
	return "blake3:" + hex.EncodeToString(sum[:])
}
