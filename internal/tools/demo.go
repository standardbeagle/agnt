package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/go-sdk/mcp"
)

// DemoInput is the input for the `demo` tool — the narrated demo-video
// authoring surface. It shells to the in-repo engine
// (docs-site/screenshots/engine/demo.mjs) as a daemon-managed process so
// long recordings survive and report like any managed script.
type DemoInput struct {
	Action string `json:"action" jsonschema:"Action to run: list (enumerate demos with segment/narration summary), record (start a recording, optionally scoped with only), assemble (re-mux from existing segment captures), inspect (cut-point aid), publish (share via the demo-publish target)"`
	Name   string `json:"name,omitempty" jsonschema:"Demo directory name under docs-site/screenshots/demos (required for record/assemble/inspect/publish)"`
	Only   string `json:"only,omitempty" jsonschema:"For record: comma-separated segment ids to record instead of the whole demo (maps to the engine --only flag)"`
	Path   string `json:"path,omitempty" jsonschema:"Project directory whose docs-site/screenshots engine to use (defaults to the session project)"`
}

// DemoSegmentSummary is one segment in a demo's summary.
type DemoSegmentSummary struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// DemoSummary summarizes a single demo directory for the list action.
type DemoSummary struct {
	Name              string               `json:"name"`
	Segments          []DemoSegmentSummary `json:"segments,omitempty"`
	SegmentCount      int                  `json:"segment_count"`
	HasNarration      bool                 `json:"has_narration"`
	NarrationVoice    string               `json:"narration_voice,omitempty"`
	NarrationSegments int                  `json:"narration_segments"`
}

// DemoOutput is the output for the `demo` tool.
type DemoOutput struct {
	Action     string `json:"action"`
	EngineRoot string `json:"engine_root,omitempty"`
	// list
	Demos []DemoSummary `json:"demos,omitempty"`
	Count int           `json:"count,omitempty"`
	// record / assemble — managed process, addressable via the proc tool.
	ProcessID string `json:"process_id,omitempty"`
	State     string `json:"state,omitempty"`
	Command   string `json:"command,omitempty"`
	// Advisory / next-step message.
	Message string `json:"message,omitempty"`
}

// demoEngine holds the resolved engine paths for a project.
type demoEngine struct {
	Entry       string // docs-site/screenshots/engine/demo.mjs
	Screenshots string // docs-site/screenshots
	DemosDir    string // docs-site/screenshots/demos
}

// resolveDemoEngine locates the in-repo demo engine for a project and fails
// loud (naming the requirement) when the project has no engine checkout. This
// is a repo-checkout capability — the engine ships in the agnt repository's
// docs-site/screenshots tree, not inside the installed agnt binary — so the
// error must say so rather than looking like a transient failure.
func resolveDemoEngine(projectPath string) (demoEngine, error) {
	screenshots := filepath.Join(projectPath, "docs-site", "screenshots")
	entry := filepath.Join(screenshots, "engine", "demo.mjs")
	if _, err := os.Stat(entry); err != nil {
		return demoEngine{}, fmt.Errorf(
			"demo requires the in-repo demo engine at %s, which is not present in this project (%s). "+
				"This is a repo-checkout capability (the agnt docs-site/screenshots demo engine), not part of the installed agnt binary. "+
				"Run demo from a checkout of the agnt repository (or a project that vendors docs-site/screenshots/engine).",
			entry, projectPath)
	}
	return demoEngine{
		Entry:       entry,
		Screenshots: screenshots,
		DemosDir:    filepath.Join(screenshots, "demos"),
	}, nil
}

