package proxy

import (
	"fmt"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// handleSessionRequest processes session API requests from the browser.
// It creates a daemon client, executes the session operation, and sends the response back.
func (ps *ProxyServer) handleSessionRequest(conn *websocket.Conn, data map[string]interface{}) {
	requestID := getStringField(data, "request_id")
	action := getStringField(data, "action")
	params := getMapField(data, "params")

	// Helper to send response
	sendResponse := func(result interface{}, errMsg string) {
		resp := map[string]interface{}{
			"type":       "session_response",
			"request_id": requestID,
		}
		if errMsg != "" {
			resp["error"] = errMsg
		} else {
			resp["result"] = result
		}
		if wErr := conn.WriteJSON(resp); wErr != nil {
			debug.Error("proxy", "session_response: failed to send response to client: %v", wErr)
		}
	}

	// Check if session client factory is configured
	if ps.sessionClientFactory == nil {
		sendResponse(nil, "session API not available: no session client factory configured")
		return
	}

	// Create session client for this request
	client, err := ps.sessionClientFactory()
	if err != nil {
		sendResponse(nil, fmt.Sprintf("failed to connect to daemon: %v", err))
		return
	}
	defer client.Close()

	// Execute the appropriate session action
	switch action {
	case "list":
		global := getBoolField(params, "global")
		dirFilter := protocol.DirectoryFilter{
			Directory: ps.Path,
			Global:    global,
		}
		result, err := client.SessionList(dirFilter)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "get":
		code := getStringField(params, "code")
		if code == "" {
			sendResponse(nil, "code is required")
			return
		}
		result, err := client.SessionGet(code)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "send":
		code := getStringField(params, "code")
		message := getStringField(params, "message")
		if code == "" {
			sendResponse(nil, "code is required")
			return
		}
		if message == "" {
			sendResponse(nil, "message is required")
			return
		}
		result, err := client.SessionSend(code, message)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "schedule":
		code := getStringField(params, "code")
		duration := getStringField(params, "duration")
		message := getStringField(params, "message")
		if code == "" {
			sendResponse(nil, "code is required")
			return
		}
		if duration == "" {
			sendResponse(nil, "duration is required")
			return
		}
		if message == "" {
			sendResponse(nil, "message is required")
			return
		}
		result, err := client.SessionSchedule(code, duration, message)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "tasks":
		global := getBoolField(params, "global")
		dirFilter := protocol.DirectoryFilter{
			Directory: ps.Path,
			Global:    global,
		}
		result, err := client.SessionTasks(dirFilter)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "cancel":
		taskID := getStringField(params, "task_id")
		if taskID == "" {
			sendResponse(nil, "task_id is required")
			return
		}
		err := client.SessionCancel(taskID)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(map[string]interface{}{"success": true}, "")
		}

	default:
		sendResponse(nil, fmt.Sprintf("unknown session action: %s", action))
	}
}

// handleStoreRequest processes store API requests from the browser.
// It creates a daemon client, executes the store operation, and sends the response back.
func (ps *ProxyServer) handleStoreRequest(conn *websocket.Conn, data map[string]interface{}) {
	requestID := getStringField(data, "request_id")
	action := getStringField(data, "action")
	params := getMapField(data, "params")

	// Helper to send response
	sendResponse := func(result interface{}, errMsg string) {
		resp := map[string]interface{}{
			"type":       "store_response",
			"request_id": requestID,
		}
		if errMsg != "" {
			resp["error"] = errMsg
		} else {
			resp["result"] = result
		}
		if wErr := conn.WriteJSON(resp); wErr != nil {
			debug.Error("proxy", "failed to send exec response: %v", wErr)
		}
	}

	// Check if session client factory is configured (we reuse it for store access)
	if ps.sessionClientFactory == nil {
		sendResponse(nil, "store API not available: no client factory configured")
		return
	}

	// Create client for this request
	client, err := ps.sessionClientFactory()
	if err != nil {
		sendResponse(nil, fmt.Sprintf("failed to connect to daemon: %v", err))
		return
	}
	defer client.Close()

	// Execute the appropriate store action
	switch action {
	case "get":
		scope := getStringField(params, "scope")
		scopeKey := getStringField(params, "scope_key")
		key := getStringField(params, "key")

		if scope == "" {
			sendResponse(nil, "scope is required")
			return
		}
		if key == "" {
			sendResponse(nil, "key is required")
			return
		}

		result, err := client.StoreGet(protocol.StoreGetRequest{
			Scope:    scope,
			ScopeKey: scopeKey,
			Key:      key,
		})
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "set":
		scope := getStringField(params, "scope")
		scopeKey := getStringField(params, "scope_key")
		key := getStringField(params, "key")
		value := params["value"]
		metadata := getMapField(params, "metadata")

		if scope == "" {
			sendResponse(nil, "scope is required")
			return
		}
		if key == "" {
			sendResponse(nil, "key is required")
			return
		}
		if value == nil {
			sendResponse(nil, "value is required")
			return
		}

		err = client.StoreSet(protocol.StoreSetRequest{
			Scope:    scope,
			ScopeKey: scopeKey,
			Key:      key,
			Value:    value,
			Metadata: metadata,
		})
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(map[string]interface{}{"success": true}, "")
		}

	case "delete":
		scope := getStringField(params, "scope")
		scopeKey := getStringField(params, "scope_key")
		key := getStringField(params, "key")

		if scope == "" {
			sendResponse(nil, "scope is required")
			return
		}
		if key == "" {
			sendResponse(nil, "key is required")
			return
		}

		err = client.StoreDelete(protocol.StoreDeleteRequest{
			Scope:    scope,
			ScopeKey: scopeKey,
			Key:      key,
		})
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(map[string]interface{}{"success": true}, "")
		}

	case "list":
		scope := getStringField(params, "scope")
		scopeKey := getStringField(params, "scope_key")

		if scope == "" {
			sendResponse(nil, "scope is required")
			return
		}

		result, err := client.StoreList(protocol.StoreListRequest{
			Scope:    scope,
			ScopeKey: scopeKey,
		})
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "clear":
		scope := getStringField(params, "scope")
		scopeKey := getStringField(params, "scope_key")

		if scope == "" {
			sendResponse(nil, "scope is required")
			return
		}

		err = client.StoreClear(protocol.StoreClearRequest{
			Scope:    scope,
			ScopeKey: scopeKey,
		})
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(map[string]interface{}{"success": true}, "")
		}

	case "getAll":
		scope := getStringField(params, "scope")
		scopeKey := getStringField(params, "scope_key")

		if scope == "" {
			sendResponse(nil, "scope is required")
			return
		}

		result, err := client.StoreGetAll(protocol.StoreGetAllRequest{
			Scope:    scope,
			ScopeKey: scopeKey,
		})
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	default:
		sendResponse(nil, fmt.Sprintf("unknown store action: %s", action))
	}
}
