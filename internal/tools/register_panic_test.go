package tools

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/go-sdk/mcp"
)

// TestRegisterMCPTools_NoSchemaPanic guards against malformed jsonschema struct
// tags that make mcp.AddTool panic at registration time.
//
// Regression: v0.13.9–v0.13.12 shipped `jsonschema:"enum=error,enum=warning,..."`
// tags on ErrorQueueInput.Severity and ChannelReplyInput.Severity. The vendored
// google/jsonschema-go rejects any tag whose first whitespace-delimited token
// contains '=' (disallowedPrefixRegexp = ^[^ \t\n]*=), and mcp.AddTool panics on
// that error. Because tools register sequentially during `agnt mcp` startup, the
// first offending tool aborted the entire MCP server boot — the client saw
// `initialize: EOF` while the daemon itself stayed healthy. Fixed in the commit
// that added this test.
//
// AddTool infers the input schema purely from the Go Input type, so registration
// never touches the daemon connection or handler dependencies — a nil ProxyManager
// is fine here, the handlers are never invoked.
func TestRegisterMCPTools_NoSchemaPanic(t *testing.T) {
	dt := NewDaemonTools(daemon.AutoStartConfig{}, "test")

	regs := []struct {
		name string
		fn   func(*mcp.Server)
	}{
		{"daemon", func(s *mcp.Server) { RegisterDaemonTools(s, dt) }},
		{"daemon_management", func(s *mcp.Server) { RegisterDaemonManagementTool(s, dt) }},
		{"tunnel", func(s *mcp.Server) { RegisterTunnelTool(s, dt) }},
		{"browser", func(s *mcp.Server) { RegisterBrowserTool(s, dt) }},
		{"automation", func(s *mcp.Server) { RegisterAutomationTool(s, dt) }},
		{"responsive_audit", func(s *mcp.Server) { RegisterResponsiveAuditTool(s, dt) }},
		{"get_incidents", func(s *mcp.Server) { RegisterGetIncidentsTool(s, dt) }},
		{"channel_reply", func(s *mcp.Server) { RegisterChannelReplyTool(s, dt) }},
		{"error_queue", func(s *mcp.Server) { RegisterErrorQueueTool(s, dt) }},
		{"store", func(s *mcp.Server) { RegisterStoreTool(s, dt) }},
		{"session", func(s *mcp.Server) { RegisterSessionTool(s, dt) }},
		// get_errors, detect, proc, run, proxy, proxylog and currentpage are
		// registered by RegisterDaemonTools (the "daemon" case above), so their
		// schemas are already exercised by this guard.
	}

	for _, r := range regs {
		t.Run(r.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("Register%s panicked during MCP tool registration (malformed jsonschema tag?): %v", r.name, p)
				}
			}()
			s := mcp.NewServer(&mcp.Implementation{Name: "agnt", Version: "test"}, nil)
			r.fn(s)
		})
	}
}
