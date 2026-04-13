# Claude Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/standardbeagle/claude-go.svg)](https://pkg.go.dev/github.com/standardbeagle/claude-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/standardbeagle/claude-go)](https://goreportcard.com/report/github.com/standardbeagle/claude-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A Go SDK for automating Claude Code CLI with concurrent session management, real-time streaming, custom tools, and hooks. This is the Go equivalent of the [Python Claude Agent SDK](https://github.com/anthropics/claude-agent-sdk-python).

## Features

- **Simple Query Function**: One-shot queries without session management
- **Concurrent Sessions**: Run multiple Claude sessions simultaneously with goroutines
- **Structured Message Types**: Typed messages (AssistantMessage, UserMessage, ResultMessage, etc.)
- **Custom Tools (MCP)**: Define in-process MCP tools that run directly in your Go application
- **Hooks**: Intercept and control tool execution with PreToolUse, PostToolUse hooks
- **JSON Streaming**: Real-time streaming with typed content blocks
- **CLI Discovery**: Automatic detection of Claude CLI in PATH and common locations
- **Comprehensive Errors**: Typed errors for precise error handling
- **Session Management**: Create, manage, and reuse conversation sessions

## Installation

```bash
go get github.com/standardbeagle/claude-go
```

## Quick Start

### Simple One-Shot Query

```go
package main

import (
    "context"
    "fmt"
    "log"

    claude "github.com/standardbeagle/claude-go"
)

func main() {
    ctx := context.Background()

    messages, err := claude.Query(ctx, "What is 2+2?", nil)
    if err != nil {
        log.Fatal(err)
    }

    for _, msg := range messages {
        if text := claude.GetText(msg); text != "" {
            fmt.Println(text)
        }
    }
}
```

### Client-Based Usage

```go
client := claude.New(&claude.AgentOptions{
    PermissionMode: claude.PermissionModeBypassPermission,
    Model:          "sonnet",
})
defer client.Close()

resp, err := client.Query(ctx, &claude.QueryRequest{
    Prompt: "Write a hello world function in Go",
})
if err != nil {
    log.Fatal(err)
}

for msg := range resp.Messages {
    fmt.Printf("%s: %s\n", msg.Type, msg.Content)
}
```

## Custom Tools (In-Process MCP Servers)

Define custom tools that Claude can use during conversations:

```go
// Create a tool using the builder pattern
addTool := claude.Tool("add", "Add two numbers").
    Param("a", "number", "First number").
    Param("b", "number", "Second number").
    Required("a", "b").
    HandlerFunc(func(ctx context.Context, args map[string]interface{}) (string, error) {
        a := args["a"].(float64)
        b := args["b"].(float64)
        return fmt.Sprintf("%g", a+b), nil
    })

// Create an SDK MCP server
server := claude.CreateSDKMCPServer("calculator", "1.0.0", addTool)

// Register with client
client := claude.New(&claude.AgentOptions{
    PermissionMode: claude.PermissionModeBypassPermission,
    AllowedTools:   []string{"mcp__calculator__add"},
})
client.RegisterSDKMCPServer("calculator", server)
```

## Hooks

Intercept and control tool execution:

```go
// Create a hook to block dangerous commands
bashBlocker := func(ctx context.Context, input claude.HookInput, toolUseID string, hookCtx claude.HookContext) (*claude.HookOutput, error) {
    if preInput, ok := input.(claude.PreToolUseInput); ok {
        if cmd, ok := preInput.ToolInput["command"].(string); ok {
            if strings.Contains(cmd, "rm -rf") {
                return &claude.HookOutput{
                    PermissionDecision: claude.PermissionBehaviorDeny,
                    PermissionDecisionReason: "Dangerous command blocked",
                }, nil
            }
        }
    }
    return nil, nil
}

registry := claude.NewHookRegistry()
registry.Register(claude.HookEventPreToolUse, []claude.HookMatcher{
    {Matcher: "Bash", Hooks: []claude.HookCallback{bashBlocker}},
})

client := claude.New(&claude.AgentOptions{
    Hooks: registry,
})
```

## Configuration Options (AgentOptions)

```go
type AgentOptions struct {
    // Model configuration
    Model             string         // Model to use (e.g., "sonnet", "opus")
    MaxThinkingTokens int            // Maximum tokens for thinking

    // System prompt
    SystemPrompt string

    // Tool configuration
    AllowedTools    []string
    DisallowedTools []string

    // Permission handling
    PermissionMode PermissionMode // default, acceptEdits, plan, bypassPermissions

    // Resource limits
    MaxTurns     int
    MaxBudgetUSD float64

    // Session management
    Resume               string
    ContinueConversation bool

    // MCP servers
    MCPServers    MCPServers
    MCPConfigPath string

    // Hooks
    Hooks *HookRegistry

    // File management
    WorkingDirectory string
    AddDirectories   []string

    // Environment
    Environment map[string]string
    CLIPath     string

    // Debug
    Debug   bool
    Verbose bool
}
```

## Stream-JSON Format

Claude Code's `--output-format stream-json` produces JSONL (one JSON object per line).
Content is nested under a `message` key for assistant and user messages:

```jsonl
{"type":"system","subtype":"init","session_id":"abc-123"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Hello!"}],"model":"claude-sonnet-4-20250514"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"main.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":"package main..."}]}}
{"type":"result","session_id":"abc-123","duration_ms":5000,"result":"Done.","total_cost_usd":0.05}
```

`ParseMessage` handles this nested format automatically, extracting `message.content`
into `AssistantMessage.Content` and `UserMessage.Content`.

## Structured Message Types

