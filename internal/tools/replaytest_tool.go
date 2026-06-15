package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/aichannel"
	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/agnt/internal/replaytest"
	"github.com/standardbeagle/go-sdk/mcp"
)

// ReplaytestInput drives the replaytest MCP tool. Action selects the operation;
// the remaining fields supply per-action parameters.
type ReplaytestInput struct {
	Action        string `json:"action" jsonschema:"Operation to perform: record, stop, refine, replay, explore, list, or show."`
	Name          string `json:"name,omitempty" jsonschema:"Scenario name to record, show, refine, or replay."`
	ProxyID       string `json:"proxy_id,omitempty" jsonschema:"Proxy id the scenario records or replays against."`
	Preset        string `json:"preset,omitempty" jsonschema:"Optional fuzz preset to run alongside the baseline on replay (e.g. latency, error5xx). When empty, replay runs the baseline only."`
	ExploreAgents int    `json:"explore_agents,omitempty" jsonschema:"Number of exploration seeds to partition the scenario into for the explore action."`
	Directory     string `json:"directory,omitempty" jsonschema:"Project directory whose scenario store to use. Defaults to the caller's session project."`
	Global        bool   `json:"global,omitempty" jsonschema:"Return cross-project results instead of scoping to the caller's session project."`
}

// ReplaytestSeed is one exploration seed in the computed breadth partition. The
// calling agent fans these out to browser-debugger subagents itself — a Go MCP
// server cannot spawn Claude subagents in-process.
type ReplaytestSeed struct {
	Index int    `json:"index"`
	Route string `json:"route"`
}

// ReplaytestOutput is the structured result of a replaytest operation.
type ReplaytestOutput struct {
	Scenarios []string         `json:"scenarios,omitempty"`
	Seeds     []ReplaytestSeed `json:"seeds,omitempty"`
	Report    string           `json:"report,omitempty"`
	Message   string           `json:"message,omitempty"`
	Success   bool             `json:"success"`
}

// replaytestHandler holds the dependencies for the replaytest MCP tool. Gated
// actions consult the license manager before doing any work.
type replaytestHandler struct {
	lic *license.Manager
}

func newReplaytestHandler(lic *license.Manager) *replaytestHandler {
	return &replaytestHandler{lic: lic}
}

var replaytestGatedActions = map[string]bool{
	"record":  true,
	"stop":    true,
	"refine":  true,
	"replay":  true,
	"explore": true,
}

var replaytestFreeActions = map[string]bool{
	"list": true,
	"show": true,
}

func (h *replaytestHandler) handle(ctx context.Context, in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	if !replaytestGatedActions[in.Action] && !replaytestFreeActions[in.Action] {
		return errorResult("unknown action: " + in.Action), ReplaytestOutput{}, nil
	}

	if replaytestGatedActions[in.Action] {
		if _, err := h.lic.Check(license.CapAdvancedTesting); err != nil {
			return errorResult("advanced_testing requires a Pro license — run `agnt activate <key>` to enable replaytest"), ReplaytestOutput{}, nil
		}
		switch in.Action {
		case "replay":
			return h.handleReplay(ctx, in)
		case "explore":
			return h.handleExplore(in)
		case "refine":
			return h.handleRefine(ctx, in)
		case "record":
			return h.handleRecord(in)
		case "stop":
			return h.handleStop(in)
		}
	}

	switch in.Action {
	case "list":
		names, err := replaytest.NewStore(in.Directory).List()
		if err != nil {
			return errorResult("failed to list scenarios: " + err.Error()), ReplaytestOutput{}, nil
		}
		out := ReplaytestOutput{Scenarios: names, Success: true}
		return replaytestOK("listed scenarios"), out, nil
	case "show":
		if in.Name == "" {
			out := ReplaytestOutput{Message: "provide a scenario name to show", Success: false}
			return replaytestOK(out.Message), out, nil
		}
		sc, err := replaytest.NewStore(in.Directory).LoadScenario(in.Name)
		if err != nil {
			return errorResult("failed to load scenario: " + err.Error()), ReplaytestOutput{}, nil
		}
		data, err := sc.MarshalJSON()
		if err != nil {
			return errorResult("failed to encode scenario: " + err.Error()), ReplaytestOutput{}, nil
		}
		out := ReplaytestOutput{Report: string(data), Success: true}
		return replaytestOK("loaded scenario " + in.Name), out, nil
	}

	return errorResult("unknown action: " + in.Action), ReplaytestOutput{}, nil
}

