package tools

import (
	"context"
	"fmt"

	"devtool-mcp/internal/project"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DetectInput defines input for the detect tool.
type DetectInput struct {
	Path string `json:"path,omitempty" jsonschema:"Directory path (defaults to current dir)"`
}

// DetectOutput defines output for detect.
type DetectOutput struct {
	Type           string            `json:"type"`
	Name           string            `json:"name"`
	Scripts        []string          `json:"scripts"`
	PackageManager string            `json:"package_manager,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// RegisterProjectTools adds project-related MCP tools to the server.
func RegisterProjectTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "detect",
		Description: `Detect project type and available scripts.
Example: detect {path: "."} → {type: "go", scripts: ["test", "build", "lint"]}`,
	}, handleDetect)
}

func handleDetect(ctx context.Context, req *mcp.CallToolRequest, input DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
	path := input.Path
	if path == "" {
		path = "."
	}

	proj, err := project.Detect(path)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to detect: %v", err)), DetectOutput{}, nil
	}

	scripts := make([]string, len(proj.Commands))
	for i, cmd := range proj.Commands {
		scripts[i] = cmd.Name
	}

	return nil, DetectOutput{
		Type:           string(proj.Type),
		Name:           proj.Name,
		Scripts:        scripts,
		PackageManager: proj.PackageManager,
		Metadata:       proj.Metadata,
	}, nil
}
