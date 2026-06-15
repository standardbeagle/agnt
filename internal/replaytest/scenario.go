package replaytest

import (
	"encoding/json"
	"regexp"
	"strings"
)

type StepKind string

const (
	StepNavigate StepKind = "navigate"
	StepClick    StepKind = "click"
	StepInput    StepKind = "input"
	StepSubmit   StepKind = "submit"
)

type AssertType string

const (
	AssertText    AssertType = "text"
	AssertPresent AssertType = "present"
)

type Assertion struct {
	Selector string     `json:"selector"`
	Type     AssertType `json:"type"`
	Expect   string     `json:"expect"`
	Mask     bool       `json:"mask"`
}

type Step struct {
	Index        int         `json:"index"`
	Kind         StepKind    `json:"kind"`
	Selector     string      `json:"selector"`
	Value        string      `json:"value,omitempty"`
	DOMSignature string      `json:"dom_signature"`
	Assertions   []Assertion `json:"assertions"`
}

type MatchKey struct {
	Method    string   `json:"method"`
	Path      string   `json:"path"`
	QueryKeys []string `json:"query_keys"`
}

type Recording struct {
	Match          MatchKey          `json:"match"`
	RequestBodySig string            `json:"request_body_sig,omitempty"`
	Status         int               `json:"status"`
	Headers        map[string]string `json:"headers"`
	BodyRef        string            `json:"body_ref"`
	Hits           int               `json:"hits"`
}

type Scenario struct {
	Name       string            `json:"name"`
	Version    int               `json:"version"`
	RecordedAt string            `json:"recorded_at"`
	BaseURL    string            `json:"base_url"`
	Steps      []Step            `json:"steps"`
	Recordings []Recording       `json:"recordings"`
	Blobs      map[string]string `json:"blobs"`
}

// idSegment matches a path segment that looks like a volatile identifier:
// pure digits, or a long (>=12 char) opaque token (hex/uuid-like).
var idSegment = regexp.MustCompile(`^([0-9]+|[0-9a-zA-Z-]{12,})$`)

// TemplatePath replaces identifier-looking path segments with ":id" so that
// recordings match across differing ids at replay time.
func TemplatePath(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if idSegment.MatchString(seg) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}

func (s *Scenario) MarshalJSON() ([]byte, error) {
	type alias Scenario
	return json.MarshalIndent((*alias)(s), "", "  ")
}

func UnmarshalScenario(data []byte) (*Scenario, error) {
	var s Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
