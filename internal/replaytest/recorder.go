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
//
// Steps always open with a navigate to the page the recording started on: the
// replay driver starts from a blank browser, so without it the first recorded
// click has no document to find its target in. Each step also asserts that the
// element the user touched next is present once it has run — the one check the
// log supports without a DOM snapshot. DOM signatures are captured by the first
// baseline replay (see Driver.RunSeed).
func AssembleScenario(name, baseURL string, entries []proxy.LogEntry) *Scenario {
	sc := &Scenario{
		Name:       name,
		Version:    1,
		BaseURL:    baseURL,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
		Blobs:      map[string]string{},
	}
	sc.Steps = []Step{{Kind: StepNavigate, Selector: startPath(entries)}}
	blobN := 0
	for _, e := range entries {
		switch e.Type {
		case proxy.LogTypeInteraction:
			if e.Interaction == nil {
				continue
			}
			kind, ok := mapInteractionKind(e.Interaction.EventType)
			if !ok || e.Interaction.Target.Selector == "" {
				continue
			}
			// The page reports the field's whole value on every keystroke;
			// keep only the last one so replay types it once.
			if last := &sc.Steps[len(sc.Steps)-1]; kind == StepInput && last.Kind == StepInput && last.Selector == e.Interaction.Target.Selector {
				last.Value = e.Interaction.Value
				continue
			}
			sc.Steps = append(sc.Steps, Step{
				Index:    len(sc.Steps),
				Kind:     kind,
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
	for i := 0; i+1 < len(sc.Steps); i++ {
		sc.Steps[i].Assertions = []Assertion{{Selector: sc.Steps[i+1].Selector, Type: AssertPresent}}
	}
	coalesceHits(sc)
	return sc
}

// startPath returns the path (with query) of the page the recording began on:
// the page of the first interaction, else the first HTML document fetched, else
// the site root.
func startPath(entries []proxy.LogEntry) string {
	for _, e := range entries {
		raw := ""
		switch {
		case e.Type == proxy.LogTypeInteraction && e.Interaction != nil:
			raw = e.Interaction.URL
		case e.Type == proxy.LogTypeHTTP && e.HTTP != nil && e.HTTP.Method == "GET" && isHTML(e.HTTP.ResponseHeaders):
			raw = e.HTTP.URL
		}
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Path != "" {
			return u.RequestURI()
		}
	}
	return "/"
}

func isHTML(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "Content-Type") {
			return strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "text/html")
		}
	}
	return false
}

// mapInteractionKind maps a recorded browser event to the step that replays
// it. ok is false for events with no replayable action (scroll, focus, blur,
// mousemove, bare keydown): they are dropped rather than replayed as something
// they were not.
func mapInteractionKind(k string) (kind StepKind, ok bool) {
	switch strings.ToLower(k) {
	case "click", "dblclick", "contextmenu":
		return StepClick, true
	case "input", "change":
		return StepInput, true
	case "submit":
		return StepSubmit, true
	default:
		return "", false
	}
}

// splitPathQuery extracts a leading-slash path and the sorted-by-appearance
// set of query keys from a raw URL or path. Scheme+host are stripped so the
// resulting path is always a leading-slash path. The cache-buster "_" key is
// dropped.
func splitPathQuery(rawURL string) (string, []string) {
	path := rawURL
	query := ""
	if u, err := url.Parse(rawURL); err == nil && (u.Path != "" || u.Host != "") {
		path = u.Path
		if path == "" {
			path = "/"
		}
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
