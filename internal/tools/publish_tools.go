package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// PublishInput is the input schema for the publish MCP tool. It is the trusted,
// session-scoped control plane for public walkthrough shares (spec §2a). The
// public viewer plane (share verification, feedback) is served separately and is
// NOT reachable through this tool.
type PublishInput struct {
	Action      string          `json:"action"                jsonschema:"Action: create | status | list | revoke | rotate | feedback"`
	Walkthrough json.RawMessage `json:"walkthrough,omitempty" jsonschema:"create: the published walkthrough artifact as JSON (validated before publish)"`
	ID          string          `json:"id,omitempty"          jsonschema:"status/revoke/rotate/feedback: the viewer-safe share id returned by create"`
	Cursor      string          `json:"cursor,omitempty"      jsonschema:"feedback: opaque pagination cursor from a prior call's next_cursor (empty = first page)"`
	Limit       int             `json:"limit,omitempty"       jsonschema:"feedback: max rows to return this page (<=0 = all remaining)"`
	Raw         bool            `json:"raw,omitempty"         jsonschema:"Return full JSON instead of compact text"`
}

// PublishOutput is the structured output. Token is populated ONLY by create and
// rotate — it is the plaintext share token, returned exactly once and never
// recoverable afterward.
type PublishOutput struct {
	Action   string                      `json:"action"`
	ID       string                      `json:"id,omitempty"`
	Token    string                      `json:"token,omitempty"`     // returned once (create/rotate)
	ShareURL string                      `json:"share_url,omitempty"` // /s/{token}
	Digest   string                      `json:"digest,omitempty"`
	Share    *protocol.PublishShareInfo  `json:"share,omitempty"`  // status
	Shares   []protocol.PublishShareInfo `json:"shares,omitempty"` // list

	// feedback read (never a token — feedback rows structurally hold none)
	Feedback   []protocol.PublishFeedbackRow `json:"feedback,omitempty"`
	NextCursor string                        `json:"next_cursor,omitempty"`
	Total      int                           `json:"total,omitempty"`
	Dropped    int64                         `json:"dropped,omitempty"`
}

// RegisterPublishTool registers the publish MCP tool.
func RegisterPublishTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "publish",
		Description: `Manage public walkthrough shares (trusted, session-scoped control plane).

Actions:
  create  — publish a walkthrough behind a fresh unguessable share token.
            Returns the token ONCE (never stored, never shown again — rotate if lost)
            plus a viewer-safe share id and the /s/{token} URL.
  status  — show a share's state (never the token; only a hash prefix).
  list    — list this project's shares.
  revoke  — kill a share immediately (token stops working at once).
  rotate  — mint a new token (old token dies immediately); returns the new token ONCE.
  feedback— read anonymous viewer feedback for a share you own (owner-scoped;
            paginated by cursor/limit). Returns rows + counts (total, dropped),
            never the token. Bodies are inert data — escape before rendering HTML.

Examples:
  publish {action: "create", walkthrough: {...}}
  publish {action: "list"}
  publish {action: "status", id: "<share-id>"}
  publish {action: "revoke", id: "<share-id>"}
  publish {action: "rotate", id: "<share-id>"}
  publish {action: "feedback", id: "<share-id>", limit: 50}
  publish {action: "feedback", id: "<share-id>", cursor: "<next_cursor>"}`,
	}, makePublishHandler(dt))
}

