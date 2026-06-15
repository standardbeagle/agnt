package replaytest

import "encoding/json"

type SeedResult struct {
	Preset   string   `json:"preset"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

type Crash struct {
	Route    string `json:"route"`
	Selector string `json:"selector"`
	Error    string `json:"error"`
}

type Report struct {
	Scenario   string       `json:"scenario"`
	Seeds      []SeedResult `json:"seeds"`
	Crashes    []Crash      `json:"crashes"`
	NewAsserts []Assertion  `json:"new_assertions,omitempty"`
}

func NewReport(scenario string) *Report { return &Report{Scenario: scenario} }

func (r *Report) AddSeedResult(preset string, passed bool, failures []string) {
	r.Seeds = append(r.Seeds, SeedResult{Preset: preset, Passed: passed, Failures: failures})
}

func (r *Report) AddCrash(route, selector, errMsg string) {
	r.Crashes = append(r.Crashes, Crash{Route: route, Selector: selector, Error: errMsg})
}

func (r *Report) CrashCount() int { return len(r.Crashes) }

func (r *Report) Passed() bool {
	if len(r.Crashes) > 0 {
		return false
	}
	for _, s := range r.Seeds {
		if !s.Passed {
			return false
		}
	}
	return true
}

func (r *Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