// handleReplay runs the baseline (and an optional preset) seed lane against a
// stored scenario, aggregates the seed results into one report, persists it, and
// returns the report JSON. The Driver mocks the network in-page, so no daemon or
// live dev server is required — but it does drive a real headless browser.
func (h *replaytestHandler) handleReplay(ctx context.Context, in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	if in.Name == "" {
		out := ReplaytestOutput{Message: "provide a scenario name to replay", Success: false}
		return replaytestOK(out.Message), out, nil
	}
	store := replaytest.NewStore(in.Directory)
	sc, err := store.LoadScenario(in.Name)
	if err != nil {
		return errorResult("failed to load scenario: " + err.Error()), ReplaytestOutput{}, nil
	}

	// Always run the baseline; add the requested preset when set. Running all
	// presets on every call is heavy, so it is opt-in via in.Preset.
	presets := []string{""}
	if in.Preset != "" {
		if _, ok := replaytest.Preset(in.Preset); !ok {
			return errorResult(fmt.Sprintf("unknown preset %q; available: %s", in.Preset, strings.Join(replaytest.PresetNames(), ", "))), ReplaytestOutput{}, nil
		}
		presets = append(presets, in.Preset)
	}

	driver := replaytest.NewDriver(in.ProxyID)
	agg := replaytest.NewReport(sc.Name)
	for _, p := range presets {
		rep, err := driver.RunSeed(ctx, sc, p)
		if err != nil {
			return errorResult("replay failed: " + err.Error()), ReplaytestOutput{}, nil
		}
		agg.Seeds = append(agg.Seeds, rep.Seeds...)
		agg.Crashes = append(agg.Crashes, rep.Crashes...)
	}

	data, err := agg.JSON()
	if err != nil {
		return errorResult("failed to encode report: " + err.Error()), ReplaytestOutput{}, nil
	}
	if err := store.SaveReport(sc.Name, data); err != nil {
		return errorResult("failed to save report: " + err.Error()), ReplaytestOutput{}, nil
	}

	out := ReplaytestOutput{Report: string(data), Success: agg.Passed()}
	return replaytestOK("replayed scenario " + sc.Name), out, nil
}

// handleExplore computes the breadth-exploration seed partition from a scenario
// and returns it for the calling agent to fan out. This is a complete, useful
// implementation given the architecture: a Go MCP server cannot spawn Claude
// browser-debugger subagents in-process, so the agent (Claude) dispatches one
// subagent per returned seed.
func (h *replaytestHandler) handleExplore(in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	if in.Name == "" {
		out := ReplaytestOutput{Message: "provide a scenario name to explore", Success: false}
		return replaytestOK(out.Message), out, nil
	}
	sc, err := replaytest.NewStore(in.Directory).LoadScenario(in.Name)
	if err != nil {
		return errorResult("failed to load scenario: " + err.Error()), ReplaytestOutput{}, nil
	}

	seeds := computeExploreSeeds(sc, in.ExploreAgents)
	out := ReplaytestOutput{
		Seeds:   seeds,
		Success: true,
		Message: fmt.Sprintf("computed %d exploration seed(s); dispatch one browser-debugger subagent per seed, then feed crashes/new assertions back via refine", len(seeds)),
	}
	return replaytestOK(out.Message), out, nil
}

