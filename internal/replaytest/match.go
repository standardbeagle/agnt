package replaytest

import (
	"sort"
	"strings"
)

// recKey produces the canonical dedup key for a recording: uppercased method,
// templated path, sorted query-key names (values ignored), and body signature
// when present. The JS worker bundle (worker_bundle.go) mirrors this exact
// key format for live-request matching — keep the two in sync.
func recKey(r Recording) string {
	keys := append([]string(nil), r.Match.QueryKeys...)
	sort.Strings(keys)
	key := strings.ToUpper(r.Match.Method) + " " + TemplatePath(r.Match.Path)
	if len(keys) > 0 {
		key += " ?" + strings.Join(keys, ",")
	}
	if r.RequestBodySig != "" {
		key += " #" + r.RequestBodySig
	}
	return key
}
