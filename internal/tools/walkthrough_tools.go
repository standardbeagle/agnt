package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/standardbeagle/go-sdk/mcp"
)

// WalkthroughInput defines input for the walkthrough tool.
//
// The walkthrough tool lets a coding agent run a live demo of work just
// completed: it drives a floating, scrolling step list in the browser overlay
// that narrates state, highlights app elements, and advances by timer, by the
// user clicking the highlighted element, or by an app-state condition.
type WalkthroughInput struct {
	Action  string `json:"action" jsonschema:"Action: load, start, stop, next, prev, play, pause, status, list"`
	ProxyID string `json:"proxy_id,omitempty" jsonschema:"Target proxy ID (preferred)"`
	ID      string `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	// Script is the walkthrough definition (for load/start). JSON object:
	// {id, title, steps:[{title, body, target?, gesture?, advance:{type,...}}]}.
	// advance.type is one of: auto ({ms}), click-target (needs target), or
	// wait ({when: url-contains|element-present|element-visible, value}).
	// gesture is one of: hover, click, scroll, drag (needs target) — renders an
	// animated affordance over the highlight until the step advances.
	// gesture_label (needs gesture, <=64 chars) labels that affordance with the
	// concrete action instead of a generic verb phrase.
	Script   any    `json:"script,omitempty" jsonschema:"Walkthrough script object (load/start): {id,title,steps:[{title,body,target?,gesture?,gesture_label?,advance:{type,ms?,when?,value?}}]}"`
	ScriptID string `json:"script_id,omitempty" jsonschema:"For start: id of an already-loaded script to run"`
	Mode     string `json:"mode,omitempty" jsonschema:"For start: auto (auto-play, default) or manual (user steps with next/prev)"`
}

// WalkthroughOutput defines output for the walkthrough tool.
type WalkthroughOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	// Result carries the browser-side return value (status/list payloads).
	Result string `json:"result,omitempty"`
}

// RegisterWalkthroughTool registers the walkthrough MCP tool. Requires daemon
// mode (dt non-nil); all actions are driven through the proxy exec path against
// the chrome-frame walkthrough host.
func RegisterWalkthroughTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "walkthrough",
		Description: `Run a live demo of work just completed in the browser overlay. Pops a floating, scrolling step list that narrates each step, highlights the matching app element, and advances by auto-timer, by the user clicking the highlighted element, or by waiting for an app-state condition. Next/Prev let the user step manually. Loaded scripts get a replay launcher in the overlay.

Actions:
- load:   register a script (shows a replay launcher; does not start)
- start:  begin playback (by inline script or script_id); mode auto|manual
- stop:   end the active walkthrough and hide the panel
- next/prev: step manually
- play/pause: control auto-advance
- status: current step / mode / running
- list:   registered scripts

Script shape:
  {id, title, steps:[{title, body, target?, gesture?, gesture_label?, advance:{type, ...}}]}
  advance.type:
    auto         {ms}        show ms then advance (default 5000)
    click-target            advance when the user clicks the highlighted target
    wait         {when,value} when: url-contains | element-present | element-visible
  gesture (optional, needs target): hover | click | scroll | drag
    Renders the matching animated affordance over the highlight; auto-dismisses
    when the step advances. The active step's narration reads through as it lands.
  gesture_label (optional, needs gesture, <=64 chars)
    Text under the affordance. Name the concrete action ("Click to open your
    cart", "Drag the handle to reorder") — omitted, it falls back to a generic
    verb phrase ("Click here").

Examples:
  walkthrough {action:"start", proxy_id:"dev", script:{id:"demo", title:"New checkout", steps:[
    {title:"Open cart", body:"Click the cart to begin.", target:"#cart-btn", gesture:"click", gesture_label:"Click to open your cart", advance:{type:"click-target"}},
    {title:"Totals", body:"Order total updates live.", target:".order-total", advance:{type:"auto", ms:4000}},
    {title:"Confirm", body:"You land on the confirm page.", advance:{type:"wait", when:"url-contains", value:"/confirm"}}
  ]}}
  walkthrough {action:"status", proxy_id:"dev"}
  walkthrough {action:"next", proxy_id:"dev"}`,
	}, dt.makeWalkthroughHandler())
}

func (dt *DaemonTools) makeWalkthroughHandler() func(context.Context, *mcp.CallToolRequest, WalkthroughInput) (*mcp.CallToolResult, WalkthroughOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input WalkthroughInput) (*mcp.CallToolResult, WalkthroughOutput, error) {
		input.ProxyID = pickProxyID(input.ID, input.ProxyID)

		code, err := buildWalkthroughExec(input)
		if err != nil {
			return fail[WalkthroughOutput](err.Error())
		}

		if input.ProxyID == "" {
			return fail[WalkthroughOutput]("proxy_id required (or `id` alias)")
		}
		if err := dt.ensureConnected(); err != nil {
			return fail[WalkthroughOutput](err.Error())
		}

		result, err := dt.client.ProxyExec(input.ProxyID, code)
		if err != nil {
			return fail[WalkthroughOutput](fmt.Sprintf("proxy exec failed: %v", err))
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fail[WalkthroughOutput](fmt.Sprintf("walkthrough failed: %s", errMsg))
		}

		resultStr := getString(result, "result")
		return nil, WalkthroughOutput{
			Success: true,
			Message: fmt.Sprintf("walkthrough %s on proxy %q", input.Action, input.ProxyID),
			Result:  resultStr,
		}, nil
	}
}

// buildWalkthroughExec builds the JS snippet for a walkthrough action. The
// snippet calls window.__devtool.walkthrough.<action>(...), which (running in
// the active content frame) forwards to the chrome-frame walkthrough host.
func buildWalkthroughExec(input WalkthroughInput) (string, error) {
	wt := "window.__devtool && window.__devtool.walkthrough"
	guard := func(call string) string {
		return fmt.Sprintf("(function(){var w=%s; if(!w){return JSON.stringify({error:'walkthrough not available'});} return JSON.stringify(%s);})()", wt, call)
	}

	// Script arrives as a decoded object (any); re-marshal to a JSON literal for
	// the exec snippet. Empty when omitted.
	var scriptJSON []byte
	if input.Script != nil {
		b, err := json.Marshal(input.Script)
		if err != nil {
			return "", fmt.Errorf("invalid script: %w", err)
		}
		scriptJSON = b
	}

	switch input.Action {
	case "load":
		if len(scriptJSON) == 0 {
			return "", fmt.Errorf("script required for load")
		}
		return guard(fmt.Sprintf("w.load(%s)", string(scriptJSON))), nil
	case "start":
		mode := "auto"
		if input.Mode == "manual" {
			mode = "manual"
		} else if input.Mode != "" && input.Mode != "auto" {
			return "", fmt.Errorf("invalid mode %q: must be auto or manual", input.Mode)
		}
		opts := fmt.Sprintf("{mode:%q}", mode)
		if len(scriptJSON) > 0 {
			return guard(fmt.Sprintf("w.start(%s, %s)", string(scriptJSON), opts)), nil
		}
		if input.ScriptID != "" {
			b, _ := json.Marshal(input.ScriptID)
			return guard(fmt.Sprintf("w.start(%s, %s)", string(b), opts)), nil
		}
		return "", fmt.Errorf("start requires script or script_id")
	case "stop":
		return guard("w.stop()"), nil
	case "next":
		return guard("w.next()"), nil
	case "prev":
		return guard("w.prev()"), nil
	case "play":
		return guard("w.play()"), nil
	case "pause":
		return guard("w.pause()"), nil
	case "status":
		return guard("w.status()"), nil
	case "list":
		return guard("w.list()"), nil
	default:
		return "", fmt.Errorf("unknown action %q: valid actions are load, start, stop, next, prev, play, pause, status, list", input.Action)
	}
}
