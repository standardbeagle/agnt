package replaytest

import (
	"net/url"
	"sort"
	"strings"
)

// buildKey produces the canonical match key for a live request: uppercased
// method, templated path, sorted query-key names, and body signature appended
// when present. Query VALUES are ignored; only key names participate.
func buildKey(method, rawURL, bodySig string) string {
	method = strings.ToUpper(method)
	path := rawURL
	query := ""
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		path, query = rawURL[:i], rawURL[i+1:]
	}
	path = TemplatePath(path)

	var keys []string
	if query != "" {
		if vals, err := url.ParseQuery(query); err == nil {
			for k := range vals {
				if k == "_" {
					continue
				}
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	key := method + " " + path
	if len(keys) > 0 {
		key += " ?" + strings.Join(keys, ",")
	}
	if bodySig != "" {
		key += " #" + bodySig
	}
	return key
}

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

// Matcher resolves live requests to recordings, advancing an ordered queue per
// key so repeated identical calls return successive recordings.
type Matcher struct {
	queues map[string][]Recording
}

func NewMatcher(recs []Recording) *Matcher {
	q := make(map[string][]Recording)
	for _, r := range recs {
		k := recKey(r)
		n := r.Hits
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			q[k] = append(q[k], r)
		}
	}
	return &Matcher{queues: q}
}

// Match returns the next recording for the request, or ok=false on a miss.
// It does a single exact templated-key lookup against the queue map. Recordings
// store already-templated paths, so a live request templates to the same key.
func (m *Matcher) Match(method, rawURL, bodySig string) (Recording, bool) {
	k := buildKey(method, rawURL, bodySig)
	if q := m.queues[k]; len(q) > 0 {
		r := q[0]
		m.queues[k] = q[1:]
		return r, true
	}
	return Recording{}, false
}
