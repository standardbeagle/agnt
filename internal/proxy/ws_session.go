package proxy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// The browser session/store API (scripts/session.js, scripts/store.js).
//
// Scope is pinned by the server, never chosen by the browser. A proxy acts for
// its own project: the SessionClient the daemon installs is bound to that
// project and refuses anything outside it, and the handlers here refuse — out
// loud, never by quietly re-scoping — a request that asks for more (`global`).
// The store's `scope`/`scope_key` are a different thing: namespaces INSIDE the
// project's store (global / folder / page, keyed by a hashed URL), so they are
// validated and bounded rather than refused.
//
// Both APIs are refused to an untrusted caller before they get here
// (wsConnState.refusePrivileged): a session request can put text into an
// agent's terminal and a store request writes persistent project data.

// Bounds on what one browser request may carry or receive. The hub's SESSION
// and STORE verbs set none of their own (they are bounded only by the control
// socket's frame size), so these are this surface's limits; without them the
// only bound was the 16 MiB WebSocket frame cap.
const (
	maxStoreKeyBytes       = 256
	maxStoreScopeKeyBytes  = 2048
	maxStoreValueBytes     = 256 << 10
	maxStoreMetadataBytes  = 16 << 10
	maxSessionMessageBytes = 16 << 10
	// maxControlResponseBytes caps one session/store reply (a getAll of a
	// large scope, a long task list). An over-cap result is refused whole,
	// never truncated: half a JSON document is not a smaller answer.
	maxControlResponseBytes = 1 << 20
)

// globalScopeRefusal is the reply to a browser request that asks for more
// than this proxy's project.
const globalScopeRefusal = "global scope is not available from the browser: this API is scoped to the proxy's project"

// writeControlResponse sends one session/store reply, bounded by
// maxControlResponseBytes.
func writeControlResponse(conn wsJSONWriter, respType, requestID string, result interface{}, errMsg string) {
	resp := map[string]interface{}{
		"type":       respType,
		"request_id": requestID,
	}
	if errMsg == "" {
		encoded, err := json.Marshal(result)
		switch {
		case err != nil:
			errMsg = fmt.Sprintf("failed to encode result: %v", err)
		case len(encoded) > maxControlResponseBytes:
			errMsg = fmt.Sprintf("result is %d bytes, over the %d byte limit for one response; narrow the request", len(encoded), maxControlResponseBytes)
		default:
			resp["result"] = json.RawMessage(encoded)
		}
	}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	if wErr := conn.WriteJSON(resp); wErr != nil {
		debug.Error("proxy", "%s: failed to send response to client: %v", respType, wErr)
	}
}

// refuseGlobalScope reports whether the browser asked for global scope, and
// records the refusal as a diagnostic so it reaches the agent.
func (ps *ProxyServer) refuseGlobalScope(api, action string, params map[string]interface{}) bool {
	if !getBoolField(params, "global") {
		return false
	}
	ps.logger.LogDiagnostic(ProxyDiagnostic{
		Timestamp: time.Now(),
		Level:     DiagnosticWarning,
		Category:  "proxy",
		Event:     "ws_scope_refused",
		Message:   fmt.Sprintf("proxy %s refused a browser %s %q request for global scope: the browser API is scoped to the proxy's project", ps.ID, api, action),
		Data:      map[string]any{"proxy_id": ps.ID, "api": api, "action": action},
	})
	return true
}

// handleSessionRequest processes session API requests from the browser.
// It creates a daemon client, executes the session operation, and sends the response back.
func (ps *ProxyServer) handleSessionRequest(conn wsJSONWriter, data map[string]interface{}) {
	requestID := getStringField(data, "request_id")
	action := getStringField(data, "action")
	params := getMapField(data, "params")

	// Helper to send response
	sendResponse := func(result interface{}, errMsg string) {
		writeControlResponse(conn, "session_response", requestID, result, errMsg)
	}

	// Scope is the proxy's project. A request for more is refused, not
	// narrowed: the caller asked for something it will not get.
	if ps.refuseGlobalScope("session", action, params) {
		sendResponse(nil, globalScopeRefusal)
		return
	}

	// Create session client for this request
	client, ok, err := ps.sessionClient()
	if !ok {
		sendResponse(nil, "session API not available: no session client factory configured")
		return
	}
	if err != nil {
		sendResponse(nil, fmt.Sprintf("failed to connect to daemon: %v", err))
		return
	}
	defer client.Close()

	// Execute the appropriate session action
	switch action {
	case "list":
		dirFilter := protocol.DirectoryFilter{Directory: ps.Path}
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
		if len(message) > maxSessionMessageBytes {
			sendResponse(nil, fmt.Sprintf("message is %d bytes, over the %d byte limit", len(message), maxSessionMessageBytes))
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
		if len(message) > maxSessionMessageBytes {
			sendResponse(nil, fmt.Sprintf("message is %d bytes, over the %d byte limit", len(message), maxSessionMessageBytes))
			return
		}
		result, err := client.SessionSchedule(code, duration, message)
		if err != nil {
			sendResponse(nil, err.Error())
		} else {
			sendResponse(result, "")
		}

	case "tasks":
		dirFilter := protocol.DirectoryFilter{Directory: ps.Path}
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

// validateStoreParams bounds the browser-supplied parts of a store request,
// returning the refusal message or "". scope names one of the store's three
// in-project namespaces (the store itself rejects any other value); scope_key
// is hashed into a file name there, so it cannot address anything outside the
// project's store — it only needs a length bound.
func validateStoreParams(params map[string]interface{}) string {
	if n := len(getStringField(params, "key")); n > maxStoreKeyBytes {
		return fmt.Sprintf("key is %d bytes, over the %d byte limit", n, maxStoreKeyBytes)
	}
	if n := len(getStringField(params, "scope_key")); n > maxStoreScopeKeyBytes {
		return fmt.Sprintf("scope_key is %d bytes, over the %d byte limit", n, maxStoreScopeKeyBytes)
	}
	if value, ok := params["value"]; ok && value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("value cannot be encoded: %v", err)
		}
		if len(encoded) > maxStoreValueBytes {
			return fmt.Sprintf("value is %d bytes, over the %d byte limit", len(encoded), maxStoreValueBytes)
		}
	}
	if metadata := getMapField(params, "metadata"); len(metadata) > 0 {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Sprintf("metadata cannot be encoded: %v", err)
		}
		if len(encoded) > maxStoreMetadataBytes {
			return fmt.Sprintf("metadata is %d bytes, over the %d byte limit", len(encoded), maxStoreMetadataBytes)
		}
	}
	return ""
}

// handleStoreRequest processes store API requests from the browser.
// It creates a daemon client, executes the store operation, and sends the response back.
func (ps *ProxyServer) handleStoreRequest(conn wsJSONWriter, data map[string]interface{}) {
	requestID := getStringField(data, "request_id")
	action := getStringField(data, "action")
	params := getMapField(data, "params")

	// Helper to send response
	sendResponse := func(result interface{}, errMsg string) {
		writeControlResponse(conn, "store_response", requestID, result, errMsg)
	}

	if ps.refuseGlobalScope("store", action, params) {
		sendResponse(nil, globalScopeRefusal)
		return
	}
	// Bounds first, before a client is built or anything is read or written.
	if errMsg := validateStoreParams(params); errMsg != "" {
		sendResponse(nil, errMsg)
		return
	}

	// Create client for this request (the session client factory also serves
	// the store)
	client, ok, err := ps.sessionClient()
	if !ok {
		sendResponse(nil, "store API not available: no client factory configured")
		return
	}
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