// demoSpec is the subset of demo.json the list summary reads.
type demoSpec struct {
	Name     string `json:"name"`
	Segments []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"segments"`
	Narration *struct {
		Voice    string `json:"voice"`
		Segments []struct {
			ID string `json:"id"`
		} `json:"segments"`
	} `json:"narration"`
}

// listDemos enumerates demo directories (those carrying a demo.json) under
// demosDir and summarizes each. A demo whose demo.json is unreadable/malformed
// is skipped with its name still surfaced, so one broken demo never blackholes
// the whole listing. The result is name-sorted for stable output.
func listDemos(demosDir string) ([]DemoSummary, error) {
	entries, err := os.ReadDir(demosDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read demos dir %s: %w", demosDir, err)
	}
	var demos []DemoSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		jsonPath := filepath.Join(demosDir, e.Name(), "demo.json")
		raw, rerr := os.ReadFile(jsonPath)
		if rerr != nil {
			continue // not a demo directory
		}
		var spec demoSpec
		summary := DemoSummary{Name: e.Name()}
		if jerr := json.Unmarshal(raw, &spec); jerr == nil {
			if spec.Name != "" {
				summary.Name = spec.Name
			}
			for _, s := range spec.Segments {
				summary.Segments = append(summary.Segments, DemoSegmentSummary{ID: s.ID, Type: s.Type})
			}
			summary.SegmentCount = len(spec.Segments)
			if spec.Narration != nil {
				summary.HasNarration = true
				summary.NarrationVoice = spec.Narration.Voice
				summary.NarrationSegments = len(spec.Narration.Segments)
			}
		}
		// A malformed demo.json still lists its directory name (discoverable),
		// just without the parsed segment/narration summary.
		demos = append(demos, summary)
	}
	sort.Slice(demos, func(i, j int) bool { return demos[i].Name < demos[j].Name })
	return demos, nil
}

// buildDemoEngineArgs builds the argv (after "node") for a demo engine run:
//
//	record:        [entry, demos/<name>]
//	record --only: [entry, demos/<name>, --only=<seg>]
//	assemble:      [entry, demos/<name>, --assemble-only]
//
// The demo dir is passed relative to the screenshots root (demos/<name>), which
// is how the engine resolves it (against its own location, not the cwd).
func buildDemoEngineArgs(entry, name, only string, assembleOnly bool) []string {
	args := []string{entry, "demos/" + name}
	if assembleOnly {
		return append(args, "--assemble-only")
	}
	if only != "" {
		args = append(args, "--only="+only)
	}
	return args
}

// makeDemoHandler creates the handler for the `demo` tool.
func (dt *DaemonTools) makeDemoHandler() func(context.Context, *mcp.CallToolRequest, DemoInput) (*mcp.CallToolResult, DemoOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input DemoInput) (*mcp.CallToolResult, DemoOutput, error) {
		projectPath := input.Path
		if projectPath == "" {
			projectPath = getProjectPath()
		}
		if projectPath == "" {
			return fail[DemoOutput]("demo: could not determine project path")
		}
		absPath, err := filepath.Abs(projectPath)
		if err != nil {
			return fail[DemoOutput](fmt.Sprintf("demo: failed to resolve path %q: %v", projectPath, err))
		}

		// Every action needs the engine checkout; resolve (and fail loud) first.
		eng, err := resolveDemoEngine(absPath)
		if err != nil {
			return fail[DemoOutput](err.Error())
		}

		switch input.Action {
		case "list":
			demos, lerr := listDemos(eng.DemosDir)
			if lerr != nil {
				return fail[DemoOutput](fmt.Sprintf("demo list: %v", lerr))
			}
			return nil, DemoOutput{
				Action:     "list",
				EngineRoot: eng.Screenshots,
				Demos:      demos,
				Count:      len(demos),
			}, nil

		case "record":
			return dt.runDemoEngine(input, eng, absPath, "record", buildDemoEngineArgs(eng.Entry, input.Name, input.Only, false))

		case "assemble":
			return dt.runDemoEngine(input, eng, absPath, "assemble", buildDemoEngineArgs(eng.Entry, input.Name, "", true))

		case "inspect", "publish":
			// The engine (demo.mjs) has no inspect/publish subcommand yet. Fail
			// loud rather than silently no-op; wiring lands as a follow-up once
			// the engine grows those subcommands.
			return fail[DemoOutput](fmt.Sprintf(
				"demo %s is not yet available: the in-repo engine (%s) has no %s subcommand yet. "+
					"Only list/record/assemble are wired. This action is tracked as follow-up engine wiring.",
				input.Action, eng.Entry, input.Action))

		default:
			return fail[DemoOutput](fmt.Sprintf(
				"demo: unknown action %q. Use: list, record, assemble, inspect, publish", input.Action))
		}
	}
}

// runDemoEngine starts a demo engine invocation as a daemon-managed process
// (PROC RUN), returning the process id immediately. The recording then survives
// and reports like any managed script: status/output/stop via the proc tool.
// Recordings never auto-restart — a finished (or crashed) recording must stay
// down, not loop.
func (dt *DaemonTools) runDemoEngine(input DemoInput, eng demoEngine, absPath, kind string, args []string) (*mcp.CallToolResult, DemoOutput, error) {
	if input.Name == "" {
		return fail[DemoOutput](fmt.Sprintf("demo %s: name required (a demo directory under %s)", kind, eng.DemosDir))
	}
	if _, err := os.Stat(filepath.Join(eng.DemosDir, input.Name, "demo.json")); err != nil {
		return fail[DemoOutput](fmt.Sprintf(
			"demo %s: no demo named %q under %s (expected %s). Use demo {action:\"list\"} to see available demos.",
			kind, input.Name, eng.DemosDir, filepath.Join(eng.DemosDir, input.Name, "demo.json")))
	}

	if err := dt.ensureConnected(); err != nil {
		return fail[DemoOutput](err.Error())
	}

	id := "demo-" + input.Name
	if kind == "assemble" {
		id = "demo-assemble-" + input.Name
	}

	cfg := daemonclient.ProcRunConfig{
		Command:     "node",
		Args:        args,
		Cwd:         eng.Screenshots,
		ProjectPath: absPath,
		AutoRestart: false,
	}
	result, err := dt.client.ProcRun(id, cfg)
	if err != nil {
		return formatDaemonError(err, "demo"), DemoOutput{}, nil
	}

	return nil, DemoOutput{
		Action:     kind,
		EngineRoot: eng.Screenshots,
		ProcessID:  getString(result, "process_id"),
		State:      getString(result, "state"),
		Command:    "node " + eng.Entry,
		Message: fmt.Sprintf(
			"%s started as managed process %q; observe with proc {action:\"output\", process_id:%q} and stop with proc {action:\"stop\", process_id:%q}",
			kind, getString(result, "process_id"), getString(result, "process_id"), getString(result, "process_id")),
	}, nil
}