Parse typed messages from Claude:

```go
msg, err := claude.ParseMessage(jsonData)

switch m := msg.(type) {
case claude.AssistantMessage:
    for _, block := range m.Content {
        switch b := block.(type) {
        case claude.TextBlock:
            fmt.Println(b.Text)
        case claude.ToolUseBlock:
            fmt.Printf("Tool: %s(%v)\n", b.Name, b.Input)
        case claude.ThinkingBlock:
            // Claude's internal reasoning (extended thinking)
        case claude.ToolResultBlock:
            // Result from a tool execution
        }
    }
case claude.SystemMessage:
    // Subtypes: "init", "hook_started", "hook_response", etc.
    fmt.Printf("System: %s\n", m.Subtype)
case claude.ResultMessage:
    fmt.Printf("Cost: $%.4f, Turns: %d\n", m.TotalCostUSD, m.NumTurns)
    fmt.Println(m.Result) // Complete response text
case claude.StreamEvent:
    // Raw streaming events (partial content deltas)
}
```

### Message Types

| Type | Description |
|------|-------------|
| `SystemMessage` | System events: `init`, `hook_started`, `hook_response` |
| `AssistantMessage` | Claude's response with content blocks |
| `UserMessage` | User input or tool results |
| `ResultMessage` | Final summary: session ID, cost, duration, token usage |
| `StreamEvent` | Raw streaming events |

### Content Block Types

| Block | Description |
|-------|-------------|
| `TextBlock` | Plain text response |
| `ThinkingBlock` | Extended thinking (internal reasoning) |
| `ToolUseBlock` | Tool invocation with name, ID, and input |
| `ToolResultBlock` | Tool execution result |

## Streaming with QueryIterator

For real-time message processing, use `QueryIterator`:

```go
iter, err := claude.NewQueryIterator(ctx, "Fix the bug in main.go", &claude.AgentOptions{
    PermissionMode: claude.PermissionModeBypassPermission,
})
if err != nil {
    log.Fatal(err)
}
defer iter.Close()

// Channel-based streaming
for {
    select {
    case msg, ok := <-iter.Messages():
        if !ok {
            return // done
        }
        switch m := msg.(type) {
        case claude.AssistantMessage:
            if text := claude.GetText(m); text != "" {
                fmt.Print(text)
            }
        case claude.ResultMessage:
            fmt.Printf("\nSession: %s, Cost: $%.4f\n", m.SessionID, m.TotalCostUSD)
            return
        }
    case err := <-iter.Errors():
        log.Printf("Error: %v", err)
    }
}
```

Or use the simpler `Next()` iterator:

```go
for {
    msg, err := iter.Next()
    if err != nil {
        log.Fatal(err)
    }
    if msg == nil {
        break // done
    }
    // handle msg...
}
```

## Error Handling

The SDK provides typed errors for precise error handling:

```go
resp, err := client.Query(ctx, req)
if err != nil {
    switch {
    case claude.IsCLINotFound(err):
        log.Fatal("Install Claude CLI: npm install -g @anthropic-ai/claude-code")
    case claude.IsProcessError(err):
        log.Fatal("CLI process failed")
    case claude.IsCLIConnectionError(err):
        log.Fatal("Connection error")
    default:
        log.Fatal(err)
    }
}
```

Error types:
- `ClaudeSDKError` - Base error type
- `CLINotFoundError` - Claude CLI not installed
- `CLIConnectionError` - Connection issues
- `ProcessError` - CLI process failures
- `CLIJSONDecodeError` - JSON parsing errors
- `MessageParseError` - Message parsing errors
- `SessionClosedError` - Using a closed session
- `ToolError` - Tool execution failures
- `HookError` - Hook execution failures

## Session Management

### Multi-turn Conversations

```go
client := claude.New(&claude.AgentOptions{
    PermissionMode: claude.PermissionModeBypassPermission,
    Interactive:    true,
})

resp, _ := client.Query(ctx, &claude.QueryRequest{
    Prompt:    "My name is Alice. Remember this.",
    SessionID: "my-session",
})

// Continue the conversation
session, _ := client.GetSession("my-session")
session.SendMessage("What is my name?") // Claude remembers "Alice"
```

### Concurrent Sessions

```go
var wg sync.WaitGroup

for _, prompt := range prompts {
    wg.Add(1)
    go func(p string) {
        defer wg.Done()
        resp, _ := client.Query(ctx, &claude.QueryRequest{Prompt: p})
        for msg := range resp.Messages {
            fmt.Println(msg.Content)
        }
    }(prompt)
}

wg.Wait()
```

## Examples

- **Basic Usage**: `examples/basic/main.go`
- **Concurrent Processing**: `examples/concurrent/main.go`
- **Interactive Session**: `examples/interactive/main.go`
- **Custom MCP Tools**: `examples/mcp/main.go`
- **Hooks**: `examples/hooks/main.go`

## Requirements

- Go 1.21 or later
- Claude CLI installed and accessible in PATH
- Valid Claude authentication

## CLI Discovery

The SDK automatically finds the Claude CLI in this order:
1. `CLAUDE_CLI_PATH` environment variable
2. Current working directory
3. System PATH
4. Common installation locations (npm, yarn, homebrew)

## Authentication

```bash
# Using Claude CLI setup
claude setup-token

# Or set environment variable
export ANTHROPIC_API_KEY=your-api-key
```

## Thread Safety

- Multiple goroutines can create sessions concurrently
- Sessions can be safely accessed from multiple goroutines
- Proper cleanup handled with context cancellation
- `client.Close()` safely shuts down all sessions

## License

MIT License
