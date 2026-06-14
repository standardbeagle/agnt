package proxy

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/standardbeagle/agnt/internal/debug"
)

// chaosStateMessage builds the chaos_state push payload from live engine state.
func (ps *ProxyServer) chaosStateMessage() map[string]interface{} {
	return map[string]interface{}{
		"type":    "chaos_state",
		"payload": ps.chaosEngine.Snapshot(),
	}
}

// BroadcastChaosState pushes the current chaos engine state to all connected
// browser clients. Wired to ChaosEngine.onChange so MCP/hub-driven changes
// reach the indicator UI too. Returns the number of clients reached.
func (ps *ProxyServer) BroadcastChaosState() int {
	messageBytes, err := json.Marshal(ps.chaosStateMessage())
	if err != nil {
		debug.Error("proxy", "BroadcastChaosState: marshal failed: %v", err)
		return 0
	}
	return ps.broadcastRaw(messageBytes)
}

// handleChaosRequest processes chaos control requests from the browser
// indicator panel. It acts directly on this proxy's chaos engine — no daemon
// round trip. Responses go back as chaos_response keyed by request_id; state
// mutations additionally trigger a chaos_state broadcast via the engine's
// onChange hook.
func (ps *ProxyServer) handleChaosRequest(conn wsJSONWriter, data map[string]interface{}) {
	requestID := getStringField(data, "request_id")
	action := getStringField(data, "action")
	params := getMapField(data, "params")

	sendResponse := func(result interface{}, errMsg string) {
		resp := map[string]interface{}{
			"type":       "chaos_response",
			"request_id": requestID,
		}
		if errMsg != "" {
			resp["error"] = errMsg
		} else {
			resp["result"] = result
		}
		if wErr := conn.WriteJSON(resp); wErr != nil {
			debug.Error("proxy", "chaos_response: failed to send response to client: %v", wErr)
		}
	}

	engine := ps.chaosEngine

	switch action {
	case "status":
		sendResponse(engine.Snapshot(), "")

	case "enable":
		engine.Enable()
		sendResponse(engine.Snapshot(), "")

	case "disable":
		engine.Disable()
		sendResponse(engine.Snapshot(), "")

	case "preset":
		name := getStringField(params, "name")
		if name == "" {
			sendResponse(nil, "name is required")
			return
		}
		config := GetPreset(name)
		if config == nil {
			sendResponse(nil, fmt.Sprintf("unknown preset: %s", name))
			return
		}
		if err := engine.SetConfig(config); err != nil {
			sendResponse(nil, err.Error())
			return
		}
		sendResponse(engine.Snapshot(), "")

	case "clear":
		engine.Clear()
		sendResponse(engine.Snapshot(), "")

	case "add_rule":
		ruleData, err := json.Marshal(params["rule"])
		if err != nil {
			sendResponse(nil, "invalid rule")
			return
		}
		var rule ChaosRule
		if err := json.Unmarshal(ruleData, &rule); err != nil {
			sendResponse(nil, fmt.Sprintf("invalid rule: %v", err))
			return
		}
		if rule.ID == "" || rule.Type == "" {
			sendResponse(nil, "rule id and type are required")
			return
		}
		if err := engine.AddRule(&rule); err != nil {
			sendResponse(nil, err.Error())
			return
		}
		sendResponse(engine.Snapshot(), "")

	case "remove_rule":
		ruleID := getStringField(params, "rule_id")
		if ruleID == "" {
			sendResponse(nil, "rule_id is required")
			return
		}
		if !engine.RemoveRule(ruleID) {
			sendResponse(nil, fmt.Sprintf("rule not found: %s", ruleID))
			return
		}
		sendResponse(engine.Snapshot(), "")

	case "toggle_rule":
		ruleID := getStringField(params, "rule_id")
		if ruleID == "" {
			sendResponse(nil, "rule_id is required")
			return
		}
		enabled := getBoolField(params, "enabled")
		var found bool
		if enabled {
			found = engine.EnableRule(ruleID)
		} else {
			found = engine.DisableRule(ruleID)
		}
		if !found {
			sendResponse(nil, fmt.Sprintf("rule not found: %s", ruleID))
			return
		}
		sendResponse(engine.Snapshot(), "")

	case "reset_stats":
		engine.ResetStats()
		sendResponse(engine.Snapshot(), "")

	case "set_swallow_detect":
		engine.SetSwallowDetect(getBoolField(params, "enabled"))
		sendResponse(engine.Snapshot(), "")

	case "list_presets":
		names := ListPresets()
		sort.Strings(names)
		presets := make([]map[string]interface{}, 0, len(names))
		for _, name := range names {
			preset := ChaosPresets[name]
			ruleNames := make([]string, 0, len(preset.Rules))
			for _, r := range preset.Rules {
				ruleNames = append(ruleNames, r.Name)
			}
			presets = append(presets, map[string]interface{}{
				"name":  name,
				"rules": ruleNames,
			})
		}
		sendResponse(map[string]interface{}{"presets": presets}, "")

	default:
		sendResponse(nil, fmt.Sprintf("unknown chaos action: %s", action))
	}
}
