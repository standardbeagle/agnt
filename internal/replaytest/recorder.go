package replaytest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// AssembleScenario builds a Scenario from a window of proxy log entries.
// Interaction entries become ordered Steps; HTTP entries become Recordings
// with response bodies out-of-lined into Blobs (blob:0, blob:1, ...). Paths
// are templated via TemplatePath, query keys are extracted (dropping the
// cache-buster "_"), and consecutive identical recordings are coalesced into
// one with an incremented Hits count.
func AssembleScenario(name, baseURL string, entries []proxy.LogEntry) *Scenario {
	sc := &Scenario{
		Name:       name,
		Version:    1,
		BaseURL:    baseURL,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
		Blobs:      map[string]string{},
	}
	blobN := 0
	for _, e := range entries {
		switch e.Type {
		case proxy.LogTypeInteraction:
			if e.Interaction == nil {
				continue
			}
			sc.Steps = append(sc.Steps, Step{
				Index:    len(sc.Steps),
				Kind:     mapInteractionKind(e.Interaction.EventType),
				Selector: e.Interaction.Target.Selector,
				Value:    e.Interaction.Value,
			})
		case proxy.LogTypeHTTP:
			h := e.HTTP
			if h == nil {
				continue
			}
			path, keys := splitPathQuery(h.URL)
			ref := fmt.Sprintf("blob:%d", blobN)
			blobN++
			sc.Blobs[ref] = h.ResponseBody
			sc.Recordings = append(sc.Recordings, Recording{
				Match:          MatchKey{Method: h.Method, Path: TemplatePath(path), QueryKeys: keys},
				RequestBodySig: bodySig(h.RequestBody),
				Status:         h.StatusCode,
				Headers:        h.ResponseHeaders,
				BodyRef:        ref,
				Hits:           1,
			})
		}
	}
	coalesceHits(sc)
	return sc
}

func mapInteractionKind(k string) StepKind {
	switch strings.ToLower(k) {
	case "click", "dblclick", "contextmenu":
		return StepClick
	case "input", "change", "keydown":
		return StepInput
	case "submit":
		return StepSubmit
	default:
		return StepNavigate
	}
}

// splitPathQuery extracts a leading-slash path and the sorted-by-appearance
// set of query keys from a raw URL or path. Scheme+host are stripped so the
// resulting path is always a leading-slash path. The cache-buster "_" key is
// dropped.
func splitPathQuery(rawURL string) (string, []string) {
	path := rawURL
	query := ""
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		path = u.Path
		query = u.RawQuery
	} else if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		path = rawURL[:i]
		query = rawURL[i+1:]
	}
	var keys []string
	if query != "" {
		if vals, err := url.ParseQuery(query); err == nil {
			for k := range vals {
				if k != "_" {
					keys = append(keys, k)
				}
			}
		}
	}
	return path, keys
}

func bodySig(body string) string {
	if body == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}

func coalesceHits(sc *Scenario) {
	if len(sc.Recordings) < 2 {
		return
	}
	out := sc.Recordings[:1]
	for _, r := range sc.Recordings[1:] {
		last := &out[len(out)-1]
		if recKey(*last) == recKey(r) && sc.Blobs[last.BodyRef] == sc.Blobs[r.BodyRef] {
			last.Hits++
			continue
		}
		out = append(out, r)
	}
	sc.Recordings = out
}
