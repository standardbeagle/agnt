package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// MCPServerType defines the type of MCP server.
type MCPServerType string

const (
	MCPServerTypeStdio MCPServerType = "stdio"
	MCPServerTypeSSE   MCPServerType = "sse"
	MCPServerTypeHTTP  MCPServerType = "http"
	MCPServerTypeSDK   MCPServerType = "sdk"
)

// MCPServerConfig is the interface for all MCP server configurations.
type MCPServerConfig interface {
	mcpServerConfig()
	GetType() MCPServerType
}

// MCPStdioServerConfig configures an MCP server running as a subprocess.
type MCPStdioServerConfig struct {
	Type    MCPServerType     `json:"type"` // Always "stdio"
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func (MCPStdioServerConfig) mcpServerConfig()       {}
func (MCPStdioServerConfig) GetType() MCPServerType { return MCPServerTypeStdio }

// MCPSSEServerConfig configures an MCP server using Server-Sent Events.
type MCPSSEServerConfig struct {
	Type    MCPServerType     `json:"type"` // Always "sse"
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (MCPSSEServerConfig) mcpServerConfig()       {}
func (MCPSSEServerConfig) GetType() MCPServerType { return MCPServerTypeSSE }

// MCPHTTPServerConfig configures an MCP server using HTTP.
type MCPHTTPServerConfig struct {
	Type    MCPServerType     `json:"type"` // Always "http"
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (MCPHTTPServerConfig) mcpServerConfig()       {}
func (MCPHTTPServerConfig) GetType() MCPServerType { return MCPServerTypeHTTP }

// MCPSDKServerConfig wraps an in-process SDK MCP server.
type MCPSDKServerConfig struct {
	Type   MCPServerType `json:"type"` // Always "sdk"
	Server *SDKMCPServer `json:"-"`
}

func (MCPSDKServerConfig) mcpServerConfig()       {}
func (MCPSDKServerConfig) GetType() MCPServerType { return MCPServerTypeSDK }

// ToolSchema defines the input schema for a tool.
type ToolSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]PropertySchema `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

// PropertySchema defines a property in a tool schema.
type PropertySchema struct {
	Type        string          `json:"type"`
	Description string          `json:"description,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
	Default     any             `json:"default,omitempty"`
	Items       *PropertySchema `json:"items,omitempty"` // For array types
}

// ToolDefinition defines a tool that can be called by Claude.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema ToolSchema  `json:"inputSchema"`
	Handler     ToolHandler `json:"-"`
}

// ToolResult is the result of a tool execution.
type ToolResult struct {
	Content []ToolResultContent `json:"content"`
	IsError bool                `json:"isError,omitempty"`
}

// ToolResultContent represents a single content item in a tool result.
type ToolResultContent struct {
	Type string `json:"type"` // "text", "image", "resource"
	Text string `json:"text,omitempty"`
	// For image type
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	// For resource type
	URI string `json:"uri,omitempty"`
}

// ToolHandler is the function signature for tool handlers.
type ToolHandler func(ctx context.Context, args map[string]interface{}) (*ToolResult, error)

// SDKMCPServer is an in-process MCP server.
type SDKMCPServer struct {
	name    string
	version string
	tools   map[string]*ToolDefinition
	mu      sync.RWMutex
}

// NewSDKMCPServer creates a new in-process MCP server.
func NewSDKMCPServer(name, version string) *SDKMCPServer {
	return &SDKMCPServer{
		name:    name,
		version: version,
		tools:   make(map[string]*ToolDefinition),
	}
}

// CreateSDKMCPServer creates an in-process MCP server with the given tools.
// This is the Go equivalent of Python's create_sdk_mcp_server().
func CreateSDKMCPServer(name, version string, tools ...*ToolDefinition) *SDKMCPServer {
	server := NewSDKMCPServer(name, version)
	for _, tool := range tools {
		server.RegisterTool(tool)
	}
	return server
}

// RegisterTool registers a tool with the server.
func (s *SDKMCPServer) RegisterTool(tool *ToolDefinition) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[tool.Name] = tool
}

// GetTool returns a tool by name.
func (s *SDKMCPServer) GetTool(name string) (*ToolDefinition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tool, ok := s.tools[name]
	return tool, ok
}

// ListTools returns all registered tools.
func (s *SDKMCPServer) ListTools() []*ToolDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tools := make([]*ToolDefinition, 0, len(s.tools))
	for _, tool := range s.tools {
		tools = append(tools, tool)
	}
	return tools
}

// ExecuteTool executes a tool by name with the given arguments.
func (s *SDKMCPServer) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	tool, ok := s.GetTool(name)
	if !ok {
		return nil, NewToolError(fmt.Sprintf("tool '%s' not found", name), name, "", nil)
	}
	return tool.Handler(ctx, args)
}

// Name returns the server name.
func (s *SDKMCPServer) Name() string {
	return s.name
}

// Version returns the server version.
func (s *SDKMCPServer) Version() string {
	return s.version
}

// ToConfig returns an MCPSDKServerConfig for this server.
func (s *SDKMCPServer) ToConfig() MCPSDKServerConfig {
	return MCPSDKServerConfig{
		Type:   MCPServerTypeSDK,
		Server: s,
	}
}

// Tool creates a new tool definition with a builder pattern.
// This is the Go equivalent of Python's @tool decorator.
func Tool(name, description string) *ToolBuilder {
	return &ToolBuilder{
		definition: &ToolDefinition{
			Name:        name,
			Description: description,
			InputSchema: ToolSchema{
				Type:       "object",
				Properties: make(map[string]PropertySchema),
			},
		},
	}
}

// ToolBuilder provides a fluent API for building tool definitions.
type ToolBuilder struct {
	definition *ToolDefinition
}

