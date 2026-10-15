package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/pathutil"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-sdk/mcp"
)

type ErrorQueueInput struct {
	Message     string `json:"message" jsonschema:"Error or failure text to add to the queue"`
	Source      string `json:"source,omitempty" jsonschema:"Source identifier, e.g. github-actions, buildkite, deploy, ci"`
	Severity    string `json:"severity,omitempty" jsonschema:"Severity: one of error, warning, info (default: error)"`
	Category    string `json:"category,omitempty" jsonschema:"Category for grouping (default: external)"`
	Description string `json:"description,omitempty" jsonschema:"Short description shown in get_incidents"`
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Project path for get_incidents filtering (default: current directory)"`
}

type ErrorQueueOutput struct {
	Queued      bool   `json:"queued"`
	Source      string `json:"source"`
	Severity    string `json:"severity"`
	ProjectPath string `json:"project_path"`
}

func RegisterErrorQueueTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "error_queue",
		Description: `Add an external CI/CD or infrastructure failure to agnt's unified error queue.

Use this when a failure came from GitHub Actions, Buildkite, deploy logs, a hosted service, or any source outside agnt's proxy/process monitors. The entry appears in get_incidents (source "external") for every session working on that project, and in proc snapshot.

Examples:
  error_queue {message: "GitHub Actions build failed: go test ./...", source: "github-actions", category: "ci"}
  error_queue {message: "Deploy failed: migration timed out", source: "deploy", severity: "error"}`,
	}, dt.makeErrorQueueHandler())
}

func (dt *DaemonTools) makeErrorQueueHandler() func(context.Context, *mcp.CallToolRequest, ErrorQueueInput) (*mcp.CallToolResult, ErrorQueueOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ErrorQueueInput) (*mcp.CallToolResult, ErrorQueueOutput, error) {
		if strings.TrimSpace(input.Message) == "" {
			return fail[ErrorQueueOutput]("message is required")
		}
		source := input.Source
		if source == "" {
			source = "external"
		}
		severity := normalizeQueueSeverity(input.Severity)
		category := input.Category
		if category == "" {
			category = "external"
		}
		description := input.Description
		if description == "" {
			description = "external error"
		}
		projectPath, err := resolveQueueProjectPath(input.ProjectPath)
		if err != nil {
			return fail[ErrorQueueOutput](err.Error())
		}

		if err := dt.ensureConnected(); err != nil {
			return fail[ErrorQueueOutput](err.Error())
		}
		payload := protocol.AlertReportPayload{
			PatternID:   "external." + sanitizeQueueSource(source),
			Severity:    severity,
			Category:    category,
			Description: description,
			Line:        input.Message,
			ScriptID:    source,
			ProjectPath: projectPath,
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if err := dt.currentClient().Client().AlertReport(payload); err != nil {
			return fail[ErrorQueueOutput](fmt.Sprintf("failed to queue error: %v", err))
		}
		return nil, ErrorQueueOutput{
			Queued:      true,
			Source:      source,
			Severity:    severity,
			ProjectPath: projectPath,
		}, nil
	}
}

// resolveQueueProjectPath resolves the project the report belongs to through
// the same chain every other tool uses (getProjectPath: AGNT_PROJECT_PATH, then
// cwd) and in the same normalised form, so the entry matches the scoped
// queries that look for it. A raw os.Getwd() ignored AGNT_PROJECT_PATH and, on
// a case-insensitive filesystem, never compared equal to the normalised scope.
func resolveQueueProjectPath(path string) (string, error) {
	if path == "" {
		resolved := getProjectPath()
		if resolved == "" {
			return "", fmt.Errorf("cannot resolve a project path: pass project_path")
		}
		return resolved, nil
	}
	return pathutil.NormalizeProjectPath(path), nil
}

func normalizeQueueSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warning", "warn":
		return "warning"
	case "info":
		return "info"
	default:
		return "error"
	}
}

func sanitizeQueueSource(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "external"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
