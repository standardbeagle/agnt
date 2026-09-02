package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/go-sdk/mcp"
)

// booleanAnnotationKeywords are the only JSON Schema keywords whose value is
// legitimately a boolean. Every other `true` in a schema document IS a schema —
// the boolean shorthand a strict client rejects.
var booleanAnnotationKeywords = map[string]bool{
	"uniqueItems": true,
	"deprecated":  true,
	"readOnly":    true,
	"writeOnly":   true,
}

// TestListedToolSchemasCarryNoBooleanSubschema drives the real tools/list
// response an MCP client sees and asserts no tool's input or output schema
// contains a boolean subschema anywhere.
//
// Regression: an `any`-typed output field infers an unrestricted schema, which
// marshals to `true`. Claude Code's SDK entrypoint validates tools/list
// strictly and rejects a boolean property schema, and ONE rejected tool fails
// the whole list — an SDK-spawned agent then sees no agnt tools at all. The
// interactive CLI accepts the same list, which is why this only surfaced for
// SDK-spawned agents. Four tools carried one: api_audit, automation,
// loading_audit, responsive_audit.
//
// The assertion is on the marshalled wire bytes rather than on the Go schema
// values, because the boolean shorthand exists only in the marshalled form —
// asserting on the struct would not see the representation that breaks.
func TestListedToolSchemasCarryNoBooleanSubschema(t *testing.T) {
	ctx := context.Background()
	server := registerAllTools(t)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	var tools []*mcp.Tool
	for tool, err := range clientSession.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		tools = append(tools, tool)
	}
	if len(tools) == 0 {
		t.Fatal("no tools listed — registration harness is broken, so this test proves nothing")
	}

	// Premise: at least one listed tool has an any-typed output field, i.e. the
	// defect class is still reachable. Without this the test passes vacuously
	// the day those fields are retyped.
	sawAnnotatedAny := false

	for _, tool := range tools {
		for _, s := range []struct {
			kind   string
			schema any
		}{
			{"inputSchema", tool.InputSchema},
			{"outputSchema", tool.OutputSchema},
		} {
			if s.schema == nil {
				continue
			}
			raw, err := json.Marshal(s.schema)
			if err != nil {
				t.Fatalf("%s %s: marshal: %v", tool.Name, s.kind, err)
			}
			var tree any
			if err := json.Unmarshal(raw, &tree); err != nil {
				t.Fatalf("%s %s: unmarshal: %v", tool.Name, s.kind, err)
			}
			for _, path := range findBooleanSubschemas(tree, s.kind) {
				t.Errorf("%s: boolean subschema at %s — a strict client rejects the whole tools/list over this\n  schema: %s",
					tool.Name, path, raw)
			}
			if bytes := string(raw); containsAnnotatedAny(bytes) {
				sawAnnotatedAny = true
			}
		}
	}

	if !sawAnnotatedAny {
		t.Error("premise broken: no listed schema carries the annotated any-value stand-in, " +
			"so no unrestricted subschema was normalized and this test asserted nothing")
	}
}

func containsAnnotatedAny(s string) bool {
	return len(s) > 0 && jsonContains(s, `"description":"Arbitrary JSON value"`)
}

func jsonContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// findBooleanSubschemas walks a marshalled schema document and returns the path
// of every `true` that sits in a schema position — any boolean value except the
// handful of keywords whose value is legitimately boolean.
func findBooleanSubschemas(node any, path string) []string {
	var found []string
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			childPath := path + "." + key
			if b, ok := child.(bool); ok {
				if b && !booleanAnnotationKeywords[key] {
					found = append(found, childPath)
				}
				continue
			}
			found = append(found, findBooleanSubschemas(child, childPath)...)
		}
	case []any:
		for i, child := range v {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if b, ok := child.(bool); ok {
				if b {
					found = append(found, childPath)
				}
				continue
			}
			found = append(found, findBooleanSubschemas(child, childPath)...)
		}
	}
	return found
}

// registerAllTools mirrors the registration block in cmd/agnt/serve.go. Keep
// the two in step: a tool registered there but not here is a tool this guard
// does not cover. RegisterDaemonTools fans out to session/store/publish/
// error_queue, so those are covered transitively.
//
// Registration infers schemas from the Go types alone and never invokes a
// handler, so a DaemonTools with no daemon connection is fine.
func registerAllTools(t *testing.T) *mcp.Server {
	t.Helper()

	dt := NewDaemonTools(daemonclient.AutoStartConfig{}, "test")
	server := mcp.NewServer(&mcp.Implementation{Name: "agnt", Version: "test"}, nil)

	RegisterDaemonTools(server, dt)
	RegisterDaemonManagementTool(server, dt)
	RegisterTunnelTool(server, dt)
	RegisterBrowserTool(server, dt)
	RegisterAutomationTool(server, dt)
	RegisterResponsiveAuditTool(server, dt)
	RegisterAPIAuditTool(server, dt)
	RegisterLoadingAuditTool(server, dt)
	RegisterGetIncidentsTool(server, dt)
	RegisterReplaytestTool(server, license.NewManager(), dt.ReplaytestLogClient)
	RegisterChannelReplyTool(server, dt)
	RegisterWalkthroughTool(server, dt)

	return server
}