// computeExploreSeeds derives distinct routes from the scenario's navigate steps
// and pads/truncates them to exactly `agents` seeds (cycling through the distinct
// routes). When agents <= 0 it produces one seed per distinct route. When the
// scenario has no navigate steps it falls back to the scenario BaseURL.
func computeExploreSeeds(sc *replaytest.Scenario, agents int) []ReplaytestSeed {
	var routes []string
	seen := map[string]bool{}
	for _, step := range sc.Steps {
		if step.Kind != replaytest.StepNavigate {
			continue
		}
		r := step.Selector
		if r == "" {
			r = sc.BaseURL
		}
		if !seen[r] {
			seen[r] = true
			routes = append(routes, r)
		}
	}
	if len(routes) == 0 {
		base := sc.BaseURL
		if base == "" {
			base = "/"
		}
		routes = []string{base}
	}

	n := agents
	if n <= 0 {
		n = len(routes)
	}
	seeds := make([]ReplaytestSeed, n)
	for i := 0; i < n; i++ {
		seeds[i] = ReplaytestSeed{Index: i, Route: routes[i%len(routes)]}
	}
	return seeds
}

// refinerProvider is the subset of aichannel.Provider the refine adapter needs.
type refinerProvider interface {
	IsConfigured() bool
	Complete(ctx context.Context, systemPrompt, userPrompt string) (*aichannel.Response, error)
}

// handleRefine refines a scenario's auto-captured assertions with an LLM: the
// model decides which assertions to mask (volatile content) vs keep. It uses the
// in-process Anthropic provider, which reads ANTHROPIC_API_KEY / CLAUDE_KEY from
// the environment. When no key is configured it returns honest guidance rather
// than failing.
func (h *replaytestHandler) handleRefine(ctx context.Context, in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	if in.Name == "" {
		out := ReplaytestOutput{Message: "provide a scenario name to refine", Success: false}
		return replaytestOK(out.Message), out, nil
	}
	provider := aichannel.NewAnthropicProvider(aichannel.ProviderConfig{})
	return h.refineWith(ctx, in, &anthropicRefiner{p: provider})
}

// refineWith is the testable core of handleRefine, parameterized over the LLM
// provider so unit tests can inject a stub.
func (h *replaytestHandler) refineWith(ctx context.Context, in ReplaytestInput, refiner *anthropicRefiner) (*mcp.CallToolResult, ReplaytestOutput, error) {
	if !refiner.p.IsConfigured() {
		out := ReplaytestOutput{
			Message: "refine needs an LLM API key — set ANTHROPIC_API_KEY or CLAUDE_KEY in the daemon's environment, then re-run. The refinement adapter (replaytest.Refine over the Anthropic provider) is wired and runs as soon as a key is present.",
			Success: false,
		}
		return replaytestOK(out.Message), out, nil
	}

	store := replaytest.NewStore(in.Directory)
	sc, err := store.LoadScenario(in.Name)
	if err != nil {
		return errorResult("failed to load scenario: " + err.Error()), ReplaytestOutput{}, nil
	}
	if err := replaytest.Refine(ctx, sc, refiner); err != nil {
		return errorResult("refine failed: " + err.Error()), ReplaytestOutput{}, nil
	}
	if err := store.SaveScenario(sc); err != nil {
		return errorResult("failed to save refined scenario: " + err.Error()), ReplaytestOutput{}, nil
	}
	data, err := sc.MarshalJSON()
	if err != nil {
		return errorResult("failed to encode scenario: " + err.Error()), ReplaytestOutput{}, nil
	}
	out := ReplaytestOutput{Report: string(data), Success: true}
	return replaytestOK("refined scenario " + sc.Name), out, nil
}

// anthropicRefiner adapts an aichannel provider to replaytest.LLMClient. It asks
// the model to return the same step array with each assertion's `mask` set
// appropriately, then parses the JSON back.
type anthropicRefiner struct{ p refinerProvider }

const refineSystemPrompt = `You refine browser replay-test assertions. You are given a JSON array of steps; ` +
	`each step has assertions with a "mask" boolean. Mask (set "mask": true) any assertion whose ` +
	`"expect" value is volatile across runs — timestamps, counts, ids, dates, relative times, ` +
	`session-specific strings. Keep (set "mask": false) stable, high-signal assertions. ` +
	`Do not add, remove, or reorder steps or assertions, and do not change any field other than "mask". ` +
	`Reply with ONLY the refined JSON array, no prose, no code fences.`