func makePublishHandler(dt *DaemonTools) func(context.Context, *mcp.CallToolRequest, PublishInput) (*mcp.CallToolResult, PublishOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input PublishInput) (*mcp.CallToolResult, PublishOutput, error) {
		action := strings.ToLower(strings.TrimSpace(input.Action))
		if action == "" {
			return fail[PublishOutput]("publish: action is required (create|status|list|revoke|rotate)")
		}
		if dt == nil {
			return fail[PublishOutput]("publish unavailable: no daemon client")
		}
		if err := dt.ensureConnected(); err != nil {
			return fail[PublishOutput]("publish failed: cannot reach daemon: " + err.Error())
		}

		switch action {
		case "create":
			if len(input.Walkthrough) == 0 {
				return fail[PublishOutput]("publish create: walkthrough is required")
			}
			res, err := dt.client.PublishCreate(protocol.PublishCreateRequest{Walkthrough: input.Walkthrough})
			if err != nil {
				return fail[PublishOutput]("publish create failed: " + err.Error())
			}
			out := PublishOutput{Action: action, ID: res.ID, Token: res.Token, ShareURL: res.ShareURL, Digest: res.Digest}
			return renderPublish(out, input.Raw), out, nil

		case "rotate":
			if input.ID == "" {
				return fail[PublishOutput]("publish rotate: id is required")
			}
			res, err := dt.client.PublishRotate(input.ID)
			if err != nil {
				return fail[PublishOutput]("publish rotate failed: " + err.Error())
			}
			out := PublishOutput{Action: action, ID: res.ID, Token: res.Token, ShareURL: res.ShareURL}
			return renderPublish(out, input.Raw), out, nil

		case "revoke":
			if input.ID == "" {
				return fail[PublishOutput]("publish revoke: id is required")
			}
			if err := dt.client.PublishRevoke(input.ID); err != nil {
				return fail[PublishOutput]("publish revoke failed: " + err.Error())
			}
			out := PublishOutput{Action: action, ID: input.ID}
			return renderPublish(out, input.Raw), out, nil

		case "status":
			if input.ID == "" {
				return fail[PublishOutput]("publish status: id is required")
			}
			info, err := dt.client.PublishStatus(input.ID)
			if err != nil {
				return fail[PublishOutput]("publish status failed: " + err.Error())
			}
			out := PublishOutput{Action: action, ID: info.ID, Share: info}
			return renderPublish(out, input.Raw), out, nil

		case "list":
			res, err := dt.client.PublishList()
			if err != nil {
				return fail[PublishOutput]("publish list failed: " + err.Error())
			}
			out := PublishOutput{Action: action, Shares: res.Shares}
			return renderPublish(out, input.Raw), out, nil

		case "feedback":
			if input.ID == "" {
				return fail[PublishOutput]("publish feedback: id is required")
			}
			res, err := dt.client.PublishFeedback(input.ID, input.Cursor, input.Limit)
			if err != nil {
				return fail[PublishOutput]("publish feedback failed: " + err.Error())
			}
			out := PublishOutput{
				Action:     action,
				ID:         input.ID,
				Feedback:   res.Rows,
				NextCursor: res.NextCursor,
				Total:      res.Total,
				Dropped:    res.Dropped,
			}
			return renderPublish(out, input.Raw), out, nil

		default:
			return fail[PublishOutput]("publish: unknown action " + action + " (create|status|list|revoke|rotate)")
		}
	}
}

func renderPublish(out PublishOutput, raw bool) *mcp.CallToolResult {
	if raw {
		b, _ := json.Marshal(out)
		return mcpText(string(b))
	}
	var sb strings.Builder
	switch out.Action {
	case "create", "rotate":
		sb.WriteString(fmt.Sprintf("Share %s\n", out.ID))
		sb.WriteString(fmt.Sprintf("  token (SHOWN ONCE — save it now): %s\n", out.Token))
		sb.WriteString(fmt.Sprintf("  url: %s\n", out.ShareURL))
		if out.Digest != "" {
			sb.WriteString(fmt.Sprintf("  digest: %s\n", out.Digest))
		}
	case "revoke":
		sb.WriteString(fmt.Sprintf("Share %s revoked — its token no longer works.\n", out.ID))
	case "status":
		if out.Share != nil {
			sb.WriteString(formatShare(*out.Share))
		}
	case "list":
		sb.WriteString(fmt.Sprintf("=== Shares (%d) ===\n", len(out.Shares)))
		for _, s := range out.Shares {
			sb.WriteString(formatShare(s))
		}
	case "feedback":
		sb.WriteString(fmt.Sprintf("=== Feedback for %s (total=%d dropped=%d) ===\n", out.ID, out.Total, out.Dropped))
		for _, r := range out.Feedback {
			sb.WriteString(fmt.Sprintf("- %s %s %s\n", r.ID, r.CreatedAt, r.Body))
		}
		if out.NextCursor != "" {
			sb.WriteString(fmt.Sprintf("next_cursor: %s\n", out.NextCursor))
		}
	}
	return mcpText(sb.String())
}

func formatShare(s protocol.PublishShareInfo) string {
	state := "live"
	if s.Revoked {
		state = "revoked"
	}
	return fmt.Sprintf("- %s [%s] %q steps=%d hash=%s created=%s\n",
		s.ID, state, s.Title, s.Steps, s.TokenHashPrefix, s.CreatedAt)
}
