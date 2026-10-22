package daemon

import "github.com/standardbeagle/agnt/internal/incident"

// agentConnector is the one transport that reaches a session's agent. It is a
// fact about how the agent was started, not a setting a sender chooses:
//
//   - stdin: `agnt run` owns the agent's PTY, so a message is typed to it
//   - mcp:   the agent only holds an MCP connection (or a channel session with no
//     overlay), so a message is a notification from the MCP process
//
// Senders emit an agent message and stop there. The daemon stamps the message
// with the session's connector, and each front end acts only on its own:
// the overlay types stdin messages, the MCP process notifies for mcp ones.
// Exactly one connector per session means one event is one delivery.
type agentConnector string

const (
	connectorStdin agentConnector = incident.DigestViaStdin
	connectorMCP   agentConnector = incident.DigestViaMCP
)

// connectorFor resolves a session's connector from its registration. An empty
// overlay path or the "-" placeholder (channel sessions, mods) means there is no
// PTY to type into.
func connectorFor(s *Session) agentConnector {
	if s != nil && s.OverlayPath != "" && s.OverlayPath != "-" {
		return connectorStdin
	}
	return connectorMCP
}