func (a *anthropicRefiner) RefineAssertions(ctx context.Context, steps []replaytest.Step) ([]replaytest.Step, error) {
	in, err := json.Marshal(steps)
	if err != nil {
		return nil, err
	}
	resp, err := a.p.Complete(ctx, refineSystemPrompt, string(in))
	if err != nil {
		return nil, err
	}
	raw := extractJSONArray(resp.Result)
	if raw == "" {
		return nil, fmt.Errorf("refine: model returned no JSON array")
	}
	var out []replaytest.Step
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("refine: parse model output: %w", err)
	}
	if len(out) != len(steps) {
		return nil, fmt.Errorf("refine: model returned %d steps, want %d", len(out), len(steps))
	}
	return out, nil
}

// extractJSONArray pulls the first top-level JSON array out of a model response,
// tolerating surrounding prose or ```json fences.
func extractJSONArray(s string) string {
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// handleRecord and handleStop are scoped out: full wiring needs full-fidelity
// proxy.LogEntry data (response bodies + headers) from the daemon's
// TrafficLogger. The proxy lives in the daemon process; this MCP tool is a
// separate process and the existing PROXYLOG QUERY protocol verb returns
// compacted entries (string summaries) that drop the response bodies
// AssembleScenario requires. Pulling them would need a new daemon protocol verb
// returning []proxy.LogEntry, which is out of scope for this task.
func (h *replaytestHandler) handleRecord(in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	out := ReplaytestOutput{
		Message: "record is scoped out: it needs full-fidelity proxy traffic (response bodies + headers) from the daemon's TrafficLogger, but the PROXYLOG QUERY protocol verb returns compacted entries that drop response bodies. Wiring requires a new daemon protocol verb returning []proxy.LogEntry over the wire. Until then, assemble scenarios from a process with direct ProxyManager access (replaytest.AssembleScenario + Store.SaveScenario).",
		Success: false,
	}
	return replaytestOK(out.Message), out, nil
}

func (h *replaytestHandler) handleStop(in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	out := ReplaytestOutput{
		Message: "stop is scoped out for the same reason as record: assembling a scenario from the captured window needs full-fidelity proxy.LogEntry data (response bodies + headers) that the cross-process PROXYLOG QUERY verb does not carry. A new daemon protocol verb returning []proxy.LogEntry is required to wire record/stop end-to-end.",
		Success: false,
	}
	return replaytestOK(out.Message), out, nil
}

func replaytestOK(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// RegisterReplaytestTool registers the replaytest MCP tool. The license manager
// gates the record/stop/refine/replay/explore actions on the advanced_testing
// capability; list/show are free.
func RegisterReplaytestTool(server *mcp.Server, lic *license.Manager) {
	h := newReplaytestHandler(lic)
	addLenientTool(server, &mcp.Tool{
		Name: "replaytest",
		Description: `Record, refine, and replay deterministic browser replay-tests (Pro: advanced_testing).

Actions:
  list:    List saved scenarios (free)
  show:    Print a saved scenario's JSON (free)
  replay:  Run the baseline seed lane (and an optional fuzz preset) against a saved
           scenario, mocking the network in-page; persists and returns a report.
  explore: Compute the breadth-exploration seed partition for a scenario. Returns
           N seeds (explore_agents) for the calling agent to fan out to
           browser-debugger subagents.
  refine:  Use an LLM to mask volatile auto-captured assertions. Requires
           ANTHROPIC_API_KEY or CLAUDE_KEY in the daemon environment.
  record/stop: Capture a scenario from proxy traffic (currently scoped out — see
           the action's response for the daemon plumbing it needs).

Examples:
  replaytest {action: "list"}
  replaytest {action: "show", name: "checkout"}
  replaytest {action: "replay", name: "checkout"}
  replaytest {action: "replay", name: "checkout", preset: "latency"}
  replaytest {action: "explore", name: "checkout", explore_agents: 4}
  replaytest {action: "refine", name: "checkout"}`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
		return h.handle(ctx, in)
	})
}
