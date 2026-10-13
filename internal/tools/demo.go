package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/go-sdk/mcp"
)

// DemoInput is the input for the `demo` tool — the narrated demo-video
// authoring surface. It shells to the in-repo engine
// (docs-site/screenshots/engine/demo.mjs) as a daemon-managed process so
// long recordings survive and report like any managed script.
type DemoInput struct {
	Action string `json:"action" jsonschema:"Action to run: list (enumerate demos with segment/narration summary), record (start a recording, optionally scoped with only), assemble (re-mux from existing segment captures), inspect (render cut-point contact sheets from the recorded takes), publish (promote the rendered demo into docs-site/static)"`
	Name   string `json:"name,omitempty" jsonschema:"Demo directory name under docs-site/screenshots/demos (required for record/assemble/inspect/publish)"`
	Only   string `json:"only,omitempty" jsonschema:"For record: comma-separated segment ids to record instead of the whole demo (engine --only). For inspect: a single segment id to render instead of every take (engine --inspect=<id>). Rejected for list, assemble and publish, which have no use for it"`
	Path   string `json:"path,omitempty" jsonschema:"Project directory whose docs-site/screenshots engine to use (defaults to the session project)"`
}

// DemoSegmentSummary is one segment in a demo's summary.
type DemoSegmentSummary struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// DemoSummary summarizes a single demo directory for the list action.
//
// Name is ALWAYS the directory name — the value every other action takes as
// `name`. demo.json's own `name` field (what assembly names its output after)
// is reported separately as DisplayName, and only when it differs: the two are
// normally identical, and when they are not, handing back the spec name as
// Name would list a demo that record/assemble/inspect/publish then cannot find.
type DemoSummary struct {
	Name              string               `json:"name"`
	DisplayName       string               `json:"display_name,omitempty"`
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
			if spec.Name != "" && spec.Name != e.Name() {
				summary.DisplayName = spec.Name
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

// demoNameRe is the shape of a demo directory name. The name is joined into
// the engine argv (demos/<name>) and, for publish, into file names under
// docs-site/static, so anything that is not a plain name is refused before it
// reaches the engine. Mirrors DEMO_NAME_RE in engine/lib/publish.mjs.
var demoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validDemoName(name string) bool {
	return demoNameRe.MatchString(name) && !strings.Contains(name, "..")
}

// demoSegmentIDRe is the shape of one segment id as it may appear in `only`.
// The value is concatenated into a single engine argument (--only=<ids> or
// --inspect=<id>), so it is held to plain ids: nothing that could read as
// another flag, a path, or — on a platform that launches through a shell —
// shell syntax.
var demoSegmentIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validateDemoOnly checks `only` for an action. Only record (a comma-separated
// list) and inspect (exactly one id) consume it; every other action refuses a
// non-empty value rather than accepting and ignoring it
// (publish-security-review-lessons §5: every validated input needs a consumer).
func validateDemoOnly(action, only string) error {
	if only == "" {
		return nil
	}
	switch action {
	case "record":
		for _, id := range strings.Split(only, ",") {
			if !demoSegmentIDRe.MatchString(id) {
				return fmt.Errorf("demo record: only must be a comma-separated list of segment ids (letters, digits, '.', '_' and '-'), got %q", only)
			}
		}
		return nil
	case "inspect":
		if strings.Contains(only, ",") {
			return fmt.Errorf("demo inspect: only takes a single segment id, got %q. Omit only to render every recorded take.", only)
		}
		if !demoSegmentIDRe.MatchString(only) {
			return fmt.Errorf("demo inspect: only must be a segment id (letters, digits, '.', '_' and '-'), got %q", only)
		}
		return nil
	default:
		return fmt.Errorf("demo %s: only is not supported for this action (it applies to record and inspect); remove it", action)
	}
}

// buildDemoEngineArgs builds the argv (after "node") for a demo engine run:
//
//	record:         [entry, demos/<name>]
//	record + only:  [entry, demos/<name>, --only=<seg,seg>]
//	assemble:       [entry, demos/<name>, --assemble-only]
//	inspect:        [entry, demos/<name>, --inspect]
//	inspect + only: [entry, demos/<name>, --inspect=<seg>]
//	publish:        [entry, demos/<name>, --publish]
//
// The demo dir is passed relative to the screenshots root (demos/<name>), which
// is how the engine resolves it (against its own location, not the cwd).
func buildDemoEngineArgs(entry, name, kind, only string) []string {
	args := []string{entry, "demos/" + name}
	switch kind {
	case "assemble":
		return append(args, "--assemble-only")
	case "publish":
		return append(args, "--publish")
	case "inspect":
		if only != "" {
			return append(args, "--inspect="+only)
		}
		return append(args, "--inspect")
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
		case "list", "record", "assemble", "inspect", "publish":
			if oerr := validateDemoOnly(input.Action, input.Only); oerr != nil {
				return fail[DemoOutput](oerr.Error())
			}
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

		case "record", "assemble", "publish", "inspect":
			// validateDemoOnly already refused `only` for assemble and publish
			// and checked its shape for record and inspect.
			return dt.runDemoEngine(input, eng, absPath, input.Action, buildDemoEngineArgs(eng.Entry, input.Name, input.Action, input.Only))

		default:
			return fail[DemoOutput](fmt.Sprintf(
				"demo: unknown action %q. Use: list, record, assemble, inspect, publish", input.Action))
		}
	}
}

// demoResultHint says where an action's result lands, since the tool returns
// before the engine has produced it.
func demoResultHint(kind string, eng demoEngine, name string) string {
	switch kind {
	case "inspect":
		return fmt.Sprintf(". Contact sheets are written to %s (one PNG per recorded take); the process output lists each path and any take that has not been recorded yet",
			filepath.Join(eng.DemosDir, name, "out", "inspect"))
	case "publish":
		static := filepath.Join(filepath.Dir(eng.Screenshots), "static")
		return fmt.Sprintf(". Assets are written to %s and %s; the process output prints the <ModeVideo/> embed snippet, or the reason nothing was published (no rendered demo yet, or ffmpeg missing)",
			filepath.Join(static, "video"), filepath.Join(static, "img"))
	}
	return ""
}

// runDemoEngine starts a demo engine invocation as a daemon-managed process
// (PROC RUN), returning the process id immediately. The run then survives and
// reports like any managed script: status/output/stop via the proc tool. Engine
// runs never auto-restart — a finished (or crashed) recording, inspect or
// publish must stay down, not loop.
func (dt *DaemonTools) runDemoEngine(input DemoInput, eng demoEngine, absPath, kind string, args []string) (*mcp.CallToolResult, DemoOutput, error) {
	if input.Name == "" {
		return fail[DemoOutput](fmt.Sprintf("demo %s: name required (a demo directory under %s)", kind, eng.DemosDir))
	}
	if !validDemoName(input.Name) {
		return fail[DemoOutput](fmt.Sprintf(
			"demo %s: invalid name %q — a demo name is a directory under %s (letters, digits, '.', '_' and '-' only)",
			kind, input.Name, eng.DemosDir))
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
	if kind != "record" {
		id = "demo-" + kind + "-" + input.Name
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
			"%s started as managed process %q; observe with proc {action:\"output\", process_id:%q} and stop with proc {action:\"stop\", process_id:%q}%s",
			kind, getString(result, "process_id"), getString(result, "process_id"), getString(result, "process_id"), demoResultHint(kind, eng, input.Name)),
	}, nil
}
