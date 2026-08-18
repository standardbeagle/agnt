package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Walkthrough json.RawMessage `json:"walkthrough,omitempty" jsonschema:"create: the published walkthrough artifact as JSON - steps plus an optional variantSet whose ops may include the raw-content ops setHTML/addStyle/addScript (validated before publish)"`
	Upstream    string          `json:"upstream,omitempty"    jsonschema:"create: https URL of the live origin this walkthrough is demonstrated against; equivalent to walkthrough.upstream.url and rejected if it contradicts one already present"`
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

	// ServeStores surfaces `agnt publish serve` stores the daemon does NOT manage
	// (list only). serve keeps its own store, so its shares are otherwise invisible
	// here; silence about them would violate the Silent Failure Prohibition.
	ServeStores []ServeStoreInfo `json:"serve_stores,omitempty"`
}

// ServeStoreInfo points an operator at a `publish serve` store the daemon does
// not own. It carries no share contents — just where the store is and which
// folder it serves — so the operator can read it with `agnt publish feedback`.
type ServeStoreInfo struct {
	Path string `json:"path"`          // the store directory
	Dir  string `json:"dir,omitempty"` // the served folder, from the store metadata
	Addr string `json:"addr,omitempty"`
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
            Accepts a live origin to demo against — either walkthrough.upstream.url
            or the top-level "upstream" field (naming two different origins is
            rejected) — and variant ops including the raw-content ops setHTML,
            addStyle, and addScript. Publisher script runs only because its
            sha256 is pinned in the served revision's CSP script-src.
            The /s/{token} URL it returns is a PATH: prefix it with the origin
            serving the public plane, or use "agnt publish serve --dir" which
            prints whole URLs (and folds in a tunnel origin).
  status  — show a share's state (never the token; only a hash prefix).
  list    — list this project's shares.
  revoke  — kill a share immediately (token stops working at once).
  rotate  — mint a new token (old token dies immediately); returns the new token ONCE.
  feedback— read anonymous viewer feedback for a share you own (owner-scoped;
            paginated by cursor/limit). Returns rows + counts (total, dropped),
            never the token. Bodies are inert data — escape before rendering HTML.

Examples:
  publish {action: "create", walkthrough: {...}}
  publish {action: "create", walkthrough: {...}, upstream: "https://shop.example.com/cart"}
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
			artifact, err := mergeUpstream(input.Walkthrough, input.Upstream)
			if err != nil {
				return fail[PublishOutput]("publish create: " + err.Error())
			}
			res, err := dt.client.PublishCreate(protocol.PublishCreateRequest{Walkthrough: artifact})
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
			out := PublishOutput{Action: action, Shares: res.Shares, ServeStores: discoverServeStores()}
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

// mergeUpstream folds the optional `upstream` input into the walkthrough
// artifact, so the manual control-plane path can name the live origin without
// hand-assembling the nested object. An empty upstream returns the artifact
// byte-for-byte unchanged, which is what keeps this a no-op for every existing
// caller.
//
// A contradiction between the two spellings is a REJECTION, never a silent
// precedence rule: the origin decides what a viewer's browser actually loads, so
// quietly preferring one of two conflicting values is exactly the kind of
// invisible decision that ends with a demo pointed at the wrong site. Identical
// values are accepted as the harmless restatement they are.
//
// The merged artifact is still fully validated daemon-side by the P2 validators
// (DecodePublishedWalkthrough) before anything is stored — this function is a
// convenience over the JSON shape, not a trust boundary.
func mergeUpstream(walkthrough json.RawMessage, upstream string) (json.RawMessage, error) {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return walkthrough, nil
	}
	var artifact map[string]json.RawMessage
	if err := json.Unmarshal(walkthrough, &artifact); err != nil {
		return nil, fmt.Errorf("walkthrough must be a JSON object to merge upstream into: %w", err)
	}
	if existing, ok := artifact["upstream"]; ok {
		var cur struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(existing, &cur); err != nil {
			return nil, fmt.Errorf("walkthrough.upstream is not an object: %w", err)
		}
		if cur.URL != upstream {
			return nil, fmt.Errorf("upstream %q contradicts walkthrough.upstream.url %q — pass one of them, not two different origins", upstream, cur.URL)
		}
		return walkthrough, nil
	}
	merged, err := json.Marshal(map[string]string{"url": upstream})
	if err != nil {
		return nil, fmt.Errorf("encode upstream: %w", err)
	}
	artifact["upstream"] = merged
	// Encode with HTML escaping OFF: json.Marshal would rewrite a setHTML op's
	// "<b>" as "<b>". Decoding is unaffected, but needlessly mangling
	// bytes we are only passing through makes the artifact harder to read in a
	// log or an error message.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(artifact); err != nil {
		return nil, fmt.Errorf("re-encode walkthrough: %w", err)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
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
		if len(out.ServeStores) > 0 {
			sb.WriteString(fmt.Sprintf("=== Serve stores (%d) — NOT managed by the daemon; from 'agnt publish serve' ===\n", len(out.ServeStores)))
			for _, ss := range out.ServeStores {
				if ss.Dir != "" {
					sb.WriteString(fmt.Sprintf("- %s  (serving %s)\n", ss.Path, ss.Dir))
				} else {
					sb.WriteString(fmt.Sprintf("- %s\n", ss.Path))
				}
				sb.WriteString(fmt.Sprintf("  read its feedback: agnt publish feedback --store %s\n", ss.Path))
			}
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

// serveStoreMetaFile mirrors the constant in cmd/agnt/publish_serve.go: the
// discovery metadata a serve run drops into its own store dir. Kept as a plain
// JSON contract (not a shared type) so this package need not import package main.
const serveStoreMetaFile = "serve.json"

// discoverServeStores enumerates `agnt publish serve` stores under the user cache
// and reports each one — the daemon does not manage them, so they are otherwise
// invisible to `publish list`. Best-effort and read-only: a missing cache dir or
// an unreadable entry yields fewer results, never an error, so the daemon's own
// share list never fails because a serve store is malformed.
func discoverServeStores() []ServeStoreInfo {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(base, "agnt", "publish-serve"))
	if err != nil {
		return nil
	}
	var out []ServeStoreInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		storeDir := filepath.Join(base, "agnt", "publish-serve", e.Name())
		info := ServeStoreInfo{Path: storeDir}
		if data, err := os.ReadFile(filepath.Join(storeDir, serveStoreMetaFile)); err == nil {
			var m struct {
				Dir  string `json:"dir"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal(data, &m) == nil {
				info.Dir = m.Dir
				info.Addr = m.Addr
			}
		}
		out = append(out, info)
	}
	return out
}

func formatShare(s protocol.PublishShareInfo) string {
	state := "live"
	if s.Revoked {
		state = "revoked"
	}
	return fmt.Sprintf("- %s [%s] %q steps=%d hash=%s created=%s\n",
		s.ID, state, s.Title, s.Steps, s.TokenHashPrefix, s.CreatedAt)
}
