package agentbench

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed testdata/baseline
var baselineFS embed.FS

// Scenario describes one pinned debugging scenario.
type Scenario struct {
	Name  string
	Blurb string
}

// scenarios is the pinned catalogue. Names must match
// testdata/baseline/<name>.json 1:1 — TestScenarioCatalogueMatchesTestdata
// asserts it.
var scenarios = map[string]Scenario{
	"blank_page":         {"blank_page", "page renders empty; locate the failing render path"},
	"dead_click":         {"dead_click", "click does nothing; find what swallowed the event"},
	"mobile_overflow":    {"mobile_overflow", "horizontal scrollbar on mobile viewport"},
	"zindex_positioning": {"zindex_positioning", "element hidden behind another despite z-index"},
	"api_failure":        {"api_failure", "API call fails; trace request to backend error"},
	"loading_flicker":    {"loading_flicker", "spinner flashes repeatedly during load"},
	"a11y_failure":       {"a11y_failure", "accessibility audit failures on a page"},
	"release_qa":         {"release_qa", "pre-release QA sweep across audits and snapshots"},
}

// Scenarios returns the catalogue in stable name order.
func Scenarios() []Scenario {
	out := make([]Scenario, 0, len(scenarios))
	for _, s := range scenarios {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadScenario parses the baseline trace for name from
// testdata/baseline/<name>.json. Unknown names return an error.
func LoadScenario(name string) (*Trace, error) {
	if _, ok := scenarios[name]; !ok {
		return nil, fmt.Errorf("agentbench: unknown scenario %q (known: %s)",
			name, strings.Join(scenarioNames(), ", "))
	}
	data, err := baselineFS.ReadFile("testdata/baseline/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("agentbench: load scenario %q: %w", name, err)
	}
	var t Trace
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("agentbench: parse scenario %q: %w", name, err)
	}
	if err := t.validate(); err != nil {
		return nil, fmt.Errorf("agentbench: scenario %q: %w", name, err)
	}
	return &t, nil
}

func scenarioNames() []string {
	names := make([]string, 0, len(scenarios))
	for n := range scenarios {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