// Param adds a parameter to the tool schema.
func (b *ToolBuilder) Param(name string, paramType string, description string) *ToolBuilder {
	b.definition.InputSchema.Properties[name] = PropertySchema{
		Type:        paramType,
		Description: description,
	}
	return b
}

// ParamWithEnum adds a parameter with enum constraints.
func (b *ToolBuilder) ParamWithEnum(name string, paramType string, description string, enum []string) *ToolBuilder {
	b.definition.InputSchema.Properties[name] = PropertySchema{
		Type:        paramType,
		Description: description,
		Enum:        enum,
	}
	return b
}

// ParamWithDefault adds a parameter with a default value.
func (b *ToolBuilder) ParamWithDefault(name string, paramType string, description string, defaultVal any) *ToolBuilder {
	b.definition.InputSchema.Properties[name] = PropertySchema{
		Type:        paramType,
		Description: description,
		Default:     defaultVal,
	}
	return b
}

// Required marks parameters as required.
func (b *ToolBuilder) Required(names ...string) *ToolBuilder {
	b.definition.InputSchema.Required = append(b.definition.InputSchema.Required, names...)
	return b
}

// Handler sets the tool handler function.
func (b *ToolBuilder) Handler(handler ToolHandler) *ToolDefinition {
	b.definition.Handler = handler
	return b.definition
}

// HandlerFunc is a convenience for creating simple string-returning handlers.
func (b *ToolBuilder) HandlerFunc(fn func(ctx context.Context, args map[string]interface{}) (string, error)) *ToolDefinition {
	b.definition.Handler = func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		text, err := fn(ctx, args)
		if err != nil {
			return &ToolResult{
				Content: []ToolResultContent{{Type: "text", Text: err.Error()}},
				IsError: true,
			}, nil
		}
		return &ToolResult{
			Content: []ToolResultContent{{Type: "text", Text: text}},
		}, nil
	}
	return b.definition
}

// TextResult creates a simple text tool result.
func TextResult(text string) *ToolResult {
	return &ToolResult{
		Content: []ToolResultContent{
			{Type: "text", Text: text},
		},
	}
}

// ErrorResult creates an error tool result.
func ErrorResult(err error) *ToolResult {
	return &ToolResult{
		Content: []ToolResultContent{
			{Type: "text", Text: err.Error()},
		},
		IsError: true,
	}
}

// ImageResult creates an image tool result.
func ImageResult(data string, mimeType string) *ToolResult {
	return &ToolResult{
		Content: []ToolResultContent{
			{Type: "image", Data: data, MimeType: mimeType},
		},
	}
}

// MCPServers is a map of server names to their configurations.
type MCPServers map[string]MCPServerConfig

// MCPServerManager manages MCP server instances.
type MCPServerManager struct {
	servers    map[string]MCPServerConfig
	sdkServers map[string]*SDKMCPServer
	mu         sync.RWMutex
}

// NewMCPServerManager creates a new MCP server manager.
func NewMCPServerManager() *MCPServerManager {
	return &MCPServerManager{
		servers:    make(map[string]MCPServerConfig),
		sdkServers: make(map[string]*SDKMCPServer),
	}
}

// Register registers an MCP server.
func (m *MCPServerManager) Register(name string, config MCPServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[name] = config
	if sdk, ok := config.(MCPSDKServerConfig); ok {
		m.sdkServers[name] = sdk.Server
	}
}

// RegisterSDKServer registers an SDK MCP server.
func (m *MCPServerManager) RegisterSDKServer(name string, server *SDKMCPServer) {
	m.Register(name, server.ToConfig())
}

// Get returns an MCP server configuration by name.
func (m *MCPServerManager) Get(name string) (MCPServerConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	config, ok := m.servers[name]
	return config, ok
}

// GetSDKServer returns an SDK MCP server by name.
func (m *MCPServerManager) GetSDKServer(name string) (*SDKMCPServer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	server, ok := m.sdkServers[name]
	return server, ok
}

// ExecuteSDKTool executes a tool on an SDK server.
func (m *MCPServerManager) ExecuteSDKTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (*ToolResult, error) {
	server, ok := m.GetSDKServer(serverName)
	if !ok {
		return nil, NewToolError(fmt.Sprintf("SDK server '%s' not found", serverName), toolName, "", nil)
	}
	return server.ExecuteTool(ctx, toolName, args)
}

// ToJSON returns a JSON representation of the servers for CLI configuration.
func (m *MCPServerManager) ToJSON() (json.RawMessage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Filter out SDK servers (they're handled in-process)
	external := make(map[string]MCPServerConfig)
	for name, config := range m.servers {
		if config.GetType() != MCPServerTypeSDK {
			external[name] = config
		}
	}

	if len(external) == 0 {
		return nil, nil
	}

	return json.Marshal(external)
}

// AllowedToolNames returns tool names in the format expected by the CLI.
// For SDK servers, returns "mcp__<server>__<tool>" format.
func (m *MCPServerManager) AllowedToolNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var names []string
	for serverName, server := range m.sdkServers {
		for _, tool := range server.ListTools() {
			names = append(names, fmt.Sprintf("mcp__%s__%s", serverName, tool.Name))
		}
	}
	return names
}

// GetAllSDKTools returns all tools from all SDK servers.
func (m *MCPServerManager) GetAllSDKTools() map[string]*ToolDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tools := make(map[string]*ToolDefinition)
	for serverName, server := range m.sdkServers {
		for _, tool := range server.ListTools() {
			key := fmt.Sprintf("mcp__%s__%s", serverName, tool.Name)
			tools[key] = tool
		}
	}
	return tools
}
