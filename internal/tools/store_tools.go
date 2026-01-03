package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StoreInput represents input for the store tool.
type StoreInput struct {
	Action   string         `json:"action" jsonschema:"Action: get, set, delete, list, clear, get_all"`
	Scope    string         `json:"scope,omitempty" jsonschema:"Scope: global, folder, page"`
	ScopeKey string         `json:"scope_key,omitempty" jsonschema:"Scope key (URL for page, path for folder, empty for global)"`
	Key      string         `json:"key,omitempty" jsonschema:"Key (required for get, set, delete)"`
	Value    interface{}    `json:"value,omitempty" jsonschema:"Value to store (required for set)"`
	Metadata map[string]any `json:"metadata,omitempty" jsonschema:"Optional metadata"`
}

// StoreOutput represents output from the store tool.
type StoreOutput struct {
	Success bool                         `json:"success"`
	Entry   *StoreEntryOutput            `json:"entry,omitempty"`
	Entries map[string]*StoreEntryOutput `json:"entries,omitempty"`
	Keys    []string                     `json:"keys,omitempty"`
	Count   int                          `json:"count,omitempty"`
	Message string                       `json:"message,omitempty"`
	Error   string                       `json:"error,omitempty"`
}

// StoreEntryOutput represents a single store entry.
type StoreEntryOutput struct {
	Value     interface{}    `json:"value"`
	Type      string         `json:"type"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// RegisterStoreTool registers the store MCP tool with the server.
func RegisterStoreTool(server *mcp.Server, dt *DaemonTools) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "store",
		Description: `Persistent key-value storage with scoped namespaces.

Actions:
  get: Retrieve a value by key
  set: Store a value by key
  delete: Remove a value by key
  list: List all keys in a scope
  clear: Clear all values in a scope
  get_all: Get all key-value pairs in a scope

Scopes:
  global: Shared across all contexts (scope_key: empty)
  folder: Per-directory storage (scope_key: directory path)
  page: Per-URL storage (scope_key: page URL)

Examples:
  store {action: "set", scope: "global", key: "api_key", value: "abc123"}
  store {action: "get", scope: "global", key: "api_key"}
  store {action: "set", scope: "page", scope_key: "http://localhost:3000", key: "user_id", value: 42}
  store {action: "list", scope: "folder", scope_key: "/home/user/project"}
  store {action: "get_all", scope: "global"}
  store {action: "delete", scope: "global", key: "api_key"}
  store {action: "clear", scope: "page", scope_key: "http://localhost:3000"}

Metadata:
  Optional metadata can be attached to values for additional context:
  store {action: "set", scope: "global", key: "config", value: {...}, metadata: {version: "1.0", author: "alice"}}

The store persists data across daemon restarts. Values are typed (string, number, boolean, object, array).`,
	}, dt.makeStoreHandler())
}

// makeStoreHandler creates a handler for the store tool.
func (dt *DaemonTools) makeStoreHandler() func(context.Context, *mcp.CallToolRequest, StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
		emptyOutput := StoreOutput{}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		switch input.Action {
		case "get":
			return dt.handleStoreGet(input)
		case "set":
			return dt.handleStoreSet(input)
		case "delete":
			return dt.handleStoreDelete(input)
		case "list":
			return dt.handleStoreList(input)
		case "clear":
			return dt.handleStoreClear(input)
		case "get_all":
			return dt.handleStoreGetAll(input)
		default:
			return errorResult(fmt.Sprintf("unknown action: %s (use: get, set, delete, list, clear, get_all)", input.Action)), emptyOutput, nil
		}
	}
}

func (dt *DaemonTools) handleStoreGet(input StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	emptyOutput := StoreOutput{}

	if input.Scope == "" {
		return errorResult("scope required (global, folder, page)"), emptyOutput, nil
	}
	if input.Key == "" {
		return errorResult("key required"), emptyOutput, nil
	}

	req := protocol.StoreGetRequest{
		Scope:    input.Scope,
		ScopeKey: input.ScopeKey,
		Key:      input.Key,
	}

	result, err := dt.client.StoreGet(req)
	if err != nil {
		return formatDaemonError(err, "store get"), emptyOutput, nil
	}

	// Check if entry exists
	if entryRaw, ok := result["entry"]; ok && entryRaw != nil {
		if entryMap, ok := entryRaw.(map[string]interface{}); ok {
			entry := &StoreEntryOutput{
				Value:     entryMap["value"],
				Type:      getString(entryMap, "type"),
				CreatedAt: getString(entryMap, "created_at"),
				UpdatedAt: getString(entryMap, "updated_at"),
			}
			if metadata, ok := entryMap["metadata"].(map[string]interface{}); ok {
				entry.Metadata = metadata
			}

			output := StoreOutput{
				Success: true,
				Entry:   entry,
			}
			return nil, output, nil
		}
	}

	return errorResult("key not found"), emptyOutput, nil
}

func (dt *DaemonTools) handleStoreSet(input StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	emptyOutput := StoreOutput{}

	if input.Scope == "" {
		return errorResult("scope required (global, folder, page)"), emptyOutput, nil
	}
	if input.Key == "" {
		return errorResult("key required"), emptyOutput, nil
	}
	if input.Value == nil {
		return errorResult("value required"), emptyOutput, nil
	}

	req := protocol.StoreSetRequest{
		Scope:    input.Scope,
		ScopeKey: input.ScopeKey,
		Key:      input.Key,
		Value:    input.Value,
		Metadata: input.Metadata,
	}

	err := dt.client.StoreSet(req)
	if err != nil {
		return formatDaemonError(err, "store set"), emptyOutput, nil
	}

	output := StoreOutput{
		Success: true,
		Message: "value stored successfully",
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleStoreDelete(input StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	emptyOutput := StoreOutput{}

	if input.Scope == "" {
		return errorResult("scope required (global, folder, page)"), emptyOutput, nil
	}
	if input.Key == "" {
		return errorResult("key required"), emptyOutput, nil
	}

	req := protocol.StoreDeleteRequest{
		Scope:    input.Scope,
		ScopeKey: input.ScopeKey,
		Key:      input.Key,
	}

	err := dt.client.StoreDelete(req)
	if err != nil {
		return formatDaemonError(err, "store delete"), emptyOutput, nil
	}

	output := StoreOutput{
		Success: true,
		Message: "value deleted successfully",
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleStoreList(input StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	emptyOutput := StoreOutput{}

	if input.Scope == "" {
		return errorResult("scope required (global, folder, page)"), emptyOutput, nil
	}

	req := protocol.StoreListRequest{
		Scope:    input.Scope,
		ScopeKey: input.ScopeKey,
	}

	result, err := dt.client.StoreList(req)
	if err != nil {
		return formatDaemonError(err, "store list"), emptyOutput, nil
	}

	count := getInt(result, "count")
	keysRaw, _ := result["keys"].([]interface{})

	keys := make([]string, 0, len(keysRaw))
	for _, k := range keysRaw {
		if keyStr, ok := k.(string); ok {
			keys = append(keys, keyStr)
		}
	}

	output := StoreOutput{
		Success: true,
		Count:   count,
		Keys:    keys,
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleStoreClear(input StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	emptyOutput := StoreOutput{}

	if input.Scope == "" {
		return errorResult("scope required (global, folder, page)"), emptyOutput, nil
	}

	req := protocol.StoreClearRequest{
		Scope:    input.Scope,
		ScopeKey: input.ScopeKey,
	}

	err := dt.client.StoreClear(req)
	if err != nil {
		return formatDaemonError(err, "store clear"), emptyOutput, nil
	}

	output := StoreOutput{
		Success: true,
		Message: "scope cleared successfully",
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleStoreGetAll(input StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	emptyOutput := StoreOutput{}

	if input.Scope == "" {
		return errorResult("scope required (global, folder, page)"), emptyOutput, nil
	}

	req := protocol.StoreGetAllRequest{
		Scope:    input.Scope,
		ScopeKey: input.ScopeKey,
	}

	result, err := dt.client.StoreGetAll(req)
	if err != nil {
		return formatDaemonError(err, "store get_all"), emptyOutput, nil
	}

	count := getInt(result, "count")
	entriesRaw, _ := result["entries"].(map[string]interface{})

	entries := make(map[string]*StoreEntryOutput)
	for key, entryRaw := range entriesRaw {
		if entryMap, ok := entryRaw.(map[string]interface{}); ok {
			entry := &StoreEntryOutput{
				Value:     entryMap["value"],
				Type:      getString(entryMap, "type"),
				CreatedAt: getString(entryMap, "created_at"),
				UpdatedAt: getString(entryMap, "updated_at"),
			}
			if metadata, ok := entryMap["metadata"].(map[string]interface{}); ok {
				entry.Metadata = metadata
			}
			entries[key] = entry
		}
	}

	output := StoreOutput{
		Success: true,
		Count:   count,
		Entries: entries,
	}

	return nil, output, nil
}
