package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/standardbeagle/agnt/internal/project"

	"github.com/standardbeagle/go-sdk/mcp"
)

// DetectInput defines input for the detect tool.
type DetectInput struct {
	Path string `json:"path,omitempty" jsonschema:"Directory path (defaults to current dir)"`
	Raw  bool   `json:"raw,omitempty" jsonschema:"Return structured JSON only, skip the compact text rendering"`
}

// DetectScript is a single script in DetectOutput, augmented with ready-to-use
// `proc run` / `proc wait` invocations and signal hints so agents don't have to
// hand-construct them.
type DetectScript struct {
	Name          string   `json:"name"`
	Command       string   `json:"command"`
	ProcRun       string   `json:"proc_run"`
	ProcWait      string   `json:"proc_wait"`
	LikelySignals []string `json:"likely_signals"`
}

// DetectOutput defines output for detect.
//
// `Scripts` is the new structured form; `ScriptNames` is kept as a plain string
// list for backward compatibility with callers that just wanted names.
type DetectOutput struct {
	Type           string            `json:"type"`
	Name           string            `json:"name"`
	Framework      string            `json:"framework,omitempty"`
	Scripts        []DetectScript    `json:"scripts"`
	ScriptNames    []string          `json:"script_names"`
	PackageManager string            `json:"package_manager,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Summary        string            `json:"summary,omitempty"`
}

// RegisterProjectTools adds project-related MCP tools to the server.
func RegisterProjectTools(server *mcp.Server) {
	addLenientTool(server, &mcp.Tool{
		Name: "detect",
		Description: `Detect project type and available scripts.
Returns ready-to-paste proc run / proc wait invocations and likely_signals
heuristics for each script, so agents can chain detect → proc run → proc wait
without hand-constructing commands.

Examples:
  detect {}
  detect {path: "."}
  detect {raw: true}   // skip compact text, return JSON only`,
	}, handleDetect)
}

func handleDetect(ctx context.Context, req *mcp.CallToolRequest, input DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
	if err := validateDetectInput(input); err != nil {
		return errorResult(validationError("detect", err)), DetectOutput{}, nil
	}

	path := input.Path
	if path == "" {
		path = "."
	}

	proj, err := project.Detect(path)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to detect: %v", err)), DetectOutput{}, nil
	}

	scripts := buildDetectScripts(proj.Commands)
	scriptNames := make([]string, len(proj.Commands))
	for i, cmd := range proj.Commands {
		scriptNames[i] = cmd.Name
	}

	output := DetectOutput{
		Type:           string(proj.Type),
		Name:           proj.Name,
		Framework:      proj.Metadata["framework"],
		Scripts:        scripts,
		ScriptNames:    scriptNames,
		PackageManager: proj.PackageManager,
		Metadata:       proj.Metadata,
	}

	if input.Raw {
		// Structured-only mode: callers want JSON, no compact text.
		return nil, output, nil
	}

	summary := formatDetectCompact(output)
	output.Summary = summary
	return mcpText(summary), output, nil
}

// buildDetectScripts converts project.CommandDef entries into DetectScript
// records with ready-to-paste proc invocations and signal hints.
func buildDetectScripts(cmds []project.CommandDef) []DetectScript {
	out := make([]DetectScript, 0, len(cmds))
	for _, cmd := range cmds {
		full := cmd.Command
		if len(cmd.Args) > 0 {
			full = cmd.Command + " " + strings.Join(cmd.Args, " ")
		}
		hints := project.PredictSignals(cmd.Name, full)
		// Pick first signal as the wait target (most useful one for the
		// category — `url` for dev servers, `error` for build/test/lint).
		waitSig := "ready"
		if len(hints.Signals) > 0 {
			waitSig = hints.Signals[0]
		}
		out = append(out, DetectScript{
			Name:          cmd.Name,
			Command:       full,
			ProcRun:       fmt.Sprintf(`proc {action:"run", id:%q, command:%q}`, cmd.Name, full),
			ProcWait:      fmt.Sprintf(`proc {action:"wait", process_id:%q, signal:%q, timeout:%d}`, cmd.Name, waitSig, hints.TimeoutMs),
			LikelySignals: hints.Signals,
		})
	}
	return out
}

// formatDetectCompact renders the human-readable compact output block.
func formatDetectCompact(out DetectOutput) string {
	var b strings.Builder

	header := projectTypeLabel(out.Type)
	if out.Framework != "" {
		header = fmt.Sprintf("%s (%s)", header, out.Framework)
	}
	fmt.Fprintf(&b, "=== Detected: %s ===\n", header)

	if len(out.Scripts) == 0 {
		b.WriteString("(no scripts detected)\n")
		return b.String()
	}

	// Compute name column width for alignment.
	nameWidth := 0
	for _, s := range out.Scripts {
		if n := len(s.Name); n > nameWidth {
			nameWidth = n
		}
	}

	// Stable ordering: keep CommandDef declaration order, which is the
	// caller-meaningful order (test/build/lint/dev/...). Don't re-sort here.
	for _, s := range out.Scripts {
		fmt.Fprintf(&b, "%-*s  → %s\n", nameWidth, s.Name, s.ProcRun)
	}

	// Footer tips — one per category present, sorted for determinism.
	tips := collectTips(out.Scripts)
	if len(tips) > 0 {
		b.WriteString("\n")
		for _, t := range tips {
			b.WriteString(t)
			b.WriteString("\n")
		}
	}

	return b.String()
}

// projectTypeLabel returns the display name for a project type.
func projectTypeLabel(t string) string {
	switch t {
	case "go":
		return "Go"
	case "node":
		return "Node.js"
	case "python":
		return "Python"
	case "unknown":
		return "Unknown"
	default:
		return t
	}
}

// collectTips returns proc-wait tips for the categories present in scripts.
// Sorted for deterministic output.
func collectTips(scripts []DetectScript) []string {
	seen := map[string]string{}
	for _, s := range scripts {
		if len(s.LikelySignals) == 0 {
			continue
		}
		switch s.LikelySignals[0] {
		case "url":
			seen["dev"] = `Tip: use proc {action:"wait", signal:"ready"} after starting a dev server.`
		case "error":
			// Differentiate build/test wait targets from lint.
			if hasSignal(s.LikelySignals, "ready") {
				seen["test"] = `Tip: use proc {action:"wait", signal:"error"} on test/build scripts to surface failures fast.`
			} else {
				seen["lint"] = `Tip: lint scripts emit error/warning signals — use proc {action:"wait", signal:"error"}.`
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func hasSignal(signals []string, s string) bool {
	for _, x := range signals {
		if x == s {
			return true
		}
	}
	return false
}
